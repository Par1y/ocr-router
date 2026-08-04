package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"ocr-router/internal/config"
	"ocr-router/internal/logger"
	"ocr-router/internal/ocr"
)

// exitError carries a process exit code out of a cobra RunE so the extract
// command can honour the 0/10/20/130 contract without calling os.Exit itself
// (which would skip deferred cleanup). Execute() maps it to os.Exit.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// extractOpts bundles the resolved CLI flags for one extract invocation.
type extractOpts struct {
	provider      string
	pages         []int // nil = all pages
	pageWorkers   int
	window        int
	skipExisting  bool
	overlapRender bool
	blankSkip     bool
	progressMode  string
}

var extractCmd = &cobra.Command{
	Use:   "extract [path]",
	Short: "Structured per-page OCR extraction with a machine-readable manifest",
	Long: `Extract OCR text from an image, PDF, or directory, always producing one
structured JSON file per page (success AND failure) plus one manifest per input
that the caller reads as the single source of truth. Supports arbitrary page
sets, page-level parallelism, JSONL progress, resumable runs, and graceful
cancellation.

Exit codes: 0 all pages OK; 10 completed with failed pages (see manifest);
20 fatal at startup (no output produced); 130 cancelled.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runExtract,
}

func init() {
	extractCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	extractCmd.Flags().StringP("provider", "p", "", "OCR provider to use (default: fallback engine)")
	extractCmd.Flags().String("out", "", "Output directory for per-page JSON (default: <input dir>/ocr_results)")
	extractCmd.Flags().String("manifest", "", "Manifest path for a single input (default: <out>/<base>.manifest.json)")
	extractCmd.Flags().String("pages", "", "PDF page set, e.g. 3,7,10-12 (default: all)")
	extractCmd.Flags().Int("page-workers", 1, "Pages OCR'd concurrently within a window")
	extractCmd.Flags().Int("window", 0, "PDF sliding-window size (overrides pdf.window_size; default 20)")
	extractCmd.Flags().Bool("skip-existing", false, "Skip pages whose JSON already exists with status ok (resumable)")
	extractCmd.Flags().Bool("overlap-render", false, "Pre-render the next window while OCR'ing the current one")
	extractCmd.Flags().Bool("no-blank-skip", false, "Do not short-circuit near-blank pages (always OCR)")
	extractCmd.Flags().Bool("recursive", false, "Recurse into subdirectories when the input is a directory")
	extractCmd.Flags().String("progress", "text", "Progress output: json | text | none")
}

// runExtract is the extract command entrypoint. It returns an exitError to
// signal the process exit code; a nil return means exit 0.
func runExtract(cmd *cobra.Command, args []string) error {
	input := args[0]

	configFile, _ := cmd.Flags().GetString("config")
	provider, _ := cmd.Flags().GetString("provider")
	outDir, _ := cmd.Flags().GetString("out")
	manifestOverride, _ := cmd.Flags().GetString("manifest")
	pagesSpec, _ := cmd.Flags().GetString("pages")
	pageWorkers, _ := cmd.Flags().GetInt("page-workers")
	window, _ := cmd.Flags().GetInt("window")
	skipExisting, _ := cmd.Flags().GetBool("skip-existing")
	overlapRender, _ := cmd.Flags().GetBool("overlap-render")
	noBlankSkip, _ := cmd.Flags().GetBool("no-blank-skip")
	recursive, _ := cmd.Flags().GetBool("recursive")
	progressMode, _ := cmd.Flags().GetString("progress")

	switch progressMode {
	case "json", "text", "none":
	default:
		fmt.Fprintf(os.Stderr, "invalid --progress %q (want json|text|none)\n", progressMode)
		return exitError{20}
	}
	if pageWorkers < 1 {
		pageWorkers = 1
	}

	pages, err := parsePages(pagesSpec)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitError{20}
	}

	// Load config. A bad config yields no output -> fatal (20).
	cfg, err := config.Load(configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		return exitError{20}
	}

	log, err := logger.New(cfg.Logging.Level, cfg.Logging.Format, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logger: %v\n", err)
		return exitError{20}
	}
	defer log.Close()

	providers := createProviders(cfg, log)
	if len(providers) == 0 {
		fmt.Fprintln(os.Stderr, "no OCR providers enabled in config")
		return exitError{20}
	}
	if provider != "" {
		if _, ok := providers[provider]; !ok {
			fmt.Fprintf(os.Stderr, "provider not found: %s\n", provider)
			return exitError{20}
		}
	}
	evaluator := ocr.NewEvaluator(cfg.Evaluator, log)
	engine := ocr.NewFallbackEngine(providers, cfg.Fallback, evaluator, log)

	var renderer *ocr.PDFRenderer
	if cfg.PDF.Enabled {
		renderer = ocr.NewPDFRenderer(cfg.PDF)
	}

	win := window
	if win <= 0 {
		win = cfg.PDF.WindowSize
	}
	if win < 1 {
		win = 20
	}

	opts := extractOpts{
		provider:      provider,
		pages:         pages,
		pageWorkers:   pageWorkers,
		window:        win,
		skipExisting:  skipExisting,
		overlapRender: overlapRender,
		blankSkip:     !noBlankSkip,
		progressMode:  progressMode,
	}

	// Resolve the input list. A directory expands to its image/PDF files.
	inputs, err := resolveExtractInputs(input, recursive)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitError{20}
	}
	if len(inputs) == 0 {
		fmt.Fprintln(os.Stderr, "no image or PDF inputs found")
		return exitError{20}
	}

	// A PDF input with PDF support disabled produces no output -> fatal.
	for _, in := range inputs {
		if ocr.IsPDF(in) && renderer == nil {
			fmt.Fprintln(os.Stderr, "pdf support is disabled in config (set pdf.enabled: true)")
			return exitError{20}
		}
	}

	prog := newProgressEmitter(progressMode)

	// SIGINT/SIGTERM -> cancel context. In-flight renders are killed via
	// exec.CommandContext; OCR HTTP calls unwind on the cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\n[extract] cancellation requested, flushing manifest...")
			cancel()
		case <-ctx.Done():
		}
	}()

	// manifestOverride only applies to a single input; ignore it otherwise.
	if len(inputs) > 1 {
		manifestOverride = ""
	}

	var totalFailed int
	var produced bool
	for _, in := range inputs {
		if ctx.Err() != nil {
			break
		}
		_, failed, ok := extractOneInput(ctx, engine, providers, renderer, cfg, in, outDir, manifestOverride, opts, prog)
		totalFailed += failed
		produced = produced || ok
	}

	if ctx.Err() != nil {
		return exitError{130}
	}
	if !produced {
		return exitError{20}
	}
	if totalFailed > 0 {
		return exitError{10}
	}
	return nil
}

// resolveExtractInputs returns the list of image/PDF files to process. A single
// file is returned as-is; a directory is expanded via findImages (which already
// filters to supported extensions and honours recursion).
func resolveExtractInputs(input string, recursive bool) ([]string, error) {
	info, err := os.Stat(input)
	if err != nil {
		return nil, fmt.Errorf("input not found: %w", err)
	}
	if !info.IsDir() {
		return []string{input}, nil
	}
	return findImages(input, recursive)
}

// resolveManifestPath returns the manifest path for one input: the explicit
// override when given, else <outDir>/<base>.manifest.json.
func resolveManifestPath(override, outDir, base string) string {
	if override != "" {
		return override
	}
	return filepath.Join(outDir, base+".manifest.json")
}

// extractOneInput processes a single image or PDF, writing per-page JSON and a
// manifest. It returns (pagesOK, pagesFailed, producedOutput).
func extractOneInput(
	ctx context.Context,
	engine *ocr.FallbackEngine,
	providers map[string]ocr.Provider,
	renderer *ocr.PDFRenderer,
	cfg *config.Config,
	input, outDir, manifestOverride string,
	opts extractOpts,
	prog *progressEmitter,
) (int, int, bool) {
	// Resolve output dir: explicit flag, else <input dir>/ocr_results.
	dir := outDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(input), "ocr_results")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create output dir %q: %v\n", dir, err)
		return 0, 0, false
	}

	base := baseName(input)
	manifestPath := resolveManifestPath(manifestOverride, dir, base)
	started := time.Now()

	if ocr.IsPDF(input) {
		return extractPDF(ctx, engine, providers, renderer, cfg, input, dir, base, manifestPath, opts, prog, started)
	}
	return extractImage(ctx, engine, providers, cfg, input, dir, base, manifestPath, opts, prog, started)
}

// extractImage handles a single (non-PDF) image as page 1.
func extractImage(
	ctx context.Context,
	engine *ocr.FallbackEngine,
	providers map[string]ocr.Provider,
	cfg *config.Config,
	input, dir, base, manifestPath string,
	opts extractOpts,
	prog *progressEmitter,
	started time.Time,
) (int, int, bool) {
	mw := newManifestWriter(manifestPath, &Manifest{
		Source:           filepath.Base(input),
		Type:             "image",
		WindowSize:       0,
		PageWorkers:      opts.pageWorkers,
		EvaluatorEnabled: cfg.Evaluator.Enabled,
		Provider:         opts.provider,
		Status:           manifestRunning,
		StartedAt:        started.Format(time.RFC3339),
	}, 1)

	prog.start(filepath.Base(input), 1)

	var done atomic.Int64
	ocrPage(ctx, engine, providers, opts, mw, prog, &done, 1, 1, input, filepath.Base(input), dir, base)

	ok, failed := mw.counts()
	status := manifestOK
	if ctx.Err() != nil {
		status = manifestCancelled
	} else if failed > 0 {
		status = manifestError
	}
	mw.finalize(status, started)
	prog.done(filepath.Base(input), ok, failed, manifestPath)
	return ok, failed, true
}

// extractPDF handles a PDF: it OCRs the requested page set window by window,
// with page-level parallelism, resumable skip-existing, optional render/OCR
// overlap, and an incrementally-flushed manifest.
func extractPDF(
	ctx context.Context,
	engine *ocr.FallbackEngine,
	providers map[string]ocr.Provider,
	renderer *ocr.PDFRenderer,
	cfg *config.Config,
	input, dir, base, manifestPath string,
	opts extractOpts,
	prog *progressEmitter,
	started time.Time,
) (int, int, bool) {
	totalPages, _ := renderer.CountPages(input)

	// Build the list of pages to attempt this run.
	var pageList []int
	if len(opts.pages) > 0 {
		for _, p := range opts.pages {
			if totalPages > 0 && p > totalPages {
				fmt.Fprintf(os.Stderr, "[extract] %s: skipping page %d (> %d pages)\n", base, p, totalPages)
				continue
			}
			pageList = append(pageList, p)
		}
	} else if totalPages > 0 {
		pageList = make([]int, totalPages)
		for i := range pageList {
			pageList[i] = i + 1
		}
	}

	mw := newManifestWriter(manifestPath, &Manifest{
		Source:           filepath.Base(input),
		Type:             "pdf",
		WindowSize:       opts.window,
		PageWorkers:      opts.pageWorkers,
		EvaluatorEnabled: cfg.Evaluator.Enabled,
		Provider:         opts.provider,
		Status:           manifestRunning,
		StartedAt:        started.Format(time.RFC3339),
	}, totalPages)

	// Unknown page count with no explicit --pages: fall back to a serial,
	// open-ended window sweep (rare; only when pdfinfo is unavailable).
	if len(pageList) == 0 {
		return extractPDFUnbounded(ctx, engine, providers, renderer, input, dir, base, manifestPath, opts, prog, mw, started)
	}

	attemptTotal := len(pageList)
	prog.start(filepath.Base(input), attemptTotal)

	var done atomic.Int64

	// Partition into pages already done (skip-existing) and pages to OCR.
	var todo []int
	for _, p := range pageList {
		if opts.skipExisting {
			if ex, ok := readExistingPage(dir, base, p); ok && ex.Status == statusOK {
				mw.upsertPage(ManifestPage{Page: p, Status: statusOK, Score: ex.Score, File: pageJSONName(base, p)})
				prog.page(p, statusOK, ex.Score, ex.ScorePresent, int(done.Add(1)), attemptTotal, "")
				continue
			}
		}
		todo = append(todo, p)
	}

	// Window ranges: contiguous runs of todo pages, each split by window size.
	var windows [][2]int
	for _, run := range contiguousRuns(todo) {
		for lo := run[0]; lo <= run[1]; lo += opts.window {
			hi := lo + opts.window - 1
			if hi > run[1] {
				hi = run[1]
			}
			windows = append(windows, [2]int{lo, hi})
		}
	}

	processWindows(ctx, engine, providers, renderer, input, dir, base, opts, mw, prog, &done, attemptTotal, windows)

	ok, failed := mw.counts()
	status := manifestOK
	if ctx.Err() != nil {
		status = manifestCancelled
	} else if failed > 0 {
		status = manifestPartial
	}
	mw.finalize(status, started)
	prog.done(filepath.Base(input), ok, failed, manifestPath)
	return ok, failed, true
}

// PROCESSING_HELPERS_PLACEHOLDER

// processWindows renders and OCRs each window range. With overlapRender a
// producer goroutine renders the next window (bounded to one ahead, so at most
// two windows' images live on disk) while the current window is being OCR'd.
func processWindows(
	ctx context.Context,
	engine *ocr.FallbackEngine,
	providers map[string]ocr.Provider,
	renderer *ocr.PDFRenderer,
	input, dir, base string,
	opts extractOpts,
	mw *manifestWriter,
	prog *progressEmitter,
	done *atomic.Int64,
	attemptTotal int,
	windows [][2]int,
) {
	sourceFile := filepath.Base(input)

	if opts.overlapRender {
		type rendered struct {
			pages  []ocr.PageImage
			tmp    string
			err    error
			lo, hi int
		}
		ch := make(chan rendered, 1)
		go func() {
			defer close(ch)
			for _, w := range windows {
				if ctx.Err() != nil {
					return
				}
				pages, tmp, err := renderer.RenderContext(ctx, input, w[0], w[1])
				select {
				case ch <- rendered{pages, tmp, err, w[0], w[1]}:
				case <-ctx.Done():
					ocr.Cleanup(tmp)
					return
				}
			}
		}()
		for r := range ch {
			if ctx.Err() != nil {
				ocr.Cleanup(r.tmp)
				break
			}
			if r.err != nil {
				markWindowFailed(ctx, mw, prog, done, attemptTotal, base, sourceFile, dir, r.lo, r.hi, r.err)
				continue
			}
			processWindowPages(ctx, engine, providers, opts, mw, prog, done, attemptTotal, input, dir, base, r.lo, r.hi, r.pages)
			ocr.Cleanup(r.tmp)
		}
		return
	}

	for _, w := range windows {
		if ctx.Err() != nil {
			break
		}
		pages, tmp, err := renderer.RenderContext(ctx, input, w[0], w[1])
		if err != nil {
			markWindowFailed(ctx, mw, prog, done, attemptTotal, base, sourceFile, dir, w[0], w[1], err)
			continue
		}
		processWindowPages(ctx, engine, providers, opts, mw, prog, done, attemptTotal, input, dir, base, w[0], w[1], pages)
		ocr.Cleanup(tmp)
	}
}

// processWindowPages OCRs every rendered page of a window using up to
// opts.pageWorkers concurrent workers, then reconciles: any page in [lo,hi]
// the renderer failed to emit is marked failed so the manifest never silently
// drops a page.
func processWindowPages(
	ctx context.Context,
	engine *ocr.FallbackEngine,
	providers map[string]ocr.Provider,
	opts extractOpts,
	mw *manifestWriter,
	prog *progressEmitter,
	done *atomic.Int64,
	attemptTotal int,
	input, dir, base string,
	lo, hi int,
	pages []ocr.PageImage,
) {
	sourceFile := filepath.Base(input)
	rendered := make(map[int]bool, len(pages))

	sem := make(chan struct{}, opts.pageWorkers)
	var wg sync.WaitGroup
	for _, pg := range pages {
		if pg.Page < lo || pg.Page > hi {
			continue // defensive: ignore stray pages outside the window
		}
		rendered[pg.Page] = true
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(pg ocr.PageImage) {
			defer wg.Done()
			defer func() { <-sem }()
			ocrPage(ctx, engine, providers, opts, mw, prog, done, pg.Page, attemptTotal, pg.Path, sourceFile, dir, base)
		}(pg)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return
	}
	for p := lo; p <= hi; p++ {
		if !rendered[p] {
			markWindowFailed(ctx, mw, prog, done, attemptTotal, base, sourceFile, dir, p, p,
				fmt.Errorf("renderer produced no image for page %d", p))
		}
	}
}

// markWindowFailed records every page in [lo,hi] as failed with rerr. Used when
// a whole window's render fails or a specific page image is missing. It is a
// no-op under cancellation (those pages are not real failures).
func markWindowFailed(
	ctx context.Context,
	mw *manifestWriter,
	prog *progressEmitter,
	done *atomic.Int64,
	attemptTotal int,
	base, sourceFile, dir string,
	lo, hi int,
	rerr error,
) {
	if ctx.Err() != nil {
		return
	}
	msg := rerr.Error()
	for p := lo; p <= hi; p++ {
		pj := &PageJSON{Page: p, Status: statusFailed, Error: &msg, SourceFile: sourceFile, Image: pageImageName(base, p)}
		writePageJSON(dir, base, p, pj)
		mw.upsertPage(ManifestPage{Page: p, Status: statusFailed, File: pageJSONName(base, p), Error: &msg})
		prog.page(p, statusFailed, 0, false, int(done.Add(1)), attemptTotal, msg)
	}
}

// ocrPage OCRs a single rendered page image, applying the blank short-circuit,
// then writes its per-page JSON and updates the manifest + progress.
func ocrPage(
	ctx context.Context,
	engine *ocr.FallbackEngine,
	providers map[string]ocr.Provider,
	opts extractOpts,
	mw *manifestWriter,
	prog *progressEmitter,
	done *atomic.Int64,
	page, attemptTotal int,
	imagePath, sourceFile, dir, base string,
) {
	if ctx.Err() != nil {
		return
	}
	image := pageImageName(base, page)

	// Blank short-circuit: near-white pages skip the expensive OCR call.
	if opts.blankSkip {
		if blank, err := ocr.IsBlankImage(imagePath); err == nil && blank {
			pj := &PageJSON{Page: page, Status: statusOK, Text: "", ScorePresent: false, Blank: true, SourceFile: sourceFile, Image: image}
			writePageJSON(dir, base, page, pj)
			mw.upsertPage(ManifestPage{Page: page, Status: statusOK, File: pageJSONName(base, page)})
			prog.page(page, statusOK, 0, false, int(done.Add(1)), attemptTotal, "")
			return
		}
	}

	result, err := ocrRecognizeOne(ctx, engine, providers, opts.provider, imagePath)
	if err != nil {
		msg := err.Error()
		// Distinguish cancellation from a genuine page failure.
		if ctx.Err() != nil {
			cmsg := ctx.Err().Error()
			pj := &PageJSON{Page: page, Status: statusCancelled, Error: &cmsg, SourceFile: sourceFile, Image: image}
			writePageJSON(dir, base, page, pj)
			mw.upsertPage(ManifestPage{Page: page, Status: statusCancelled, File: pageJSONName(base, page), Error: &cmsg})
			return
		}
		pj := &PageJSON{Page: page, Status: statusFailed, Error: &msg, SourceFile: sourceFile, Image: image}
		writePageJSON(dir, base, page, pj)
		mw.upsertPage(ManifestPage{Page: page, Status: statusFailed, File: pageJSONName(base, page), Error: &msg})
		prog.page(page, statusFailed, 0, false, int(done.Add(1)), attemptTotal, msg)
		return
	}

	scorePresent := result.Evaluation != nil
	score := 0.0
	if scorePresent {
		score = result.Evaluation.Score
	}
	pj := &PageJSON{
		Page:           page,
		Status:         statusOK,
		Text:           result.Text,
		Score:          score,
		ScorePresent:   scorePresent,
		Provider:       result.Provider,
		Fallback:       result.Fallback,
		QualityWarning: result.QualityWarning,
		Blank:          false,
		BestScore:      result.BestScore,
		SourceFile:     sourceFile,
		Image:          image,
	}
	writePageJSON(dir, base, page, pj)
	mw.upsertPage(ManifestPage{Page: page, Status: statusOK, Score: score, File: pageJSONName(base, page)})
	prog.page(page, statusOK, score, scorePresent, int(done.Add(1)), attemptTotal, "")
}

// ocrRecognizeOne runs OCR for one page image via a specific provider or the
// fallback engine, with a per-page timeout derived from the parent context.
func ocrRecognizeOne(
	ctx context.Context,
	engine *ocr.FallbackEngine,
	providers map[string]ocr.Provider,
	provider, imagePath string,
) (*ocr.OCRResult, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req := &ocr.OCRRequest{ImagePath: imagePath}
	if provider != "" {
		return providers[provider].Recognize(cctx, req)
	}
	return engine.Recognize(cctx, req)
}

// extractPDFUnbounded is the fallback path when the PDF page count is unknown
// (pdfinfo unavailable) and no explicit --pages was given. It sweeps windows
// open-ended and serially; page-level parallelism and precise progress totals
// are not available here. Rare in practice since poppler ships with the tool.
func extractPDFUnbounded(
	ctx context.Context,
	engine *ocr.FallbackEngine,
	providers map[string]ocr.Provider,
	renderer *ocr.PDFRenderer,
	input, dir, base, manifestPath string,
	opts extractOpts,
	prog *progressEmitter,
	mw *manifestWriter,
	started time.Time,
) (int, int, bool) {
	prog.start(filepath.Base(input), 0)
	sourceFile := filepath.Base(input)
	var done atomic.Int64

	_ = processPDFWithWindow(renderer, input, 0, 0, opts.window, 0, func(task FileTask) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if opts.skipExisting {
			if ex, ok := readExistingPage(dir, base, task.PageNum); ok && ex.Status == statusOK {
				mw.upsertPage(ManifestPage{Page: task.PageNum, Status: statusOK, Score: ex.Score, File: pageJSONName(base, task.PageNum)})
				prog.page(task.PageNum, statusOK, ex.Score, ex.ScorePresent, int(done.Add(1)), 0, "")
				return nil
			}
		}
		ocrPage(ctx, engine, providers, opts, mw, prog, &done, task.PageNum, 0, task.ImagePath, sourceFile, dir, base)
		return nil
	})

	ok, failed := mw.counts()
	status := manifestOK
	if ctx.Err() != nil {
		status = manifestCancelled
	} else if failed > 0 {
		status = manifestPartial
	}
	mw.finalize(status, started)
	prog.done(filepath.Base(input), ok, failed, manifestPath)
	return ok, failed, true
}

// --- small on-disk helpers -------------------------------------------------

func pageJSONName(base string, page int) string  { return fmt.Sprintf("%s-%04d.json", base, page) }
func pageImageName(base string, page int) string { return fmt.Sprintf("%s-%04d.png", base, page) }

// readExistingPage loads a previously written per-page JSON, used by
// --skip-existing to decide whether a page can be reused.
func readExistingPage(dir, base string, page int) (*PageJSON, bool) {
	data, err := os.ReadFile(filepath.Join(dir, pageJSONName(base, page)))
	if err != nil {
		return nil, false
	}
	var pj PageJSON
	if json.Unmarshal(data, &pj) != nil {
		return nil, false
	}
	return &pj, true
}

// writePageJSON writes a per-page JSON atomically (temp file + rename).
func writePageJSON(dir, base string, page int, pj *PageJSON) {
	data, err := json.MarshalIndent(pj, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(dir, pageJSONName(base, page))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

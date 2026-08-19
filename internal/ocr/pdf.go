package ocr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"ocr-router/internal/config"
)

// PDFRenderer rasterizes PDF files into per-page image files.
type PDFRenderer struct {
	cfg config.PDFConfig
}

// NewPDFRenderer creates a renderer bound to the given config.
func NewPDFRenderer(cfg config.PDFConfig) *PDFRenderer {
	return &PDFRenderer{cfg: cfg}
}

// IsPDF reports whether the given path looks like a PDF file (by extension).
func IsPDF(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".pdf"
}

// PageImage represents a single rasterized page.
type PageImage struct {
	Path  string // absolute path to the rendered image file
	Page  int    // 1-based page number
	MIME  string // image MIME type
	Bytes int64  // file size in bytes
}

// resolveBinary finds the absolute path of a tool in this priority order:
//  1. explicit config override (BinPath / InfoBinPath),
//  2. a binary shipped in the project's ./bin directory,
//  3. any directory on PATH.
//
// Candidate paths must resolve to an executable regular file; otherwise the
// candidate is skipped so a corrupt/leftover file in ./bin does not produce a
// confusing fork/exec error and the next candidate is tried instead.
// missingToolMsg formats a helpful, install-oriented message when nothing is found.
func (r *PDFRenderer) resolveBinary(name, override string) (string, error) {
	// 1. Explicit override: trust the user but still require it to be executable.
	if override != "" {
		if p, ok := usableBinary(override); ok {
			return p, nil
		}
		// If the override is just a name (not a path), fall through to LookPath.
		if p, err := exec.LookPath(override); err == nil {
			return p, nil
		}
	}
	// 2. Project ./bin/<name>.
	if p := inProjectBin(name); p != "" {
		if usable, ok := usableBinary(p); ok {
			return usable, nil
		}
	}
	// 3. PATH.
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s", missingToolMsg(name))
}

// usableBinary returns (cleaned path, true) when path exists, is a regular
// file, and is executable by the current user. Otherwise ( "", false).
func usableBinary(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		return "", false
	}
	// Require at least the user-execute bit; cross-user exec is covered by
	// exec.LookPath semantics on the next tier when this is from override.
	if info.Mode().Perm()&0o100 == 0 {
		return "", false
	}
	return path, true
}

// CountPages returns the number of pages in a PDF.
// It prefers pdfinfo; on failure it falls back to 0 which callers may
// interpret as "render whatever exists".
func (r *PDFRenderer) CountPages(pdfPath string) (int, error) {
	bin, err := r.resolveBinary("pdfinfo", r.cfg.InfoBinPath)
	if err != nil {
		// pdfinfo is optional; absence is not a hard error.
		return 0, nil
	}

	cmd := exec.Command(bin, pdfPath)
	out, err := cmd.Output()
	if err != nil {
		return 0, nil
	}
	re := regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)
	m := re.FindSubmatch(out)
	if len(m) < 2 {
		return 0, nil
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil || n < 0 {
		return 0, nil
	}
	return n, nil
}

// Render converts a PDF into one image per page and returns the page list.
// The caller is responsible for cleaning up the returned page files via
// Cleanup(). The temp directory used is also tracked for removal.
//
// first and last narrow the page range to render (1-based; 0 means unbounded
// on that end), enabling the CLI to process large PDFs in chunks. When both
// are 0 the whole document is rendered (still subject to cfg.MaxPages).
func (r *PDFRenderer) Render(pdfPath string, first, last int) ([]PageImage, string, error) {
	return r.RenderContext(context.Background(), pdfPath, first, last)
}

// RenderContext is Render bound to a context: when ctx is cancelled the child
// rasterizer process (and its process group on Unix) is killed and a render
// error is returned. Use this on the extract path so SIGTERM/SIGINT cancellation
// tears down in-flight rendering promptly instead of leaving orphaned processes.
func (r *PDFRenderer) RenderContext(ctx context.Context, pdfPath string, first, last int) ([]PageImage, string, error) {
	if _, err := os.Stat(pdfPath); err != nil {
		return nil, "", fmt.Errorf("pdf not found: %w", err)
	}

	tool, bin, err := r.resolveTool()
	if err != nil {
		return nil, "", err
	}

	tmpDir, err := os.MkdirTemp(r.cfg.TempDir, "ocr-pdf-")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	if err := r.renderWith(ctx, tool, bin, pdfPath, tmpDir, first, last); err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", err
	}

	pages, err := r.collectPages(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", err
	}

	if r.cfg.MaxPages > 0 && len(pages) > r.cfg.MaxPages {
		// Remove pages beyond the limit
		for _, p := range pages[r.cfg.MaxPages:] {
			os.Remove(p.Path)
		}
		pages = pages[:r.cfg.MaxPages]
	}

	return pages, tmpDir, nil
}

// resolveTool picks the renderer to use, returning the tool name and the
// absolute path of its binary. Discovery order:
//  1. explicit config (cfg.Tool + cfg.BinPath), validated to exist;
//  2. ./bin/<tool> shipped with the project;
//  3. PATH lookup; first of pdftoppm, then mutool.
//
// When cfg.Tool is set but that specific tool cannot be found, the function
// returns an error rather than silently falling back to another tool, so the
// user's explicit choice is respected.
func (r *PDFRenderer) resolveTool() (name string, bin string, err error) {
	// Explicit configured tool: must exist, do not fall back.
	if r.cfg.Tool != "" {
		p, e := r.resolveBinary(r.cfg.Tool, r.cfg.BinPath)
		if e != nil {
			return "", "", fmt.Errorf("configured pdf.tool %q not found: %v", r.cfg.Tool, e)
		}
		return r.cfg.Tool, p, nil
	}

	// Auto-detect: prefer pdftoppm, then mutool.
	for _, c := range []string{"pdftoppm", "mutool"} {
		if p, e := r.resolveBinary(c, ""); e == nil {
			return c, p, nil
		}
	}

	return "", "", fmt.Errorf("no PDF renderer available. %s",
		missingToolMsg(""))
}

// renderWith invokes the chosen renderer to produce images in tmpDir.
// first/last narrow the page range (1-based, 0 = unbounded); cfg.MaxPages
// is applied afterwards in collectPages-style trimming as a safety cap.
func (r *PDFRenderer) renderWith(ctx context.Context, name, bin, pdfPath, tmpDir string, first, last int) error {
	// DPI is guaranteed positive by config.setDefaults.
	dpi := r.cfg.DPI
	prefix := filepath.Join(tmpDir, "page")
	outPrefix := prefix + "-"

	// Build the effective last page: explicit CLI last, else cfg.MaxPages, else 0.
	maxLast := last
	if maxLast == 0 && r.cfg.MaxPages > 0 && (first <= 1 || first == 0) {
		maxLast = r.cfg.MaxPages
	}

	switch name {
	case "pdftoppm":
		args := []string{
			"-r", strconv.Itoa(dpi),
			"-png",
		}
		if first > 0 {
			args = append(args, "-f", strconv.Itoa(first))
		}
		if maxLast > 0 {
			args = append(args, "-l", strconv.Itoa(maxLast))
		}
		args = append(args, pdfPath, prefix)
		cmd := exec.CommandContext(ctx, bin, args...)
		setProcGroup(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("pdftoppm cancelled: %w", ctx.Err())
			}
			return fmt.Errorf("pdftoppm failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil

	case "mutool":
		// mutool draw -o prefix-%d.png -r <dpi> input.pdf
		format := r.cfg.Format
		if format == "" {
			format = "png"
		}
		pattern := outPrefix + "%d." + format
		args := []string{"draw", "-o", pattern, "-r", strconv.Itoa(dpi)}
		// mutool uses a "first-last" range selector.
		if first > 0 || maxLast > 0 {
			lo := first
			if lo < 1 {
				lo = 1
			}
			hi := maxLast
			if hi < 1 {
				hi = 0 // open-ended
			}
			if hi > 0 {
				args = append(args, fmt.Sprintf("%d-%d", lo, hi))
			} else {
				args = append(args, fmt.Sprintf("%d-N", lo))
			}
		} else if r.cfg.MaxPages > 0 {
			args = append(args, "1-"+strconv.Itoa(r.cfg.MaxPages))
		}
		args = append(args, pdfPath)
		cmd := exec.CommandContext(ctx, bin, args...)
		setProcGroup(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("mutool cancelled: %w", ctx.Err())
			}
			return fmt.Errorf("mutool failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil

	default:
		return fmt.Errorf("unsupported pdf tool: %s", name)
	}
}

// collectPages scans tmpDir for rendered page images and returns them ordered.
func (r *PDFRenderer) collectPages(tmpDir string) ([]PageImage, error) {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, err
	}

	var pages []PageImage
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".pdf" {
			continue
		}
		mime := mimeForExt(ext)
		if mime == "" {
			continue
		}
		page, ok := pageFromName(name)
		if !ok {
			continue
		}
		full := filepath.Join(tmpDir, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		pages = append(pages, PageImage{
			Path:  full,
			Page:  page,
			MIME:  mime,
			Bytes: info.Size(),
		})
	}

	if len(pages) == 0 {
		return nil, fmt.Errorf("pdf rendered no pages (empty or corrupted pdf?)")
	}

	// Sort by page number (stable enough given the small N).
	for i := 1; i < len(pages); i++ {
		for j := i; j > 0 && pages[j].Page < pages[j-1].Page; j-- {
			pages[j], pages[j-1] = pages[j-1], pages[j]
		}
	}
	return pages, nil
}

// pageFromName extracts the 1-based page number from rendered filenames.
// pdftoppm: page-1.png, page-01.png, page-001.png ...
// mutool:   page-1.png
func pageFromName(name string) (int, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	idx := strings.LastIndexByte(base, '-')
	if idx < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(base[idx+1:])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func mimeForExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return ""
	}
}

// Cleanup removes a temp directory created by Render, ignoring errors.
func Cleanup(dir string) {
	if dir != "" {
		os.RemoveAll(dir)
	}
}

// inProjectBin checks for a tool shipped inside the project's ./bin directory
// (with an OS-appropriate ".exe" suffix on Windows) and returns its absolute
// path when present.
//
// Lookup order:
//  1. bin/ directory relative to the running executable (release mode) —
//     this makes a bundled ./bin folder work for end users who don't have
//     go.mod in their working tree.
//  2. Walk up from the working directory until a go.mod sibling of `bin/`
//     is found (development mode).
func inProjectBin(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	// 1. Executable-relative bin/ (release mode — no go.mod needed).
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "bin", name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}

	// 2. Development mode: walk up from cwd looking for go.mod.
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			candidate := filepath.Join(dir, "bin", name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// missingToolMsg formats a user-actionable hint explaining how to obtain the
// PDF renderer. Pass an empty tool name for the generic "any tool" case.
func missingToolMsg(tool string) string {
	binDir := filepath.Join(projectRoot(), "bin")
	var specific string
	switch tool {
	case "pdftoppm", "pdfinfo":
		specific = fmt.Sprintf("install poppler-utils:\n  %s\n", popplerInstallHint())
	case "mutool":
		specific = fmt.Sprintf("install mupdf-tools:\n  %s\n", mupdfInstallHint())
	default:
		specific = fmt.Sprintf("install one of:\n  %s\n  %s\n",
			popplerInstallHint(), mupdfInstallHint())
	}
	return strings.TrimRight(specific, "\n") + fmt.Sprintf(
		"\nor place the `pdftoppm` / `mutool` binary in the project bin/ folder:\n  %s\n",
		binDir,
	)
}

func popplerInstallHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install poppler"
	case "windows":
		return "choco install poppler   (or download poppler and add it to PATH)"
	default:
		return "sudo apt-get install -y poppler-utils   # or: dnf install poppler-utils / pacman -S poppler"
	}
}

func mupdfInstallHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install mupdf-tools"
	case "windows":
		return "download mutool from mupdf.com and add it to PATH"
	default:
		return "sudo apt-get install -y mupdf-tools   # or: dnf install mupdf / pacman -S mupdf-tools"
	}
}

// projectRoot best-effort locates the dir containing go.mod (used to point the
// user at ./bin). Returns "" if not found.
func projectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return wd
		}
		dir = parent
	}
	return wd
}

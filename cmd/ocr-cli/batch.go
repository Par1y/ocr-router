package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"ocr-router/internal/config"
	"ocr-router/internal/logger"
	"ocr-router/internal/ocr"
)

var batchCmd = &cobra.Command{
	Use:   "batch [directory]",
	Short: "Batch OCR recognition",
	Long:  "Perform OCR recognition on all images and PDF files in a directory. PDFs are rasterized in a sliding window so large files do not explode disk usage.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := args[0]

		// Get flags
		configFile, _ := cmd.Flags().GetString("config")
		provider, _ := cmd.Flags().GetString("provider")
		outputDir, _ := cmd.Flags().GetString("output")
		workers, _ := cmd.Flags().GetInt("workers")
		recursive, _ := cmd.Flags().GetBool("recursive")
		skipExisting, _ := cmd.Flags().GetBool("skip-existing")
		saveJSON, _ := cmd.Flags().GetBool("save-json")

		// Load config
		cfg, err := config.Load(configFile)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Create logger
		log, err := logger.New(cfg.Logging.Level, cfg.Logging.Format, "")
		if err != nil {
			return fmt.Errorf("failed to create logger: %w", err)
		}
		defer log.Close()

		// Create providers
		providers := createProviders(cfg, log)

		// Create evaluator
		evaluator := ocr.NewEvaluator(cfg.Evaluator, log)

		// Create fallback engine
		engine := ocr.NewFallbackEngine(providers, cfg.Fallback, evaluator, log)

		// PDF renderer (may be nil when disabled)
		renderer := makeRenderer(cfg)

		// Find files (images and PDFs)
		files, err := findImages(dir, recursive)
		if err != nil {
			return fmt.Errorf("failed to find files: %w", err)
		}

		if len(files) == 0 {
			fmt.Println("No files found")
			return nil
		}

		// Set output directory
		if outputDir == "" {
			outputDir = filepath.Join(dir, "ocr_results")
		}
		os.MkdirAll(outputDir, 0755)

		// Split into image inputs (eligible for skip-existing parallelism)
		// and PDF inputs (processed inline via sliding window).
		var imageFiles []string
		var pdfFiles []string
		for _, p := range files {
			if ocr.IsPDF(p) {
				pdfFiles = append(pdfFiles, p)
			} else {
				imageFiles = append(imageFiles, p)
			}
		}

		// Filter existing results for images only
		if skipExisting {
			var filteredImg []string
			for _, img := range imageFiles {
				base := filepath.Base(img)
				ext := filepath.Ext(base)
				name := base[:len(base)-len(ext)]
				txtPath := filepath.Join(outputDir, name+".txt")
				if _, err := os.Stat(txtPath); os.IsNotExist(err) {
					filteredImg = append(filteredImg, img)
				}
			}
			imageFiles = filteredImg
		}

		total := len(imageFiles) + len(pdfFiles)
		if total == 0 {
			fmt.Println("No files to process")
			return nil
		}

		fmt.Printf("Found %d files to process (%d images, %d PDFs)\n", total, len(imageFiles), len(pdfFiles))

		// Create parent context with cancellation for graceful shutdown
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()

		var success, failed int

		// 1) Process standalone images concurrently (unchanged behaviour).
		if len(imageFiles) > 0 {
			var wg sync.WaitGroup
			sem := make(chan struct{}, workers)
			results := make(chan *processResult, len(imageFiles))

			for i, img := range imageFiles {
				wg.Add(1)
				go func(idx int, imgPath string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()

					result := processImage(ctx, engine, provider, imgPath, outputDir, idx+1, len(imageFiles), saveJSON)
					results <- result
				}(i, img)
			}

			go func() {
				wg.Wait()
				close(results)
			}()

			for result := range results {
				if result.err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "✗ %s: %s\n", filepath.Base(result.file), result.err)
				} else {
					success++
					fmt.Printf("✓ %s -> %s\n", filepath.Base(result.file), result.output)
				}
			}
		}

		// 2) Process each PDF via sliding window, reusing processImageNamed
		// for every page so disk-writing/scoring logic is shared with images.
		// workers intentionally does NOT apply here: rendering a window is cheap
		// and pages are OCR'd serially to keep the renderer's temp directory
		// bounded to pdf.window_size pages at a time.
		windowFlag, _ := cmd.Flags().GetInt("window")
		for _, pdf := range pdfFiles {
			if !cfg.PDF.Enabled || renderer == nil {
				fmt.Fprintf(os.Stderr, "✗ %s: PDF support disabled in config\n", filepath.Base(pdf))
				failed++
				continue
			}

			pdfBase := filepath.Base(pdf)
			if ext := filepath.Ext(pdfBase); ext != "" {
				pdfBase = pdfBase[:len(pdfBase)-len(ext)]
			}

			totalPages, _ := renderer.CountPages(pdf)
			window := windowFlag
			if window <= 0 {
				window = cfg.PDF.WindowSize
			}
			if window < 1 {
				window = 1
			}
			fmt.Fprintf(os.Stderr, "[pdf] %s: %d pages, window=%d\n", pdfBase, totalPages, window)

			pageDone := 0
			handlePage := func(task FileTask) error {
				outName := fmt.Sprintf("%s-%04d", pdfBase, task.PageNum)
				res := processImageNamed(ctx, engine, provider, task.ImagePath,
					outputDir, pageDone+1, totalPages, saveJSON, outName)
				pageDone++
				if res.err != nil {
					fmt.Fprintf(os.Stderr, "✗ %s page %d: %s\n", pdfBase, task.PageNum, res.err)
					return nil // keep going like batch does
				}
				fmt.Fprintf(os.Stderr, "✓ %s page %d -> %s\n", pdfBase, task.PageNum, res.output)
				return nil
			}

			perr := processPDFWithWindow(renderer, pdf, 0, 0, window, totalPages, handlePage)
			if perr != nil {
				fmt.Fprintf(os.Stderr, "✗ %s: %s\n", filepath.Base(pdf), perr)
				failed++
				continue
			}
			success++
		}

		// Print summary
		fmt.Printf("\nCompleted: %d success, %d failed, %d total\n", success, failed, total)

		return nil
	},
}

func init() {
	batchCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	batchCmd.Flags().StringP("provider", "p", "", "OCR provider to use")
	batchCmd.Flags().StringP("output", "o", "", "Output directory")
	batchCmd.Flags().IntP("workers", "w", 1, "Number of concurrent workers (images only; PDF pages run serially)")
	batchCmd.Flags().BoolP("recursive", "r", false, "Search recursively")
	batchCmd.Flags().BoolP("skip-existing", "s", false, "Skip existing results")
	batchCmd.Flags().Bool("save-json", false, "Also save JSON output")
	batchCmd.Flags().Int("window", 0, "PDF sliding-window size (overrides pdf.window_size; default 20)")
}

type processResult struct {
	file   string
	output string
	err    error
}

func processImage(ctx context.Context, engine *ocr.FallbackEngine, provider, imagePath, outputDir string, current, total int, saveJSON bool) *processResult {
	return processImageNamed(ctx, engine, provider, imagePath, outputDir, current, total, saveJSON, "")
}

// processImageNamed is processImage with an optional output-name override used
// for PDF pages, whose rendered image path is a temp PNG. When nameOverride is
// non-empty it is used as the output basename (without extension); otherwise
// the basename of imagePath is used. Reusing this for both images and PDF
// pages keeps a single source of truth for how results are written to disk.
func processImageNamed(ctx context.Context, engine *ocr.FallbackEngine, provider, imagePath, outputDir string, current, total int, saveJSON bool, nameOverride string) *processResult {
	// Build request
	req := &ocr.OCRRequest{
		ImagePath: imagePath,
	}

	// Create child context with timeout from parent context
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var result *ocr.OCRResult
	var err error

	// Use specific provider or fallback engine
	if provider != "" {
		providers := engine.GetProviders()
		if p, ok := providers[provider]; ok {
			result, err = p.Recognize(ctx, req)
		} else {
			return &processResult{
				file: imagePath,
				err:  fmt.Errorf("provider not found: %s", provider),
			}
		}
	} else {
		result, err = engine.Recognize(ctx, req)
	}

	if err != nil {
		return &processResult{
			file: imagePath,
			err:  err,
		}
	}

	// Resolve output basename.
	name := nameOverride
	if name == "" {
		base := filepath.Base(imagePath)
		ext := filepath.Ext(base)
		name = base[:len(base)-len(ext)]
	}
	txtPath := filepath.Join(outputDir, name+".txt")

	text := result.Text
	if result.Evaluation != nil {
		text += fmt.Sprintf("\n\n[Score: %.2f]", result.Evaluation.Score)
	}

	if err := os.WriteFile(txtPath, []byte(text), 0644); err != nil {
		return &processResult{
			file: imagePath,
			err:  fmt.Errorf("failed to save text result: %w", err),
		}
	}

	// Save JSON result if requested
	if saveJSON {
		jsonPath := filepath.Join(outputDir, name+".json")
		jsonData, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return &processResult{
				file: imagePath,
				err:  fmt.Errorf("failed to marshal JSON: %w", err),
			}
		}
		if err := os.WriteFile(jsonPath, jsonData, 0644); err != nil {
			return &processResult{
				file: imagePath,
				err:  fmt.Errorf("failed to save JSON result: %w", err),
			}
		}
	}

	return &processResult{
		file:   imagePath,
		output: txtPath,
	}
}

func findImages(dir string, recursive bool) ([]string, error) {
	var images []string
	extensions := map[string]bool{
		".png":  true,
		".jpg":  true,
		".jpeg": true,
		".webp": true,
		".bmp":  true,
		".pdf":  true,
	}

	if recursive {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				ext := strings.ToLower(filepath.Ext(path))
				if extensions[ext] {
					images = append(images, path)
				}
			}
			return nil
		})
		return images, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if extensions[ext] {
				images = append(images, filepath.Join(dir, entry.Name()))
			}
		}
	}

	return images, nil
}

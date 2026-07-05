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
	Long:  "Perform OCR recognition on all images and PDF files in a directory. PDFs are rasterized page-by-page.",
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

		// Find images
		images, err := findImages(dir, recursive)
		if err != nil {
			return fmt.Errorf("failed to find images: %w", err)
		}

		if len(images) == 0 {
			fmt.Println("No images found")
			return nil
		}

		// Set output directory
		if outputDir == "" {
			outputDir = filepath.Join(dir, "ocr_results")
		}
		os.MkdirAll(outputDir, 0755)

		// Filter existing results
		if skipExisting {
			var filtered []string
			for _, img := range images {
				base := filepath.Base(img)
				ext := filepath.Ext(base)
				name := base[:len(base)-len(ext)]
				txtPath := filepath.Join(outputDir, name+".txt")
				if _, err := os.Stat(txtPath); os.IsNotExist(err) {
					filtered = append(filtered, img)
				}
			}
			images = filtered
		}

		if len(images) == 0 {
			fmt.Println("No images to process")
			return nil
		}

		fmt.Printf("Found %d images to process\n", len(images))

		// Create parent context with cancellation for graceful shutdown
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()

		// Process images
		var wg sync.WaitGroup
		sem := make(chan struct{}, workers)
		results := make(chan *processResult, len(images))

		for i, img := range images {
			wg.Add(1)
			go func(idx int, imgPath string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				result := processImage(ctx, engine, provider, imgPath, outputDir, idx+1, len(images), saveJSON)
				results <- result
			}(i, img)
		}

		// Wait for all goroutines
		go func() {
			wg.Wait()
			close(results)
		}()

		// Collect results
		var success, failed int
		var failedFiles []string

		for result := range results {
			if result.err != nil {
				failed++
				failedFiles = append(failedFiles, fmt.Sprintf("%s: %s", result.file, result.err))
				fmt.Fprintf(os.Stderr, "✗ %s: %s\n", filepath.Base(result.file), result.err)
			} else {
				success++
				fmt.Printf("✓ %s -> %s\n", filepath.Base(result.file), result.output)
			}
		}

		// Print summary
		fmt.Printf("\nCompleted: %d success, %d failed, %d total\n", success, failed, len(images))

		if len(failedFiles) > 0 {
			fmt.Println("\nFailed files:")
			for _, f := range failedFiles {
				fmt.Printf("  - %s\n", f)
			}
		}

		return nil
	},
}

func init() {
	batchCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	batchCmd.Flags().StringP("provider", "p", "", "OCR provider to use")
	batchCmd.Flags().StringP("output", "o", "", "Output directory")
	batchCmd.Flags().IntP("workers", "w", 1, "Number of concurrent workers")
	batchCmd.Flags().BoolP("recursive", "r", false, "Search recursively")
	batchCmd.Flags().BoolP("skip-existing", "s", false, "Skip existing results")
	batchCmd.Flags().Bool("save-json", false, "Also save JSON output")
}

type processResult struct {
	file   string
	output string
	err    error
}

func processImage(ctx context.Context, engine *ocr.FallbackEngine, provider, imagePath, outputDir string, current, total int, saveJSON bool) *processResult {
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

	// Save text result
	base := filepath.Base(imagePath)
	ext := filepath.Ext(base)
	name := base[:len(base)-len(ext)]
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

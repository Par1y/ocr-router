package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"ocr-router/internal/config"
	"ocr-router/internal/logger"
	"ocr-router/internal/ocr"
)

var recognizeCmd = &cobra.Command{
	Use:   "recognize [file path]",
	Short: "Recognize text in an image or PDF",
	Long:  "Perform OCR recognition on a single image or PDF file. PDFs are rasterized in a sliding window (pdf.window_size), and text is concatenated with one progress line per page (like batch).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		imagePath := args[0]

		// Get flags
		configFile, _ := cmd.Flags().GetString("config")
		provider, _ := cmd.Flags().GetString("provider")
		prompt, _ := cmd.Flags().GetString("prompt")
		outputFormat, _ := cmd.Flags().GetString("format")
		first, _ := cmd.Flags().GetInt("first")
		last, _ := cmd.Flags().GetInt("last")
		window, _ := cmd.Flags().GetInt("window")

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

		// Create providers and engine
		providers := createProviders(cfg, log)
		evaluator := ocr.NewEvaluator(cfg.Evaluator, log)
		engine := ocr.NewFallbackEngine(providers, cfg.Fallback, evaluator, log)

		// Image (non-PDF): unchanged single-shot path.
		if !ocr.IsPDF(imagePath) {
			req := &ocr.OCRRequest{ImagePath: imagePath, Prompt: prompt}
			if provider != "" {
				if p, ok := providers[provider]; ok {
					result, err := p.Recognize(context.Background(), req)
					if err != nil {
						return fmt.Errorf("OCR failed: %w", err)
					}
					return outputResult(result, outputFormat)
				}
				return fmt.Errorf("provider not found: %s", provider)
			}
			result, err := engine.Recognize(context.Background(), req)
			if err != nil {
				return fmt.Errorf("OCR failed: %w", err)
			}
			return outputResult(result, outputFormat)
		}

		// PDF: sliding-window rasterize + per-page OCR, streaming progress.
		if !cfg.PDF.Enabled {
			return fmt.Errorf("pdf support is disabled in config (set pdf.enabled: true)")
		}
		renderer := ocr.NewPDFRenderer(cfg.PDF)
		win := window
		if win <= 0 {
			win = cfg.PDF.WindowSize
		}
		if win < 1 {
			win = 1
		}

		totalPages, _ := renderer.CountPages(imagePath)
		if totalPages == 0 {
			// Proceed best-effort; window loop self-terminates on the tail.
			fmt.Fprintln(os.Stderr, "[pdf] page count unknown; rendering from requested range")
		}
		if first < 0 {
			first = 0
		}
		if last < 0 {
			last = 0
		}
		hi := last
		if hi == 0 {
			hi = totalPages
		}
		fmt.Fprintf(os.Stderr, "[pdf] %s: %d pages requested, window=%d\n", baseName(imagePath), hi-first+1, win)

		req := &ocr.OCRRequest{Prompt: prompt}
		var combined strings.Builder
		pageDone := 0
		pageSuccess := 0
		var usedProvider string       // provider that actually ran the first page
		var evalSum float64           // summed evaluation scores across pages
		var evalCount int             // number of pages that had an evaluation
		var allAttempts []ocr.AttemptRecord

		err = processPDFWithWindow(renderer, imagePath, first, last, win, totalPages, func(task FileTask) error {
			req.ImagePath = task.ImagePath
			var r *ocr.OCRResult
			var e error
			if provider != "" {
				if p, ok := providers[provider]; ok {
					r, e = p.Recognize(context.Background(), req)
				} else {
					return fmt.Errorf("provider not found: %s", provider)
				}
			} else {
				r, e = engine.Recognize(context.Background(), req)
			}
			pageDone++
			if e != nil {
				fmt.Fprintf(os.Stderr, "✗ %s page %d: %s\n", baseName(imagePath), task.PageNum, e)
				return nil // continue, like batch
			}
			pageSuccess++
			if combined.Len() > 0 {
				combined.WriteString("\n\n")
			}
			combined.WriteString(r.Text)
			if usedProvider == "" {
				usedProvider = r.Provider
			}
			if r.Evaluation != nil {
				evalSum += r.Evaluation.Score
				evalCount++
			}
			if len(r.Attempts) > 0 {
				allAttempts = append(allAttempts, r.Attempts...)
			}
			fmt.Fprintf(os.Stderr, "✓ %s page %d (%d chars)\n", baseName(imagePath), task.PageNum, len(r.Text))
			return nil
		})
		if err != nil {
			return err
		}

		text := combined.String()
		if text == "" {
			return fmt.Errorf("no text recognized")
		}
		r := &ocr.OCRResult{
			Provider:  usedProvider,
			Text:      text,
			Attempts:  allAttempts,
			Timestamp: time.Now(),
			Metadata: map[string]interface{}{
				"is_pdf":         true,
				"pages":          pageSuccess,
				"pages_total":    pageDone,
				"pages_failed":   pageDone - pageSuccess,
				"source_file":    imagePath,
				"window_first":   first,
				"window_last":    last,
				"window_size":    win,
			},
		}
		if evalCount > 0 {
		avgScore := evalSum / float64(evalCount)
		r.Evaluation = &ocr.EvaluationResult{
			Score:  avgScore,
			Reason: fmt.Sprintf("average across %d pages", evalCount),
			Pass:   avgScore >= cfg.Evaluator.Threshold,
		}
		}
		return outputResult(r, outputFormat)
	},
}

func baseName(p string) string {
	n := p
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == filepath.Separator {
			n = p[i+1:]
			break
		}
	}
	if ext := filepath.Ext(n); ext != "" {
		n = n[:len(n)-len(ext)]
	}
	return n
}

func init() {
	recognizeCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	recognizeCmd.Flags().StringP("provider", "p", "", "OCR provider to use")
	recognizeCmd.Flags().StringP("prompt", "", "", "Custom prompt for OCR")
	recognizeCmd.Flags().StringP("format", "f", "text", "Output format (text, json)")
	recognizeCmd.Flags().Int("first", 0, "PDF first page to process (1-based; 0=from start)")
	recognizeCmd.Flags().Int("last", 0, "PDF last page to process (1-based; 0=to end)")
	recognizeCmd.Flags().Int("window", 0, "PDF window size: pages rasterized at a time (overrides pdf.window_size; default 20)")
}

func outputResult(result *ocr.OCRResult, format string) error {
	switch format {
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	case "text":
		fmt.Println(result.Text)

		// Show attempts if any
		if len(result.Attempts) > 0 {
			fmt.Fprintf(os.Stderr, "\n--- 尝试记录 ---\n")
			for _, attempt := range result.Attempts {
				status := "✓"
				if !attempt.Passed {
					status = "✗"
				}
				scoreStr := "-"
				if attempt.Score > 0 {
					scoreStr = fmt.Sprintf("%.2f", attempt.Score)
				}
				errStr := ""
				if attempt.Error != "" {
					errStr = fmt.Sprintf(" (错误: %s)", attempt.Error)
				}
				fmt.Fprintf(os.Stderr, "  %s %s: %s%s\n", status, attempt.Provider, scoreStr, errStr)
			}
		}

		// Show evaluation
		if result.Evaluation != nil {
			fmt.Fprintf(os.Stderr, "\n[最终评分: %.2f]\n", result.Evaluation.Score)
		}

		// Show quality warning
		if result.QualityWarning {
			fmt.Fprintf(os.Stderr, "[警告: 所有Provider都未能达到质量阈值]\n")
			if result.BestScore > 0 {
				fmt.Fprintf(os.Stderr, "[最佳评分: %.2f]\n", result.BestScore)
			}
		}

		return nil
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}

func createProviders(cfg *config.Config, log *logger.Logger) map[string]ocr.Provider {
	// Build the optional PDF renderer. We pass the same instance to every
	// provider so tool detection runs only once. The renderer is stateless.
	var pdfRenderer *ocr.PDFRenderer
	if cfg.PDF.Enabled {
		pdfRenderer = ocr.NewPDFRenderer(cfg.PDF)
	}

	providers := make(map[string]ocr.Provider)

	// NVIDIA provider
	if cfg.Providers.NVIDIA.Enabled {
		providers["nvidia"] = ocr.NewNVIDIAProvider(cfg.Providers.NVIDIA, log, pdfRenderer)
	}

	// LLM Vision provider
	if cfg.Providers.LLMVision.Enabled {
		providers["llm_vision"] = ocr.NewLLMVisionProvider(cfg.Providers.LLMVision, log, pdfRenderer)
	}

	// Browser SSE provider
	if cfg.Providers.BrowserSSE.Enabled {
		providers["browser_sse"] = ocr.NewBrowserSSEProvider(cfg.Providers.BrowserSSE, log, pdfRenderer)
	}

	return providers
}

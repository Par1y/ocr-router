package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"ocr-router/internal/config"
	"ocr-router/internal/logger"
	"ocr-router/internal/ocr"
)

var recognizeCmd = &cobra.Command{
	Use:   "recognize [file path]",
	Short: "Recognize text in an image or PDF",
	Long:  "Perform OCR recognition on a single image or PDF file. PDFs are rasterized page-by-page and the text is concatenated.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		imagePath := args[0]

		// Get flags
		configFile, _ := cmd.Flags().GetString("config")
		provider, _ := cmd.Flags().GetString("provider")
		prompt, _ := cmd.Flags().GetString("prompt")
		outputFormat, _ := cmd.Flags().GetString("format")

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

		// Build request
		req := &ocr.OCRRequest{
			ImagePath: imagePath,
			Prompt:    prompt,
		}

		// If provider specified, use only that provider
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

		// Use fallback engine
		result, err := engine.Recognize(context.Background(), req)
		if err != nil {
			return fmt.Errorf("OCR failed: %w", err)
		}

		return outputResult(result, outputFormat)
	},
}

func init() {
	recognizeCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	recognizeCmd.Flags().StringP("provider", "p", "", "OCR provider to use")
	recognizeCmd.Flags().StringP("prompt", "", "", "Custom prompt for OCR")
	recognizeCmd.Flags().StringP("format", "f", "text", "Output format (text, json)")
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

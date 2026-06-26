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

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check system health",
	Long:  "Check the health of the OCR system and providers.",
	RunE: func(cmd *cobra.Command, args []string) error {
		configFile, _ := cmd.Flags().GetString("config")
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

		// Check health
		statuses := engine.GetProviders()
		providerStatuses := make(map[string]bool)
		for name, provider := range statuses {
			err := provider.HealthCheck(context.Background())
			providerStatuses[name] = err == nil
		}

		// Build health response
		health := map[string]interface{}{
			"status":     "ok",
			"providers":  providerStatuses,
		}

		// Output
		switch outputFormat {
		case "json":
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(health)
		case "text":
			fmt.Println("System Health:")
			fmt.Println()
			fmt.Printf("  Status: %s\n", health["status"])
			fmt.Println()
			fmt.Println("  Providers:")
			for name, healthy := range providerStatuses {
				status := "✓ Healthy"
				if !healthy {
					status = "✗ Unhealthy"
				}
				fmt.Printf("    %s: %s\n", name, status)
			}
			return nil
		default:
			return fmt.Errorf("unknown format: %s", outputFormat)
		}
	},
}

func init() {
	healthCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	healthCmd.Flags().StringP("format", "f", "text", "Output format (text, json)")
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"ocr-router/internal/config"
	"ocr-router/internal/logger"
	"ocr-router/internal/ocr"
)

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "List OCR providers",
	Long:  "List all available OCR providers and their health status.",
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

		// Check health
		statuses := make(map[string]ocr.ProviderStatus)
		for name, provider := range providers {
			start := time.Now()
			err := provider.HealthCheck(context.Background())
			latency := time.Since(start)

			status := ocr.ProviderStatus{
				Name:      name,
				Type:      provider.Type(),
				Healthy:   err == nil,
				Latency:   latency,
				CheckedAt: time.Now(),
			}
			if err != nil {
				status.Error = err.Error()
			}
			statuses[name] = status
		}

		// Output
		switch outputFormat {
		case "json":
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(statuses)
		case "text":
			fmt.Println("OCR Providers:")
			fmt.Println()
			for name, status := range statuses {
				healthStatus := "✓ Healthy"
				if !status.Healthy {
					healthStatus = "✗ Unhealthy"
				}
				fmt.Printf("  %s (%s)\n", name, status.Type)
				fmt.Printf("    Status: %s\n", healthStatus)
				fmt.Printf("    Latency: %s\n", status.Latency)
				if status.Error != "" {
					fmt.Printf("    Error: %s\n", status.Error)
				}
				fmt.Println()
			}
			return nil
		default:
			return fmt.Errorf("unknown format: %s", outputFormat)
		}
	},
}

func init() {
	providersCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	providersCmd.Flags().StringP("format", "f", "text", "Output format (text, json)")
}

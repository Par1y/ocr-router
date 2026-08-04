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
)

// checkConfigCmd validates a generated config the way the translator workbench
// needs before an extract run: it loads the config, builds the providers, and
// health-checks each one, emitting a machine-readable JSON verdict. It returns
// exit 20 when the config is fatally unusable (parse error, no providers
// enabled, or every provider unhealthy) and 0 otherwise, matching the extract
// exit-code contract for "fatal at startup".
var checkConfigCmd = &cobra.Command{
	Use:           "check-config",
	Short:         "Validate a config and provider connectivity (JSON verdict)",
	Long:          "Load a config, build its providers, and health-check them. Prints a JSON result. Exit 20 when the config is unusable (parse error / no providers / all unhealthy), else 0.",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		configFile, _ := cmd.Flags().GetString("config")

		emit := func(v interface{}) {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(v)
		}

		cfg, err := config.Load(configFile)
		if err != nil {
			emit(map[string]interface{}{"ok": false, "error": fmt.Sprintf("failed to load config: %v", err)})
			return exitError{20}
		}

		log, err := logger.New(cfg.Logging.Level, cfg.Logging.Format, "")
		if err != nil {
			emit(map[string]interface{}{"ok": false, "error": fmt.Sprintf("failed to create logger: %v", err)})
			return exitError{20}
		}
		defer log.Close()

		providers := createProviders(cfg, log)
		if len(providers) == 0 {
			emit(map[string]interface{}{"ok": false, "error": "no OCR providers enabled"})
			return exitError{20}
		}

		type provResult struct {
			Type      string `json:"type"`
			Healthy   bool   `json:"healthy"`
			Error     string `json:"error,omitempty"`
			LatencyMs int64  `json:"latency_ms"`
		}
		results := make(map[string]provResult, len(providers))
		anyHealthy := false
		for name, p := range providers {
			start := time.Now()
			herr := p.HealthCheck(context.Background())
			res := provResult{
				Type:      string(p.Type()),
				Healthy:   herr == nil,
				LatencyMs: time.Since(start).Milliseconds(),
			}
			if herr != nil {
				res.Error = herr.Error()
			} else {
				anyHealthy = true
			}
			results[name] = res
		}

		verdict := map[string]interface{}{
			"ok":                anyHealthy,
			"pdf_enabled":       cfg.PDF.Enabled,
			"evaluator_enabled": cfg.Evaluator.Enabled,
			"providers":         results,
		}
		if !anyHealthy {
			verdict["error"] = "all providers unhealthy"
			emit(verdict)
			return exitError{20}
		}
		emit(verdict)
		return nil
	},
}

func init() {
	checkConfigCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
}

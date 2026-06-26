package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"ocr-router/internal/config"
	"ocr-router/internal/handler"
	"ocr-router/internal/logger"
	"ocr-router/internal/ocr"
	"ocr-router/internal/storage"
	"ocr-router/internal/task"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start HTTP server",
	Long:  "Start the OCR HTTP API server.",
	RunE: func(cmd *cobra.Command, args []string) error {
		configFile, _ := cmd.Flags().GetString("config")
		port, _ := cmd.Flags().GetInt("port")

		// Load config
		cfg, err := config.Load(configFile)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Override port if specified
		if port > 0 {
			cfg.Server.Port = port
		}

		// Create logger
		log, err := logger.New(cfg.Logging.Level, cfg.Logging.Format, cfg.Logging.File)
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

		// Create task manager
		taskMgr := task.NewTaskManager(engine, cfg.Task.Workers, cfg.Task.QueueSize, cfg.Task.TaskTimeout)
		taskMgr.Start()
		defer taskMgr.Stop()

		// Create storage
		fileStorage, err := storage.NewFileStorage(cfg.Storage.BaseDir, cfg.Storage.Format)
		if err != nil {
			return fmt.Errorf("failed to create storage: %w", err)
		}

		// Create API handler
		apiHandler := handler.NewAPIHandler(engine, taskMgr, fileStorage, log)

		// Create WebUI handler
		webuiHandler, err := handler.NewWebUIHandler(engine, taskMgr, log)
		if err != nil {
			return fmt.Errorf("failed to create webui handler: %w", err)
		}

		// Create router
		mux := http.NewServeMux()
		apiHandler.RegisterRoutes(mux)
		webuiHandler.RegisterRoutes(mux)

		// Create server
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		server := &http.Server{
			Addr:    addr,
			Handler: mux,
		}

		// Start server
		go func() {
			log.Info(fmt.Sprintf("Starting server on %s", addr))
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error(fmt.Sprintf("Server error: %s", err.Error()))
				os.Exit(1)
			}
		}()

		// Wait for interrupt
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit

		log.Info("Shutting down server...")

		// Graceful shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Error(fmt.Sprintf("Server shutdown error: %s", err.Error()))
		}

		log.Info("Server stopped")
		return nil
	},
}

func init() {
	serveCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	serveCmd.Flags().IntP("port", "p", 0, "Server port (overrides config)")
}

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"ocr-router/internal/config"
	"ocr-router/internal/logger"
	"ocr-router/internal/ocr"
	"ocr-router/internal/task"
	"time"
)

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Manage OCR tasks",
	Long:  "Manage async OCR tasks.",
}

var taskListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks",
	Long:  "List all tasks or filter by status.",
	RunE: func(cmd *cobra.Command, args []string) error {
		configFile, _ := cmd.Flags().GetString("config")
		status, _ := cmd.Flags().GetString("status")
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

		// Create task manager
		taskMgr := task.NewTaskManager(engine, cfg.Task.Workers, cfg.Task.QueueSize, cfg.Task.TaskTimeout)

		// List tasks
		var taskStatus task.TaskStatus
		if status != "" {
			taskStatus = task.TaskStatus(status)
		}
		tasks := taskMgr.ListTasks(taskStatus)

		// Output
		switch outputFormat {
		case "json":
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(tasks)
		case "text":
			if len(tasks) == 0 {
				fmt.Println("No tasks found")
				return nil
			}
			fmt.Printf("Found %d tasks:\n\n", len(tasks))
			for _, t := range tasks {
				fmt.Printf("ID: %s\n", t.ID)
				fmt.Printf("Status: %s\n", t.Status)
				fmt.Printf("Created: %s\n", t.CreatedAt.Format(time.RFC3339))
				if t.Provider != "" {
					fmt.Printf("Provider: %s\n", t.Provider)
				}
				fmt.Println("---")
			}
			return nil
		default:
			return fmt.Errorf("unknown format: %s", outputFormat)
		}
	},
}

var taskStatusCmd = &cobra.Command{
	Use:   "status [task-id]",
	Short: "Get task status",
	Long:  "Get the status of a specific task.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]
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

		// Create task manager
		taskMgr := task.NewTaskManager(engine, cfg.Task.Workers, cfg.Task.QueueSize, cfg.Task.TaskTimeout)

		// Get task
		t, ok := taskMgr.GetTask(taskID)
		if !ok {
			return fmt.Errorf("task not found: %s", taskID)
		}

		// Output
		switch outputFormat {
		case "json":
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(t)
		case "text":
			fmt.Printf("ID: %s\n", t.ID)
			fmt.Printf("Status: %s\n", t.Status)
			fmt.Printf("Created: %s\n", t.CreatedAt.Format(time.RFC3339))
			if t.StartedAt != nil {
				fmt.Printf("Started: %s\n", t.StartedAt.Format(time.RFC3339))
			}
			if t.EndedAt != nil {
				fmt.Printf("Ended: %s\n", t.EndedAt.Format(time.RFC3339))
			}
			if t.Provider != "" {
				fmt.Printf("Provider: %s\n", t.Provider)
			}
			if t.Error != "" {
				fmt.Printf("Error: %s\n", t.Error)
			}
			if t.Result != nil {
				fmt.Printf("\nResult:\n%s\n", t.Result.Text)
			}
			return nil
		default:
			return fmt.Errorf("unknown format: %s", outputFormat)
		}
	},
}

func init() {
	taskCmd.AddCommand(taskListCmd)
	taskCmd.AddCommand(taskStatusCmd)

	taskListCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	taskListCmd.Flags().StringP("status", "s", "", "Filter by status (pending, running, completed, failed)")
	taskListCmd.Flags().StringP("format", "f", "text", "Output format (text, json)")

	taskStatusCmd.Flags().StringP("config", "c", "config.yaml", "Config file path")
	taskStatusCmd.Flags().StringP("format", "f", "text", "Output format (text, json)")
}

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ocr-cli",
	Short: "OCR CLI tool with multiple providers",
	Long:  "A CLI tool for OCR recognition with multiple providers, quality evaluation, and fallback support.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// Add commands
	rootCmd.AddCommand(recognizeCmd)
	rootCmd.AddCommand(batchCmd)
	rootCmd.AddCommand(taskCmd)
	rootCmd.AddCommand(providersCmd)
	rootCmd.AddCommand(healthCmd)
	rootCmd.AddCommand(serveCmd)
}

func main() {
	Execute()
}

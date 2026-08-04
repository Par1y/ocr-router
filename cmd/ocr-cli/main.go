package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ocr-cli",
	Short: "OCR CLI tool with multiple providers",
	Long:  "A CLI tool for OCR recognition with multiple providers, quality evaluation, and fallback support.",
}

// Execute runs the root command. Commands may return an exitError to request a
// specific process exit code (the extract command uses 0/10/20/130); any other
// error is printed and mapped to exit 1.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// Add commands
	rootCmd.AddCommand(recognizeCmd)
	rootCmd.AddCommand(batchCmd)
	rootCmd.AddCommand(extractCmd)
	rootCmd.AddCommand(checkConfigCmd)
	rootCmd.AddCommand(taskCmd)
	rootCmd.AddCommand(providersCmd)
	rootCmd.AddCommand(healthCmd)
	rootCmd.AddCommand(serveCmd)
}

func main() {
	Execute()
}

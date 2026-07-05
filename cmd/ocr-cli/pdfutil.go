package main

import (
	"ocr-router/internal/config"
	"ocr-router/internal/ocr"
)

// FileTask describes one page of OCR work produced by the sliding-window
// PDF pipeline (cmd/ocr-cli pdfwindow.go). It is shared between recognize
// and batch so both commands describe a single page identically.
type FileTask struct {
	ImagePath  string // absolute path to the (rendered) image file
	OutName    string // output filename without extension, e.g. "book-0003"
	PageSize   int    // total pages in the source PDF; 0 for plain images
	PageNum    int    // 1-based page number for PDFs; 0 for plain images
	SourceFile string // the original input path (PDF file for PDF inputs, image otherwise)
	CleanupDir string // temp dir to remove after this file is done; "" for images
}

// makeRenderer builds the shared PDF renderer from config, or nil when PDF
// support is disabled. The same instance can be reused across all callers
// since the renderer is stateless.
func makeRenderer(cfg *config.Config) *ocr.PDFRenderer {
	if !cfg.PDF.Enabled {
		return nil
	}
	return ocr.NewPDFRenderer(cfg.PDF)
}

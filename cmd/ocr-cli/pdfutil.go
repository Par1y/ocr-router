package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ocr-router/internal/config"
	"ocr-router/internal/ocr"
)

// FileTask is a single unit of OCR work: one image to recognize and one
// output filename to write the result to. A multi-page PDF expands into one
// FileTask per page, which lets the batch pipeline treat every input the same
// way and gives the user per-page progress exactly like a directory of images.
type FileTask struct {
	ImagePath  string // absolute path to the (rendered) image file
	OutName    string // output filename without extension, e.g. "book-0003"
	PageSize   int    // for PDFs: number of pages in the source PDF; 0 for images
	PageNum    int    // for PDFs: 1-based page number; 0 for plain images
	SourceFile string // the original input path (PDF file for PDF inputs)
	CleanupDir string // temp dir to remove after this file is done; "" for images
}

// shouldProcess reports whether a file extension is one we handle.
func shouldProcess(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".webp", ".bmp", ".pdf":
		return true
	}
	return false
}

// scanFiles walks a directory and returns the list of source files (images
// and PDFs) to expand later. Non-recursive by default.
func scanFiles(dir string, recursive bool) ([]string, error) {
	var sources []string

	if recursive {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && shouldProcess(filepath.Ext(path)) {
				sources = append(sources, path)
			}
			return nil
		})
		return sources, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && shouldProcess(filepath.Ext(e.Name())) {
			sources = append(sources, filepath.Join(dir, e.Name()))
		}
	}
	return sources, nil
}

// expandFile turns a single source file into one or more FileTasks. Images
// yield a single task; PDFs are rasterized once and yield one task per page.
//
// The returned temp dirs (in CleanupDir fields) must be removed by the caller
// once the corresponding task has been processed.
//
// For PDFs, prefer expandFileWindow when dealing with large documents: it
// rasterizes only a sliding window of pages rather than the whole file.
func expandFile(path string, renderer *ocr.PDFRenderer) ([]FileTask, error) {
	if !ocr.IsPDF(path) {
		base := filepath.Base(path)
		name := base[:len(base)-len(filepath.Ext(base))]
		return []FileTask{{
			ImagePath: path,
			OutName:   name,
		}}, nil
	}

	if renderer == nil {
		return nil, fmt.Errorf("pdf support is disabled in config (pdf.enabled=false), cannot process %q", path)
	}

	pages, tmp, err := renderer.Render(path, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("render %q: %w", path, err)
	}

	base := filepath.Base(path)
	base = base[:len(base)-len(filepath.Ext(base))]

	tasks := make([]FileTask, 0, len(pages))
	for _, pg := range pages {
		tasks = append(tasks, FileTask{
			ImagePath:  pg.Path,
			OutName:    fmt.Sprintf("%s-%04d", base, pg.Page),
			PageSize:   len(pages),
			PageNum:    pg.Page,
			SourceFile: path,
			CleanupDir: "", // temp dir removed only after all pages processed
		})
	}
	if len(tasks) > 0 {
		tasks[len(tasks)-1].CleanupDir = tmp
	} else {
		ocr.Cleanup(tmp)
	}
	return tasks, nil
}

// expandFiles expands each source in turn, collecting every FileTask. Every
// per-source failure is returned inlined as a failed task so that batch
// processing keeps going for the rest of the inputs (matching the existing
// batch behavior of reporting ✗ per file and continuing).
func expandFiles(sources []string, renderer *ocr.PDFRenderer) []FileTask {
	var tasks []FileTask
	for _, src := range sources {
		ts, err := expandFile(src, renderer)
		if err != nil {
			// Record as a synthetic failed task so the summary still counts it.
			base := filepath.Base(src)
			name := base
			if ext := filepath.Ext(base); ext != "" {
				name = base[:len(base)-len(ext)]
			}
			tasks = append(tasks, FileTask{
				OutName:    name,
				SourceFile: src,
				// ImagePath empty + a load error marker handled by processImage.
				PageSize:   0,
			})
			fmt.Fprintf(os.Stderr, "✗ %s: %s\n", base, err)
			continue
		}
		tasks = append(tasks, ts...)
	}
	return tasks
}

// makeRenderer builds the shared PDF renderer from config, or nil when PDF
// support is disabled. The same instance can be reused across all expanders
// since the renderer is stateless.
func makeRenderer(cfg *config.Config) *ocr.PDFRenderer {
	if !cfg.PDF.Enabled {
		return nil
	}
	return ocr.NewPDFRenderer(cfg.PDF)
}

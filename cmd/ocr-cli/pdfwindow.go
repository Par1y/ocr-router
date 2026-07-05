package main

import (
	"fmt"
	"os"
	"path/filepath"

	"ocr-router/internal/ocr"
)

// PageProcessFn is invoked once per image page within a sliding window.
// It receives the rendered image path, the source filename, the page number,
// and the total page count of the PDF. Any returned error aborts the run.
type PageProcessFn func(task FileTask) error

// processPDFWithWindow rasterizes and OCRs a PDF in a sliding window so the
// disk never holds more than `window` rendered pages at a time while still
// covering the full [first,last] page range (1-based, 0 = unbounded).
//
// It is the memory-bounded analogue of processing a directory of images:
// render W pages -> handle each page -> delete temp dir -> next W pages.
// This makes arbitrarily large PDFs safe to run without manual chunking,
// while `--first`/`--last` let the caller restrict the range explicitly for
// incremental/partial runs.
func processPDFWithWindow(
	renderer *ocr.PDFRenderer,
	pdfPath string,
	first, last, window int,
	totalPages int,
	handle PageProcessFn,
) error {
	if renderer == nil {
		return fmt.Errorf("pdf support is disabled in config (pdf.enabled=false), cannot process %q", pdfPath)
	}
	if window < 1 {
		window = 1
	}

	if first < 1 {
		first = 1
	}
	// Upper bound: caller-supplied last, else PDF page count, else unknown (0).
	hi := last
	if hi <= 0 {
		hi = totalPages
	}
	if hi > 0 && hi < first {
		return fmt.Errorf("invalid page range: first=%d > last=%d", first, hi)
	}

	base := filepath.Base(pdfPath)
	base = base[:len(base)-len(filepath.Ext(base))]

	for cur := first; hi <= 0 || cur <= hi; {
		wEnd := cur + window - 1
		if hi > 0 && wEnd > hi {
			wEnd = hi
		}

		// Render this window only. tmp dir is removed before the next window.
		pages, tmp, err := renderer.Render(pdfPath, cur, wEnd)
		if err != nil {
			return fmt.Errorf("render %q pages %d-%d: %w", pdfPath, cur, wEnd, err)
		}

		for _, pg := range pages {
			task := FileTask{
				ImagePath:  pg.Path,
				OutName:    fmt.Sprintf("%s-%04d", base, pg.Page),
				PageSize:   totalPages,
				PageNum:    pg.Page,
				SourceFile: pdfPath,
			}
			if err := handle(task); err != nil {
				ocr.Cleanup(tmp)
				return err
			}
		}
		_ = os.Stdout.Sync()
		ocr.Cleanup(tmp)

		// Advance window.
		if len(pages) < window {
			// Fewer than a full window returned: this is the tail, stop.
			break
		}
		cur += window
		_ = base
	}
	return nil
}

// pageIndexOf returns the 1-based page number from a rendered filename like
// "page-0042.png"; used for diagnostics.
func pageIndexOf(name string) int {
	ext := filepath.Ext(name)
	base := name[:len(name)-len(ext)]
	for i := len(base) - 1; i >= 0; i-- {
		c := base[i]
		if c == '-' || c == '_' {
			n := 0
			for _, r := range base[i+1:] {
				if r < '0' || r > '9' {
					return 0
				}
				n = n*10 + int(r-'0')
			}
			return n
		}
	}
	return 0
}

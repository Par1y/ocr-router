package main

import (
	"context"
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
	ctx context.Context,
	renderer *ocr.PDFRenderer,
	pdfPath string,
	first, last, window int,
	totalPages int,
	handle PageProcessFn,
) error {
	if renderer == nil {
		return fmt.Errorf("pdf support is disabled in config (pdf.enabled=false), cannot process %q", pdfPath)
	}
	if ctx == nil {
		ctx = context.Background()
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
		if err := ctx.Err(); err != nil {
			return err
		}
		wEnd := cur + window - 1
		if hi > 0 && wEnd > hi {
			wEnd = hi
		}

		// Render this window only. tmp dir is removed before the next window.
		// RenderContext ties the child rasterizer to ctx so cancellation kills
		// it promptly instead of waiting for the current window to finish.
		pages, tmp, err := renderer.RenderContext(ctx, pdfPath, cur, wEnd)
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

		// Advance window. Stop only when the last page we actually rendered
		// equals the requested upper bound (cur..wEnd), not when the count
		// drops below the window size — pdftoppm may emit fewer pages than
		// asked for various reasons, so a count-based tail check would risk
		// dropping pages mid-document.
		//
		// An empty page list from a successful render is treated as end of
		// document: this happens when the caller-supplied --last exceeds the
		// actual page count, or when totalPages was unknown (hi==0). A
		// genuine render failure is already surfaced via the error returned
		// by renderer.Render above, so reaching here with no pages means
		// there is simply nothing left to render.
		if len(pages) == 0 {
			break
		}
		lastRendered := 0
		for _, pg := range pages {
			if pg.Page > lastRendered {
				lastRendered = pg.Page
			}
		}
		// Reached the caller's explicit upper bound.
		if hi > 0 && lastRendered >= hi {
			break
		}
		// Strong EOF signal: the renderer reached the known end of the
		// document. This is the only safe termination when pages can be
		// skipped inside a window — relying on "rendered fewer than the
		// window" would falsely stop on the first partial window.
		if totalPages > 0 && lastRendered >= totalPages {
			break
		}
		// Unknown total page count (hi==0 and totalPages==0). Here the only
		// end-of-document cue we have is the renderer emitting fewer pages
		// than the window asked for, so use it — but only in that mode.
		if hi <= 0 && totalPages <= 0 && lastRendered < wEnd {
			break
		}
		// Make progress: if the renderer did not advance past `cur` we'd
		// loop forever, so bail out defensively.
		next := lastRendered + 1
		if next <= cur {
			break
		}
		cur = next
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

package ocr

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PageResult holds the OCR outcome for a single rendered page.
type PageResult struct {
	Page  int
	Text  string
	Error string
}

// paginate runs `recognizeOne` against each page of a PDF (or a single image
// when the input is not a PDF), returning a combined OCRResult.
//
// renderer may be nil; in that case PDF inputs are rejected with an error and
// image inputs are passed straight through by calling recognizeOne with the
// original request.
func paginate(
	ctx context.Context,
	renderer *PDFRenderer,
	recognizeOne func(ctx context.Context, req *OCRRequest) (*OCRResult, error),
	providerName string,
	req *OCRRequest,
	start time.Time,
) (*OCRResult, error) {
	if req.ImagePath == "" || !IsPDF(req.ImagePath) {
		return recognizeOne(ctx, req)
	}

	if renderer == nil {
		return nil, fmt.Errorf("provider %q does not support PDF input", providerName)
	}

	pages, tmpDir, err := renderer.Render(req.ImagePath)
	if err != nil {
		return nil, fmt.Errorf("pdf render failed: %w", err)
	}
	defer Cleanup(tmpDir)

	total := len(pages)
	var parts []string
	var pagesMeta []map[string]interface{}
	var firstErr error

	for _, pg := range pages {
		pageReq := *req
		pageReq.ImagePath = pg.Path

		r, err := recognizeOne(ctx, &pageReq)
		if err != nil {
			pagesMeta = append(pagesMeta, map[string]interface{}{
				"page":  pg.Page,
				"error": err.Error(),
			})
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		parts = append(parts, r.Text)
		pagesMeta = append(pagesMeta, map[string]interface{}{
			"page":        pg.Page,
			"text_length": len(r.Text),
			"duration":    r.Duration.String(),
		})
	}

	text := strings.Join(parts, "\n\n")
	if text == "" {
		if firstErr != nil {
			return nil, fmt.Errorf("all %d pages failed (first error: %w)", total, firstErr)
		}
		return nil, fmt.Errorf("pdf produced no recognized text (%d pages)", total)
	}

	if total > 1 {
		text = fmt.Sprintf("--- PDF (%d pages) ---\n%s", total, text)
	}

	return &OCRResult{
		Provider:  providerName,
		Text:      text,
		Timestamp: time.Now(),
		Duration:  time.Since(start),
		Metadata: map[string]interface{}{
			"is_pdf":          true,
			"pages":           total,
			"page_details":    pagesMeta,
			"source_file":     req.ImagePath,
		},
	}, nil
}

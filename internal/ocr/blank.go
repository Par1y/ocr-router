package ocr

import (
	"image"
	_ "image/jpeg" // register JPEG decoder
	_ "image/png"  // register PNG decoder
	"math"
	"os"
)

// IsBlankImage reports whether the image at path is (near) blank — i.e. almost
// entirely white/empty. Callers use this to skip an expensive OCR round-trip on
// pages that carry no content (cover backs, section separators, scan gaps).
//
// The heuristic decodes the image and samples pixels on a stride (capping work
// regardless of page resolution), counting a pixel as "content" when any RGB
// channel is meaningfully darker than white. A page is blank when the fraction
// of content pixels is below contentThreshold. A decode error is returned so
// the caller can fall back to OCR rather than silently dropping the page.
func IsBlankImage(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return false, err
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return true, nil
	}

	// Sample at most ~500k pixels; for larger pages use a grid stride.
	const maxSamples = 500000
	stride := 1
	if total := w * h; total > maxSamples {
		stride = int(math.Sqrt(float64(total) / float64(maxSamples)))
		if stride < 1 {
			stride = 1
		}
	}

	const whiteCutoff = 240 // 0..255; any channel below this counts as content
	const contentThreshold = 0.0015

	var sampled, content int
	for y := b.Min.Y; y < b.Max.Y; y += stride {
		for x := b.Min.X; x < b.Max.X; x += stride {
			r, g, bl, _ := img.At(x, y).RGBA() // each 0..65535
			sampled++
			if r>>8 < whiteCutoff || g>>8 < whiteCutoff || bl>>8 < whiteCutoff {
				content++
			}
		}
	}
	if sampled == 0 {
		return true, nil
	}
	return float64(content)/float64(sampled) < contentThreshold, nil
}

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
	// contentThreshold must stay conservative: a page is only declared blank
	// when its content fraction is far below what even a sparse real page (a
	// lone chapter title, a single page number, one short line) produces. A
	// single line of text at typical rendering DPI covers on the order of 0.1%
	// of the page, so anything at or above ~0.02% must be OCR'd, not skipped.
	// Erring toward "not blank" only costs a wasted OCR call; erring the other
	// way silently drops real text.
	const contentThreshold = 0.0002
	// minContentPixels guards small/low-resolution images where a fraction is
	// noisy: even a handful of clearly-dark sampled pixels means real content.
	const minContentPixels = 24

	var sampled, content int
	for y := b.Min.Y; y < b.Max.Y; y += stride {
		for x := b.Min.X; x < b.Max.X; x += stride {
			r, g, bl, a := img.At(x, y).RGBA() // each 0..65535, alpha-premultiplied
			sampled++
			// RGBA() returns alpha-premultiplied values. Composite the pixel over
			// a white background (documents are white) so transparency is handled
			// correctly: out = premult + white*(1-alpha) = premult + (0xffff-a).
			// A transparent pixel becomes white (not dark), while genuinely dark
			// content on a semi-transparent image still reads as dark — avoiding
			// both the "transparent counts as content" bug and the opposite
			// "semi-transparent text silently dropped" bug.
			inv := 0xffff - a
			if (r+inv)>>8 < whiteCutoff || (g+inv)>>8 < whiteCutoff || (bl+inv)>>8 < whiteCutoff {
				content++
			}
		}
	}
	if sampled == 0 {
		return true, nil
	}
	if content >= minContentPixels {
		return false, nil
	}
	return float64(content)/float64(sampled) < contentThreshold, nil
}

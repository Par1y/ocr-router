package ocr

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"strings"

	"github.com/nfnt/resize"
)

const (
	// DefaultMaxB64Len caps the base64 payload sent to providers. NVIDIA's
	// OCR API rejects larger bodies, so oversized images are re-encoded
	// (see compressExact) to fit under this limit.
	DefaultMaxB64Len = 180_000
)

// mimeMap maps a lowercase file extension to its MIME type. Extensions not
// listed here fall back to "image/png" in DetectMIME.
var mimeMap = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

// DetectMIME detects the MIME type of a file
func DetectMIME(filepath string) string {
	ext := strings.ToLower(filepath[strings.LastIndex(filepath, "."):])
	if mime, ok := mimeMap[ext]; ok {
		return mime
	}
	return "image/png"
}

// EncodeImage encodes an image to base64, compressing if necessary
func EncodeImage(filePath string, maxB64Len int) (b64, mime string, err error) {
	if maxB64Len == 0 {
		maxB64Len = DefaultMaxB64Len
	}

	raw, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read file: %w", err)
	}

	b64 = base64.StdEncoding.EncodeToString(raw)
	mime = DetectMIME(filePath)

	// Original image is within limit
	if len(b64) < maxB64Len {
		return b64, mime, nil
	}

	// Need compression
	return compressExact(filePath, maxB64Len)
}

// compressExact compresses an image to exactly fit within the target size
func compressExact(filePath string, maxB64Len int) (string, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return "", "", fmt.Errorf("failed to decode image: %w", err)
	}

	targetRaw := maxB64Len * 3 / 4

	// Phase 1: Binary search for optimal quality (keep original resolution)
	quality := binarySearchQuality(img, targetRaw)
	if quality > 0 {
		b64, err := encodeJPEG(img, quality)
		if err == nil {
			return b64, "image/jpeg", nil
		}
	}

	// Phase 2: Binary search for optimal scale (quality=85)
	scale := binarySearchScale(img, targetRaw, 85)
	resized := resize.Resize(
		uint(float64(img.Bounds().Dx())*scale),
		0,
		img,
		resize.Lanczos3,
	)

	b64, err := encodeJPEG(resized, 85)
	if err != nil {
		return "", "", fmt.Errorf("failed to encode image: %w", err)
	}

	return b64, "image/jpeg", nil
}

// binarySearchQuality finds the optimal JPEG quality
func binarySearchQuality(img image.Image, targetRaw int) int {
	lo, hi := 1, 95
	best := 0

	for lo <= hi {
		mid := (lo + hi) / 2
		size := estimateSize(img, mid)

		if size <= targetRaw {
			best = mid
			lo = mid + 1 // Try higher quality
		} else {
			hi = mid - 1
		}
	}

	return best
}

// binarySearchScale finds the optimal resize scale
func binarySearchScale(img image.Image, targetRaw, quality int) float64 {
	lo, hi := 0.1, 1.0

	for hi-lo > 0.01 {
		mid := (lo + hi) / 2
		w := uint(float64(img.Bounds().Dx()) * mid)
		h := uint(float64(img.Bounds().Dy()) * mid)

		resized := resize.Resize(w, h, img, resize.Lanczos3)
		size := estimateSize(resized, quality)

		if size <= targetRaw {
			lo = mid // Try larger size
		} else {
			hi = mid
		}
	}

	return lo
}

// encodeJPEG encodes an image to JPEG with the given quality
func encodeJPEG(img image.Image, quality int) (string, error) {
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// estimateSize estimates the JPEG size for an image with given quality
func estimateSize(img image.Image, quality int) int {
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	return buf.Len()
}

// ImageToBase64 reads an image file and returns its base64 encoding
func ImageToBase64(filePath string) (string, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

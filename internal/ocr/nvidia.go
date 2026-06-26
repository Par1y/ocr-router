package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ocr-router/internal/config"
	"ocr-router/internal/logger"
)

// NVIDIAProvider implements the NVIDIA Nemotron OCR v2 API
type NVIDIAProvider struct {
	config config.ProviderConfig
	logger *logger.Logger
	client *http.Client
}

// NVIDIARequest represents the NVIDIA OCR API request
type NVIDIARequest struct {
	Input []NVIDIAInput `json:"input"`
}

// NVIDIAInput represents an input item for the NVIDIA API
type NVIDIAInput struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// NVIDIAResponse represents the NVIDIA OCR API response
type NVIDIAResponse struct {
	Data []NVIDIAResult `json:"data"`
}

// NVIDIAResult represents a single OCR result from NVIDIA
type NVIDIAResult struct {
	TextDetections []TextDetection `json:"text_detections"`
}

// TextDetection represents a detected text region
type TextDetection struct {
	TextPrediction TextPrediction `json:"text_prediction"`
}

// TextPrediction represents the predicted text
type TextPrediction struct {
	Text string `json:"text"`
}

// NewNVIDIAProvider creates a new NVIDIA OCR provider
func NewNVIDIAProvider(cfg config.ProviderConfig, log *logger.Logger) *NVIDIAProvider {
	return &NVIDIAProvider{
		config: cfg,
		logger: log,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Name returns the provider name
func (p *NVIDIAProvider) Name() string {
	return "nvidia"
}

// Type returns the provider type
func (p *NVIDIAProvider) Type() ProviderType {
	return ProviderTypeNVIDIA
}

// Recognize performs OCR recognition using NVIDIA Nemotron OCR v2
func (p *NVIDIAProvider) Recognize(ctx context.Context, req *OCRRequest) (*OCRResult, error) {
	start := time.Now()

	// Encode image
	b64, mime, err := EncodeImage(req.ImagePath, p.config.MaxB64Len)
	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}

	// Build request
	nvidiaReq := NVIDIARequest{
		Input: []NVIDIAInput{
			{
				Type: "image_url",
				URL:  fmt.Sprintf("data:%s;base64,%s", mime, b64),
			},
		},
	}

	// Marshal request
	reqBody, err := json.Marshal(nvidiaReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.config.Endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)

	// Send request
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var nvidiaResp NVIDIAResponse
	if err := json.Unmarshal(body, &nvidiaResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text
	text := extractText(nvidiaResp)

	return &OCRResult{
		Provider:  p.Name(),
		Text:      text,
		Timestamp: time.Now(),
		Duration:  time.Since(start),
		Metadata: map[string]interface{}{
			"raw_response": nvidiaResp,
		},
	}, nil
}

// HealthCheck checks if the NVIDIA API is healthy
func (p *NVIDIAProvider) HealthCheck(ctx context.Context) error {
	// Use the dedicated health check endpoint
	// From NVIDIA docs: /v1/health/live returns {"live": true}
	healthURL := strings.TrimSuffix(p.config.Endpoint, "/")
	// Remove the inference path to get the base URL
	// e.g., https://ai.api.nvidia.com/v1/cv/nvidia/nemotron-ocr-v2 -> https://ai.api.nvidia.com
	if idx := strings.Index(healthURL, "/v1/"); idx > 0 {
		healthURL = healthURL[:idx]
	}
	healthURL += "/v1/health/live"

	req, err := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	// Drain the response body to allow connection reuse
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed: status %d", resp.StatusCode)
	}

	return nil
}

// extractText extracts text from the NVIDIA API response
func extractText(resp NVIDIAResponse) string {
	var lines []string
	for _, data := range resp.Data {
		for _, det := range data.TextDetections {
			if det.TextPrediction.Text != "" {
				lines = append(lines, det.TextPrediction.Text)
			}
		}
	}
	return joinLines(lines)
}

// joinLines joins multiple lines with newline
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}

	result := lines[0]
	for i := 1; i < len(lines); i++ {
		result += "\n" + lines[i]
	}
	return result
}

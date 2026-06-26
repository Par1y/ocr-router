package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"ocr-router/internal/config"
	"ocr-router/internal/logger"
)

// LLMVisionProvider implements the OpenAI-compatible vision API
type LLMVisionProvider struct {
	config config.ProviderConfig
	logger *logger.Logger
	client *http.Client
}

// LLMVisionRequest represents the OpenAI-compatible chat completion request
type LLMVisionRequest struct {
	Model    string        `json:"model"`
	Messages []LLMMessage  `json:"messages"`
	MaxTokens int          `json:"max_tokens,omitempty"`
}

// LLMMessage represents a message in the chat
type LLMMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// LLMContent represents content in a message
type LLMContent struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *ImageURL   `json:"image_url,omitempty"`
}

// ImageURL represents an image URL
type ImageURL struct {
	URL string `json:"url"`
}

// LLMVisionResponse represents the OpenAI-compatible chat completion response
type LLMVisionResponse struct {
	Choices []Choice `json:"choices"`
}

// Choice represents a choice in the response
type Choice struct {
	Message Message `json:"message"`
}

// Message represents a message in the response
type Message struct {
	Content string `json:"content"`
}

// NewLLMVisionProvider creates a new LLM Vision provider
func NewLLMVisionProvider(cfg config.ProviderConfig, log *logger.Logger) *LLMVisionProvider {
	return &LLMVisionProvider{
		config: cfg,
		logger: log,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Name returns the provider name
func (p *LLMVisionProvider) Name() string {
	return "llm_vision"
}

// Type returns the provider type
func (p *LLMVisionProvider) Type() ProviderType {
	return ProviderTypeLLMVision
}

// Recognize performs OCR recognition using an OpenAI-compatible vision API
func (p *LLMVisionProvider) Recognize(ctx context.Context, req *OCRRequest) (*OCRResult, error) {
	start := time.Now()

	// Get prompt
	prompt := p.config.Prompt
	if req.Prompt != "" {
		prompt = req.Prompt
	}

	// Encode image
	b64, mime, err := EncodeImage(req.ImagePath, DefaultMaxB64Len)
	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}

	// Build request
	messages := []LLMMessage{
		{
			Role: "user",
			Content: []LLMContent{
				{
					Type: "text",
					Text: prompt,
				},
				{
					Type: "image_url",
					ImageURL: &ImageURL{
						URL: fmt.Sprintf("data:%s;base64,%s", mime, b64),
					},
				},
			},
		},
	}

	maxTokens := p.config.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	llmReq := LLMVisionRequest{
		Model:     p.config.Model,
		Messages:  messages,
		MaxTokens: maxTokens,
	}

	// Marshal request
	reqBody, err := json.Marshal(llmReq)
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
	var llmResp LLMVisionResponse
	if err := json.Unmarshal(body, &llmResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text
	text := ""
	if len(llmResp.Choices) > 0 {
		text = llmResp.Choices[0].Message.Content
	}

	return &OCRResult{
		Provider:  p.Name(),
		Text:      text,
		Timestamp: time.Now(),
		Duration:  time.Since(start),
		Metadata: map[string]interface{}{
			"model": p.config.Model,
		},
	}, nil
}

// HealthCheck checks if the LLM Vision API is healthy
func (p *LLMVisionProvider) HealthCheck(ctx context.Context) error {
	// Make a minimal API call to verify the service is working
	minimalReq := LLMVisionRequest{
		Model: p.config.Model,
		Messages: []LLMMessage{
			{
				Role:    "user",
				Content: "hi",
			},
		},
		MaxTokens: 1,
	}

	reqBody, err := json.Marshal(minimalReq)
	if err != nil {
		return fmt.Errorf("failed to marshal health check request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.config.Endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.config.APIKey)

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

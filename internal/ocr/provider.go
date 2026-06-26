package ocr

import (
	"context"
	"encoding/json"
	"time"
)

// ProviderType represents the type of OCR provider
type ProviderType string

const (
	ProviderTypeNVIDIA    ProviderType = "nvidia"
	ProviderTypeLLMVision ProviderType = "llm_vision"
	ProviderTypeBrowser   ProviderType = "browser_sse"
)

// OCRRequest represents an OCR recognition request
type OCRRequest struct {
	ImagePath string            `json:"image_path"`
	ImageB64  string            `json:"image_b64,omitempty"`
	ImageURL  string            `json:"image_url,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`
	Options   map[string]string `json:"options,omitempty"`
}

// EvaluationResult represents the quality evaluation result
type EvaluationResult struct {
	Score    float64            `json:"score"`
	Reason   string             `json:"reason"`
	Pass     bool               `json:"pass"`
	Details  map[string]float64 `json:"details,omitempty"`
}

// AttemptRecord records a single OCR attempt
type AttemptRecord struct {
	Provider    string  `json:"provider"`
	Score       float64 `json:"score"`
	Passed      bool    `json:"passed"`
	Error       string  `json:"error,omitempty"`
	TextLength  int     `json:"text_length,omitempty"`
}

// OCRResult represents the OCR recognition result
type OCRResult struct {
	Provider          string             `json:"provider"`
	Fallback          bool               `json:"fallback"`
	Original          string             `json:"original,omitempty"`
	Text              string             `json:"text"`
	Evaluation        *EvaluationResult  `json:"evaluation,omitempty"`
	EvaluationWarning bool               `json:"evaluation_warning,omitempty"`
	QualityWarning    bool               `json:"quality_warning,omitempty"`
	Attempts          []AttemptRecord    `json:"attempts,omitempty"`
	BestScore         float64            `json:"best_score,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
	Timestamp         time.Time          `json:"timestamp"`
	Duration          time.Duration      `json:"duration"`
}

// Provider is the interface that all OCR providers must implement
type Provider interface {
	// Name returns the name of the provider
	Name() string

	// Type returns the type of the provider
	Type() ProviderType

	// Recognize performs OCR recognition on an image
	Recognize(ctx context.Context, req *OCRRequest) (*OCRResult, error)

	// HealthCheck checks if the provider is healthy
	HealthCheck(ctx context.Context) error
}

// ProviderStatus represents the health status of a provider
type ProviderStatus struct {
	Name      string        `json:"name"`
	Type      ProviderType  `json:"type"`
	Healthy   bool          `json:"healthy"`
	Error     string        `json:"error,omitempty"`
	Latency   time.Duration `json:"latency"`
	CheckedAt time.Time     `json:"checked_at"`
}

// MarshalJSON custom JSON marshaling for OCRResult
func (r *OCRResult) MarshalJSON() ([]byte, error) {
	type Alias OCRResult
	return json.Marshal(&struct {
		*Alias
		Timestamp string `json:"timestamp"`
		Duration  string `json:"duration"`
	}{
		Alias:     (*Alias)(r),
		Timestamp: r.Timestamp.Format(time.RFC3339),
		Duration:  r.Duration.String(),
	})
}

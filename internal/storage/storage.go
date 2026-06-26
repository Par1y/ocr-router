package storage

import (
	"context"
	"time"
)

// Storage is the interface for storing OCR results
type Storage interface {
	// Save saves an OCR result
	Save(ctx context.Context, result *StoredResult) error

	// Get retrieves an OCR result by ID
	Get(ctx context.Context, id string) (*StoredResult, error)

	// List lists all OCR results
	List(ctx context.Context, filter *Filter) ([]*StoredResult, error)

	// Delete deletes an OCR result by ID
	Delete(ctx context.Context, id string) error
}

// StoredResult represents a stored OCR result
type StoredResult struct {
	ID        string                 `json:"id"`
	Source    string                 `json:"source"`
	Provider  string                 `json:"provider"`
	Fallback  bool                   `json:"fallback"`
	Text      string                 `json:"text"`
	Score     float64                `json:"score,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

// Filter represents a filter for listing results
type Filter struct {
	Provider string
	MinScore float64
	Limit    int
	Offset   int
}

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileStorage implements Storage interface using file system
type FileStorage struct {
	baseDir string
	format  string
	mu      sync.RWMutex
}

// NewFileStorage creates a new file storage
func NewFileStorage(baseDir, format string) (*FileStorage, error) {
	// Create directories
	if err := os.MkdirAll(filepath.Join(baseDir, "formatted"), 0755); err != nil {
		return nil, fmt.Errorf("failed to create formatted dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "structured"), 0755); err != nil {
		return nil, fmt.Errorf("failed to create structured dir: %w", err)
	}

	return &FileStorage{
		baseDir: baseDir,
		format:  format,
	}, nil
}

// Save saves an OCR result
func (s *FileStorage) Save(ctx context.Context, result *StoredResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate ID if not set
	if result.ID == "" {
		result.ID = generateID(result.Source)
	}
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now()
	}

	// Save text file
	if s.format == "text" || s.format == "both" {
		if err := s.saveText(result); err != nil {
			return fmt.Errorf("failed to save text: %w", err)
		}
	}

	// Save JSON file
	if s.format == "json" || s.format == "both" {
		if err := s.saveJSON(result); err != nil {
			return fmt.Errorf("failed to save json: %w", err)
		}
	}

	return nil
}

// saveText saves the result as a formatted text file
func (s *FileStorage) saveText(result *StoredResult) error {
	textPath := filepath.Join(s.baseDir, "formatted", result.ID+".txt")

	// Build formatted text
	text := fmt.Sprintf("──────────────────────────────────────────────────────────────────────\n")
	text += fmt.Sprintf("  %s", result.Source)
	if result.Score > 0 {
		text += fmt.Sprintf(" [Score: %.2f", result.Score)
		if result.Score >= 0.7 {
			text += " ✓"
		} else {
			text += " ⚠"
		}
		text += "]"
	}
	text += fmt.Sprintf("\n──────────────────────────────────────────────────────────────────────\n\n")
	text += result.Text
	text += "\n"

	return os.WriteFile(textPath, []byte(text), 0644)
}

// saveJSON saves the result as a JSON file
func (s *FileStorage) saveJSON(result *StoredResult) error {
	jsonPath := filepath.Join(s.baseDir, "structured", result.ID+".json")

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal json: %w", err)
	}

	return os.WriteFile(jsonPath, data, 0644)
}

// Get retrieves an OCR result by ID
func (s *FileStorage) Get(ctx context.Context, id string) (*StoredResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getLocked(id)
}

// getLocked retrieves an OCR result by ID without acquiring the lock
func (s *FileStorage) getLocked(id string) (*StoredResult, error) {
	jsonPath := filepath.Join(s.baseDir, "structured", id+".json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("result not found: %s", id)
		}
		return nil, fmt.Errorf("failed to read json: %w", err)
	}

	var result StoredResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal json: %w", err)
	}

	return &result, nil
}

// List lists all OCR results
func (s *FileStorage) List(ctx context.Context, filter *Filter) ([]*StoredResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	structuredDir := filepath.Join(s.baseDir, "structured")
	entries, err := os.ReadDir(structuredDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var results []*StoredResult
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		id := entry.Name()[:len(entry.Name())-5] // Remove .json extension
		result, err := s.getLocked(id)
		if err != nil {
			continue
		}

		// Apply filter
		if filter != nil {
			if filter.Provider != "" && result.Provider != filter.Provider {
				continue
			}
			if filter.MinScore > 0 && result.Score < filter.MinScore {
				continue
			}
		}

		results = append(results, result)
	}

	// Apply pagination
	if filter != nil {
		if filter.Offset > 0 && filter.Offset < len(results) {
			results = results[filter.Offset:]
		}
		if filter.Limit > 0 && filter.Limit < len(results) {
			results = results[:filter.Limit]
		}
	}

	return results, nil
}

// Delete deletes an OCR result by ID
func (s *FileStorage) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete text file
	textPath := filepath.Join(s.baseDir, "formatted", id+".txt")
	os.Remove(textPath)

	// Delete JSON file
	jsonPath := filepath.Join(s.baseDir, "structured", id+".json")
	if err := os.Remove(jsonPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("result not found: %s", id)
		}
		return fmt.Errorf("failed to delete json: %w", err)
	}

	return nil
}

// generateID generates a unique ID from the source file
func generateID(source string) string {
	base := filepath.Base(source)
	ext := filepath.Ext(base)
	name := base[:len(base)-len(ext)]
	return fmt.Sprintf("%s_%d", name, time.Now().UnixNano())
}

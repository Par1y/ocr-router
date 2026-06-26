package ocr

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"ocr-router/internal/config"
	"ocr-router/internal/logger"
)

// FallbackEngine handles provider fallback with priority
type FallbackEngine struct {
	providers map[string]Provider
	config    config.FallbackConfig
	evaluator *Evaluator
	logger    *logger.Logger
}

// NewFallbackEngine creates a new fallback engine
func NewFallbackEngine(
	providers map[string]Provider,
	cfg config.FallbackConfig,
	evaluator *Evaluator,
	log *logger.Logger,
) *FallbackEngine {
	return &FallbackEngine{
		providers: providers,
		config:    cfg,
		evaluator: evaluator,
		logger:    log,
	}
}

// Recognize performs OCR with fallback based on priority
func (e *FallbackEngine) Recognize(ctx context.Context, req *OCRRequest) (*OCRResult, error) {
	sorted := e.getSortedProviders()

	var bestResult *OCRResult
	var bestScore float64
	var lastErr error
	var attempts []AttemptRecord

	for _, name := range sorted {
		provider, ok := e.providers[name]
		if !ok {
			e.logger.Warn("Provider not found", &logger.LogEntry{
				Event:    "provider_not_found",
				Provider: name,
			})
			continue
		}

		result, err := provider.Recognize(ctx, req)
		if err != nil {
			e.logger.LogProviderError(name, err)
			lastErr = err
			attempts = append(attempts, AttemptRecord{
				Provider: name,
				Error:    err.Error(),
			})

			// Wait before trying next provider
			time.Sleep(e.config.RetryDelay)
			continue
		}

		// Log OCR result for every attempt
		e.logger.Info("OCR result", &logger.LogEntry{
			Event:    "ocr_result",
			Provider: name,
			Extra: map[string]interface{}{
				"text_length": len(result.Text),
				"text":        truncateText(result.Text, 200),
			},
		})

		// If evaluator is not enabled, return result
		if !e.evaluator.IsEnabled() {
			return result, nil
		}

		// Quality evaluation
		eval, evalErr := e.evaluator.Evaluate(ctx, result.Text)
		if evalErr != nil {
			// Evaluation failed, record attempt and return result with warning
			result.EvaluationWarning = true
			e.logger.Warn("Evaluation failed, returning result", &logger.LogEntry{
				Event:    "evaluation_failed",
				Provider: name,
				Error:    evalErr.Error(),
			})
			attempts = append(attempts, AttemptRecord{
				Provider:   name,
				Score:      0,
				Passed:     false,
				TextLength: len(result.Text),
			})
			return result, nil
		}

		result.Evaluation = eval

		// Record attempt
		attempts = append(attempts, AttemptRecord{
			Provider:   name,
			Score:      eval.Score,
			Passed:     eval.Score >= e.evaluator.GetThreshold(),
			TextLength: len(result.Text),
		})

		// Log quality check result
		e.logger.Info("Quality check result", &logger.LogEntry{
			Event:    "quality_check",
			Provider: name,
			Score:    eval.Score,
			Threshold: e.evaluator.GetThreshold(),
			Extra: map[string]interface{}{
				"pass":   eval.Score >= e.evaluator.GetThreshold(),
				"reason": eval.Reason,
			},
		})

		// Quality passed
		if eval.Score >= e.evaluator.GetThreshold() {
			e.logger.LogQualityPassed(name, eval.Score)
			result.Attempts = attempts
			return result, nil
		}

		// Quality failed, log and continue to next provider
		e.logger.LogQualityFailed(name, eval.Score, eval.Reason)

		// Track best result
		if bestResult == nil || eval.Score > bestScore {
			bestResult = result
			bestScore = eval.Score
		}

		// Wait before trying next provider
		time.Sleep(e.config.RetryDelay)
	}

	// All providers failed or quality too low
	if bestResult != nil {
		// Return best result with quality warning
		bestResult.QualityWarning = true
		bestResult.BestScore = bestScore
		bestResult.Attempts = attempts
		e.logger.Warn("All providers failed quality check, returning best result", &logger.LogEntry{
			Event: "quality_warning",
			Extra: map[string]interface{}{
				"best_score":    bestScore,
				"best_provider": bestResult.Provider,
				"attempts":      len(attempts),
			},
		})
		return bestResult, nil
	}

	return nil, fmt.Errorf("all providers failed: %w", lastErr)
}

// getSortedProviders returns providers sorted by priority
func (e *FallbackEngine) getSortedProviders() []string {
	// Create a copy of providers for sorting
	priorities := make([]config.ProviderPriority, len(e.config.Providers))
	copy(priorities, e.config.Providers)

	// Sort by priority (lower number = higher priority)
	switch e.config.Strategy {
	case "sequential":
		// Sort by priority
		for i := 0; i < len(priorities); i++ {
			for j := i + 1; j < len(priorities); j++ {
				if priorities[i].Priority > priorities[j].Priority {
					priorities[i], priorities[j] = priorities[j], priorities[i]
				}
			}
		}
	case "random":
		// Shuffle randomly
		rand.Shuffle(len(priorities), func(i, j int) {
			priorities[i], priorities[j] = priorities[j], priorities[i]
		})
	default:
		// Default to sequential
		for i := 0; i < len(priorities); i++ {
			for j := i + 1; j < len(priorities); j++ {
				if priorities[i].Priority > priorities[j].Priority {
					priorities[i], priorities[j] = priorities[j], priorities[i]
				}
			}
		}
	}

	var result []string
	for _, p := range priorities {
		if p.Enabled {
			result = append(result, p.Name)
		}
	}
	return result
}

// GetProviders returns all registered providers
func (e *FallbackEngine) GetProviders() map[string]Provider {
	return e.providers
}

// GetEvaluator returns the evaluator
func (e *FallbackEngine) GetEvaluator() *Evaluator {
	return e.evaluator
}

// truncateText truncates text to a maximum length
func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}

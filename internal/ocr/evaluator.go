package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ocr-router/internal/config"
	"ocr-router/internal/logger"
)

// Evaluator evaluates the quality of OCR results
type Evaluator struct {
	config config.EvaluatorConfig
	logger *logger.Logger
	client *http.Client
}

// EvaluationRequest represents the evaluation request
type EvaluationRequest struct {
	Model          string       `json:"model"`
	Messages       []LLMMessage `json:"messages"`
	MaxTokens      int          `json:"max_tokens,omitempty"`
	Stream         bool         `json:"stream"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
}

// EvaluationResponse represents the evaluation response
type EvaluationResponse struct {
	Choices []Choice `json:"choices"`
}

// EvaluationResultParsed mirrors the JSON the evaluator LLM is prompted to
// return. It is converted into the public EvaluationResult by doEvaluate.
type EvaluationResultParsed struct {
	Score    float64            `json:"score"`
	Reason   string             `json:"reason"`
	Pass     bool               `json:"pass"`
	Details  map[string]float64 `json:"details,omitempty"`
}

// NewEvaluator creates a new evaluator
func NewEvaluator(cfg config.EvaluatorConfig, log *logger.Logger) *Evaluator {
	return &Evaluator{
		config: cfg,
		logger: log,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Evaluate evaluates the quality of an OCR result
func (e *Evaluator) Evaluate(ctx context.Context, ocrResult string) (*EvaluationResult, error) {
	var lastErr error

	// Retry up to max_retries times
	for attempt := 1; attempt <= e.config.MaxRetries; attempt++ {
		result, err := e.doEvaluate(ctx, ocrResult)
		if err == nil {
			return result, nil
		}

		lastErr = err
		e.logger.LogEvaluationRetry(attempt, err)

		if attempt < e.config.MaxRetries {
			time.Sleep(e.config.RetryDelay)
		}
	}

	// All retries failed
	e.logger.LogEvaluationFailed(lastErr)
	return nil, lastErr
}

// doEvaluate performs a single evaluation attempt
func (e *Evaluator) doEvaluate(ctx context.Context, ocrResult string) (*EvaluationResult, error) {
	// Truncate OCR result if too long
	maxLen := 2000
	truncatedResult := ocrResult
	if len(ocrResult) > maxLen {
		truncatedResult = ocrResult[:maxLen] + "...(truncated)"
	}

	// Build prompt
	prompt := strings.Replace(e.config.Prompt, "{{ocr_result}}", truncatedResult, -1)

	e.logger.Debug("Evaluator: Sending evaluation request", &logger.LogEntry{
		Event: "evaluation_request",
		Extra: map[string]interface{}{
			"model":       e.config.Model,
			"result_len":  len(ocrResult),
			"truncated":   len(ocrResult) > maxLen,
		},
	})

	// Build request
	messages := []LLMMessage{
		{
			Role: "user",
			Content: prompt,
		},
	}

	// MaxTokens is guaranteed non-zero by config.setDefaults.
	evalReq := EvaluationRequest{
		Model:          e.config.Model,
		Messages:       messages,
		MaxTokens:      e.config.MaxTokens,
		ReasoningEffort: e.config.ReasoningEffort,
	}

	// Marshal request
	reqBody, err := json.Marshal(evalReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", e.config.Endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+e.config.APIKey)

	// Send request
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Log raw response for debugging (truncate if too large)
	rawResponse := string(body)
	if len(rawResponse) > 2000 {
		rawResponse = rawResponse[:2000] + "...(truncated)"
	}
	e.logger.Debug("Evaluator: Raw API response", &logger.LogEntry{
		Event: "evaluation_raw_response",
		Extra: map[string]interface{}{
			"status_code": resp.StatusCode,
			"raw_response": rawResponse,
		},
	})

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var evalResp EvaluationResponse
	if err := json.Unmarshal(body, &evalResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract content
	if len(evalResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	content := evalResp.Choices[0].Message.Content

	if strings.TrimSpace(content) == "" {
		rawLen := len(body)
		if rawLen > 1000 {
			rawLen = 1000
		}
		return nil, fmt.Errorf("content is empty, raw response: %s", string(body[:rawLen]))
	}

	e.logger.Debug("Evaluator: Raw response content", &logger.LogEntry{
		Event: "evaluation_raw",
		Extra: map[string]interface{}{
			"content": content,
			"message": evalResp.Choices[0].Message,
		},
	})

	// Parse JSON from content
	var result EvaluationResultParsed
	if err := parseJSONFromContent(content, &result); err != nil {
		e.logger.Debug("Evaluator: JSON parse failed, trying fallback", &logger.LogEntry{
			Event: "evaluation_parse_fallback",
			Error: err.Error(),
		})
		
		// Fallback: try to extract score from text
		score := extractScoreFromText(content)
		if score > 0 {
			result = EvaluationResultParsed{
				Score:  score,
				Reason: "Extracted from text response",
			}
		} else {
			return nil, fmt.Errorf("failed to parse evaluation result: %w", err)
		}
	}

	// Set pass based on threshold
	result.Pass = result.Score >= e.config.Threshold

	e.logger.Debug("Evaluator: Parsed result", &logger.LogEntry{
		Event: "evaluation_result",
		Extra: map[string]interface{}{
			"score":    result.Score,
			"reason":   result.Reason,
			"pass":     result.Pass,
			"details":  result.Details,
		},
	})

	return &EvaluationResult{
		Score:   result.Score,
		Reason:  result.Reason,
		Pass:    result.Pass,
		Details: result.Details,
	}, nil
}

// parseJSONFromContent extracts JSON from LLM response content
func parseJSONFromContent(content string, v interface{}) error {
	// Try multiple approaches to find JSON
	
	// Approach 1: Find JSON block with regex
	re := regexp.MustCompile(`\{[\s\S]*\}`)
	matches := re.FindAllString(content, -1)
	
	for _, match := range matches {
		// Try to parse each match
		if err := json.Unmarshal([]byte(match), v); err == nil {
			return nil
		}
	}
	
	// Approach 2: Find between { and }
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")

	if start == -1 || end == -1 || end <= start {
		return fmt.Errorf("no JSON found in content: %s", content[:min(100, len(content))])
	}

	jsonStr := content[start : end+1]
	
	// Try to fix common JSON issues
	jsonStr = fixJSON(jsonStr)
	
	return json.Unmarshal([]byte(jsonStr), v)
}

// fixJSON tries to fix common JSON issues
func fixJSON(s string) string {
	// Remove trailing commas
	s = regexp.MustCompile(`,\s*}`).ReplaceAllString(s, "}")
	s = regexp.MustCompile(`,\s*]`).ReplaceAllString(s, "]")
	
	// Fix unquoted keys (common in LLM outputs)
	// Match: (non-quote or start) + unquoted-word + colon
	s = regexp.MustCompile(`([^"\w]|^)(\w+)\s*:`).ReplaceAllString(s, `$1"$2":`)
	
	return s
}

// extractScoreFromText extracts a score from text response
func extractScoreFromText(content string) float64 {
	// Look for patterns like "score: 0.85" or "Score: 0.85" or "0.85"
	re := regexp.MustCompile(`(?i)score[:\s]+(\d+\.?\d*)`)
	matches := re.FindStringSubmatch(content)
	if len(matches) > 1 {
		var score float64
		fmt.Sscanf(matches[1], "%f", &score)
		if score >= 0 && score <= 1 {
			return score
		}
	}
	
	// Look for any number between 0 and 1
	re = regexp.MustCompile(`\b0\.\d+\b`)
	matches = re.FindStringSubmatch(content)
	if len(matches) > 0 {
		var score float64
		fmt.Sscanf(matches[0], "%f", &score)
		if score >= 0 && score <= 1 {
			return score
		}
	}
	
	return 0
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// IsEnabled returns whether the evaluator is enabled
func (e *Evaluator) IsEnabled() bool {
	return e.config.Enabled
}

// GetThreshold returns the quality threshold
func (e *Evaluator) GetThreshold() float64 {
	return e.config.Threshold
}

package ocr

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ocr-router/internal/config"
	"ocr-router/internal/logger"
)

// BrowserSSEProvider implements the browser-based SSE provider (e.g., Gradio dots.mocr)
type BrowserSSEProvider struct {
	config config.ProviderConfig
	logger *logger.Logger
	client *http.Client
}

// NewBrowserSSEProvider creates a new browser SSE provider
func NewBrowserSSEProvider(cfg config.ProviderConfig, log *logger.Logger) *BrowserSSEProvider {
	return &BrowserSSEProvider{
		config: cfg,
		logger: log,
		client: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

// Name returns the provider name
func (p *BrowserSSEProvider) Name() string {
	return "browser_sse"
}

// Type returns the provider type
func (p *BrowserSSEProvider) Type() ProviderType {
	return ProviderTypeBrowser
}

// Recognize performs OCR recognition using browser-based SSE (dots.mocr Gradio API)
func (p *BrowserSSEProvider) Recognize(ctx context.Context, req *OCRRequest) (*OCRResult, error) {
	start := time.Now()

	p.logger.Debug("BrowserSSE: Starting recognition", &logger.LogEntry{
		Provider: p.Name(),
		Extra:    map[string]interface{}{"image_path": req.ImagePath},
	})

	// Generate session hash
	sessionHash := generateSessionHash()

	// Step 1: Upload image
	p.logger.Debug("BrowserSSE: Step 1 - Uploading image", &logger.LogEntry{
		Provider: p.Name(),
	})

	serverPath, err := p.uploadImage(ctx, req.ImagePath, sessionHash)
	if err != nil {
		return nil, fmt.Errorf("failed to upload image: %w", err)
	}

	p.logger.Debug("BrowserSSE: Image uploaded", &logger.LogEntry{
		Provider: p.Name(),
		Extra:    map[string]interface{}{"server_path": serverPath},
	})

	// Step 2: Register file (fn_index=1) and wait for completion
	p.logger.Debug("BrowserSSE: Step 2 - Registering file", &logger.LogEntry{
		Provider: p.Name(),
	})

	if err := p.registerAndWait(ctx, serverPath, req.ImagePath, sessionHash); err != nil {
		return nil, fmt.Errorf("failed to register file: %w", err)
	}

	// Step 3: Query status (fn_index=5) - no SSE needed
	p.logger.Debug("BrowserSSE: Step 3 - Querying status", &logger.LogEntry{
		Provider: p.Name(),
	})

	if err := p.queryStatus(ctx, sessionHash); err != nil {
		return nil, fmt.Errorf("failed to query status: %w", err)
	}

	// Step 4: Submit OCR task (fn_index=6) and wait for result
	p.logger.Debug("BrowserSSE: Step 4 - Submitting OCR task", &logger.LogEntry{
		Provider: p.Name(),
	})

	if err := p.submitOCRTask(ctx, serverPath, req.ImagePath, sessionHash); err != nil {
		return nil, fmt.Errorf("failed to submit OCR task: %w", err)
	}

	// Step 5: Listen for OCR result via SSE
	p.logger.Debug("BrowserSSE: Step 5 - Listening for OCR result", &logger.LogEntry{
		Provider: p.Name(),
	})

	text, err := p.waitForOCRResult(ctx, sessionHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get OCR result: %w", err)
	}

	p.logger.Debug("BrowserSSE: Recognition completed", &logger.LogEntry{
		Provider: p.Name(),
		Extra:    map[string]interface{}{"text_length": len(text)},
	})

	return &OCRResult{
		Provider:  p.Name(),
		Text:      text,
		Timestamp: time.Now(),
		Duration:  time.Since(start),
	}, nil
}

// HealthCheck checks if the browser SSE provider is healthy
func (p *BrowserSSEProvider) HealthCheck(ctx context.Context) error {
	baseURL := p.config.BaseURL
	if baseURL == "" {
		return fmt.Errorf("base URL not configured")
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", baseURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	return nil
}

// uploadImage uploads an image to the Gradio server
func (p *BrowserSSEProvider) uploadImage(ctx context.Context, filePath, sessionHash string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file field
	part, err := writer.CreateFormFile("files", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}

	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file: %w", err)
	}

	writer.Close()

	// Build URL
	uploadURL := p.config.BaseURL + p.config.Endpoints["upload"]
	uploadURL += "?upload_id=" + sessionHash

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", uploadURL, &buf)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	p.setHeaders(req)

	// Send request
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload failed (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response - returns array of paths
	var paths []string
	if err := json.Unmarshal(body, &paths); err != nil {
		return "", fmt.Errorf("failed to parse response: %s", string(body))
	}

	if len(paths) == 0 {
		return "", fmt.Errorf("no paths returned from upload")
	}

	return paths[0], nil
}

// registerAndWait registers the uploaded file and waits for completion
func (p *BrowserSSEProvider) registerAndWait(ctx context.Context, serverPath, filePath, sessionHash string) error {
	filename := filepath.Base(filePath)
	filesize := getFileSize(filePath)
	fileData := p.buildFileData(serverPath, filename, filesize)

	// Build request payload for fn_index=1
	payload := map[string]interface{}{
		"data": []interface{}{
			fileData,
			nil,
		},
		"event_data":   nil,
		"fn_index":     1,
		"trigger_id":   6,
		"session_hash": sessionHash,
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", p.config.BaseURL+p.config.Endpoints["submit"], bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	p.setHeaders(req)

	// Send request
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("request failed (status %d): %s", resp.StatusCode, string(body))
	}

	// Wait for registration to complete
	return p.waitForEvent(ctx, sessionHash, "register")
}

// queryStatus queries the status (fn_index=5)
func (p *BrowserSSEProvider) queryStatus(ctx context.Context, sessionHash string) error {
	payload := map[string]interface{}{
		"data":         []interface{}{},
		"event_data":   nil,
		"fn_index":     5,
		"trigger_id":   13,
		"session_hash": sessionHash,
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.config.BaseURL+"/gradio_api/run/predict", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	p.setHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("request failed (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// submitOCRTask submits the OCR task (fn_index=6)
func (p *BrowserSSEProvider) submitOCRTask(ctx context.Context, serverPath, filePath, sessionHash string) error {
	filename := filepath.Base(filePath)
	filesize := getFileSize(filePath)
	fileData := p.buildFileData(serverPath, filename, filesize)

	payload := map[string]interface{}{
		"data": []interface{}{
			nil,           // Optional second image
			"",            // Empty text
			fileData,      // Image file data
			"prompt_ocr",  // Task type
			"dots.mocr",   // Model
			3136,          // Max tokens
			11289600,      // Pixel limit
			true,          // Enable option
			"Extract the text content from this image.", // Prompt
		},
		"event_data":   nil,
		"fn_index":     6,
		"trigger_id":   13,
		"session_hash": sessionHash,
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.config.BaseURL+p.config.Endpoints["submit"], bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	p.setHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("request failed (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// waitForEvent waits for a specific event type
func (p *BrowserSSEProvider) waitForEvent(ctx context.Context, sessionHash, eventType string) error {
	sseURL := p.config.BaseURL + p.config.Endpoints["sse"]
	sseURL += "?session_hash=" + sessionHash

	req, err := http.NewRequestWithContext(ctx, "GET", sseURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "text/event-stream")
	p.setHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	timeout := time.After(2 * time.Minute)

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for %s event", eventType)
		case <-ctx.Done():
			return ctx.Err()
		default:
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return fmt.Errorf("failed to read SSE: %w", err)
				}
				return fmt.Errorf("SSE connection closed")
			}

			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}

			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data)
			if data == "" {
				continue
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			msg, _ := event["msg"].(string)

			switch msg {
			case "queue_full":
				return fmt.Errorf("queue full")
			case "process_completed":
				success, _ := event["success"].(bool)
				if !success {
					return fmt.Errorf("process failed")
				}
				p.logger.Debug("BrowserSSE: Event completed", &logger.LogEntry{
					Provider: p.Name(),
					Extra:    map[string]interface{}{"event_type": eventType},
				})
				return nil
			case "heartbeat":
				// Ignore
			}
		}
	}
}

// waitForOCRResult waits for the OCR result via SSE
func (p *BrowserSSEProvider) waitForOCRResult(ctx context.Context, sessionHash string) (string, error) {
	sseURL := p.config.BaseURL + p.config.Endpoints["sse"]
	sseURL += "?session_hash=" + sessionHash

	p.logger.Debug("BrowserSSE: Connecting to SSE for OCR result", &logger.LogEntry{
		Provider: p.Name(),
		Extra:    map[string]interface{}{"url": sseURL},
	})

	req, err := http.NewRequestWithContext(ctx, "GET", sseURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "text/event-stream")
	p.setHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	timeout := time.After(5 * time.Minute)

	for {
		select {
		case <-timeout:
			return "", fmt.Errorf("SSE timeout waiting for OCR result")
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return "", fmt.Errorf("failed to read SSE: %w", err)
				}
				return "", fmt.Errorf("SSE connection closed")
			}

			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}

			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data)
			if data == "" {
				continue
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				p.logger.Debug("BrowserSSE: Failed to parse SSE event", &logger.LogEntry{
					Provider: p.Name(),
					Error:    err.Error(),
				})
				continue
			}

			msg, _ := event["msg"].(string)

			p.logger.Debug("BrowserSSE: SSE event received", &logger.LogEntry{
				Provider: p.Name(),
				Extra:    map[string]interface{}{"msg": msg},
			})

			switch msg {
			case "queue_full":
				return "", fmt.Errorf("queue full")
			case "process_starts":
				p.logger.Debug("BrowserSSE: OCR processing started", &logger.LogEntry{
					Provider: p.Name(),
				})
			case "process_completed":
				success, _ := event["success"].(bool)
				if !success {
					return "", fmt.Errorf("OCR processing failed")
				}

				output, ok := event["output"].(map[string]interface{})
				if !ok {
					return "", fmt.Errorf("invalid output format")
				}

				data, ok := output["data"].([]interface{})
				if !ok || len(data) == 0 {
					return "", fmt.Errorf("no data in output")
				}

				// Extract text from output
				text := p.extractTextFromOutput(data)
				
				p.logger.Debug("BrowserSSE: OCR result extracted", &logger.LogEntry{
					Provider: p.Name(),
					Extra:    map[string]interface{}{"text_length": len(text)},
				})
				
				return text, nil

			case "heartbeat":
				// Ignore heartbeat
			}
		}
	}
}

// extractTextFromOutput extracts text from the Gradio output data
// dots.mocr response format:
// data[0]: FileData (image info)
// data[1]: Image info text ("Image Information:...")
// data[2]: OCR result text (actual content)
// data[3]: OCR result text (duplicate or alternative)
// data[4]: FileData (layout_results)
// data[5-8]: Other metadata
func (p *BrowserSSEProvider) extractTextFromOutput(data []interface{}) string {
	// Strategy 1: Look for the longest string that's not image info
	var bestText string
	for _, item := range data {
		if item == nil {
			continue
		}
		
		var text string
		switch v := item.(type) {
		case string:
			text = v
		case map[string]interface{}:
			// Try to extract from map
			if t, ok := v["value"].(string); ok {
				text = t
			} else if t, ok := v["text"].(string); ok {
				text = t
			} else if t, ok := v["data"].(string); ok {
				text = t
			}
		}
		
		// Skip empty, null-like, or metadata strings
		if text == "" || text == "null" || text == "__type__:update" {
			continue
		}
		
		// Skip image info metadata
		if isImageMetadata(text) {
			continue
		}
		
		// Skip button/status text
		if isStatusText(text) {
			continue
		}
		
		// Keep the longest valid text
		if len(text) > len(bestText) {
			bestText = text
		}
	}
	
	if bestText != "" {
		return bestText
	}

	// Fallback: convert first non-nil, non-empty item to string
	for _, item := range data {
		if item != nil {
			str := fmt.Sprintf("%v", item)
			if str != "" && str != "null" && !isImageMetadata(str) && !isStatusText(str) {
				return str
			}
		}
	}

	return ""
}

// isImageMetadata checks if the text is image metadata (not actual OCR result)
func isImageMetadata(text string) bool {
	// Filter out image metadata like "Image Information:..."
	if strings.Contains(text, "**Image Information:**") {
		return true
	}
	if strings.Contains(text, "Original Size:") {
		return true
	}
	if strings.Contains(text, "Model Input Size:") {
		return true
	}
	if strings.Contains(text, "Model:") && strings.Contains(text, "Server:") {
		return true
	}
	if strings.Contains(text, "Detected") && strings.Contains(text, "layout elements") {
		return true
	}
	if strings.Contains(text, "Session ID:") {
		return true
	}
	if strings.Contains(text, "page_info_box") {
		return true
	}
	return false
}

// isStatusText checks if the text is status/button text
func isStatusText(text string) bool {
	statusTexts := []string{
		"🔍 Parse",
		"🔍 Parsing...",
		"__type__:update",
		"update",
	}
	for _, s := range statusTexts {
		if text == s {
			return true
		}
	}
	return false
}

// buildFileData builds the Gradio file data object
func (p *BrowserSSEProvider) buildFileData(serverPath, filename string, filesize int64) map[string]interface{} {
	mime := DetectMIME(filename)

	return map[string]interface{}{
		"path":      serverPath,
		"url":       p.config.BaseURL + "/gradio_api/file=" + serverPath,
		"orig_name": filename,
		"size":      filesize,
		"mime_type": mime,
		"meta":      map[string]string{"_type": "gradio.FileData"},
	}
}

// setHeaders sets the request headers
func (p *BrowserSSEProvider) setHeaders(req *http.Request) {
	for key, value := range p.config.Headers {
		req.Header.Set(key, value)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36")
	}
}

// generateSessionHash generates a random session hash
func generateSessionHash() string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 11)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			// Fallback to time-based if crypto/rand fails
			b[i] = letters[time.Now().UnixNano()%int64(len(letters))]
			time.Sleep(1 * time.Nanosecond)
		} else {
			b[i] = letters[idx.Int64()]
		}
	}
	return string(b)
}

// getFileSize returns the size of a file
func getFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// Package handler exposes the HTTP surface of ocr-router: the JSON API
// (api.go) and the server-rendered WebUI (webui.go).
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"ocr-router/internal/logger"
	"ocr-router/internal/ocr"
	"ocr-router/internal/storage"
	"ocr-router/internal/task"
)

// APIHandler handles HTTP API requests
type APIHandler struct {
	engine   *ocr.FallbackEngine
	taskMgr  *task.TaskManager
	storage  storage.Storage
	logger   *logger.Logger
}

// NewAPIHandler creates a new API handler
func NewAPIHandler(
	engine *ocr.FallbackEngine,
	taskMgr *task.TaskManager,
	storage storage.Storage,
	logger *logger.Logger,
) *APIHandler {
	return &APIHandler{
		engine:  engine,
		taskMgr: taskMgr,
		storage: storage,
		logger:  logger,
	}
}

// RegisterRoutes registers the API routes
func (h *APIHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/ocr/sync", h.handleSyncOCR)
	mux.HandleFunc("/api/ocr/async", h.handleAsyncOCR)
	mux.HandleFunc("/api/ocr/batch", h.handleBatchOCR)
	mux.HandleFunc("/api/tasks", h.handleTasks)
	mux.HandleFunc("/api/tasks/", h.handleTaskByID)
	mux.HandleFunc("/api/providers", h.handleProviders)
	mux.HandleFunc("/api/health", h.handleHealth)
}

// handleSyncOCR handles synchronous OCR requests
func (h *APIHandler) handleSyncOCR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ocr.OCRRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.ImagePath == "" && req.ImageB64 == "" && req.ImageURL == "" {
		http.Error(w, "Either image_path, image_b64, or image_url is required", http.StatusBadRequest)
		return
	}

	// Create a new context with timeout (don't use request context)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Perform OCR
	result, err := h.engine.Recognize(ctx, &req)
	if err != nil {
		h.logger.Error("OCR failed", &logger.LogEntry{
			Event: "ocr_failed",
			Error: err.Error(),
		})
		http.Error(w, fmt.Sprintf("OCR failed: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	// Save result
	storedResult := &storage.StoredResult{
		ID:       generateID(req.ImagePath),
		Source:   req.ImagePath,
		Provider: result.Provider,
		Fallback: result.Fallback,
		Text:     result.Text,
		Score:    0,
		Metadata: result.Metadata,
	}
	if result.Evaluation != nil {
		storedResult.Score = result.Evaluation.Score
	}

	if err := h.storage.Save(r.Context(), storedResult); err != nil {
		h.logger.Error("Failed to save result", &logger.LogEntry{
			Event: "save_failed",
			Error: err.Error(),
		})
	}

	// Return result
	writeJSON(w, http.StatusOK, result)
}

// handleAsyncOCR handles asynchronous OCR requests
func (h *APIHandler) handleAsyncOCR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ocr.OCRRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.ImagePath == "" && req.ImageB64 == "" && req.ImageURL == "" {
		http.Error(w, "Either image_path, image_b64, or image_url is required", http.StatusBadRequest)
		return
	}

	// Submit task
	task := h.taskMgr.Submit(&req)

	writeJSON(w, http.StatusAccepted, task)
}

// handleBatchOCR handles batch OCR requests
func (h *APIHandler) handleBatchOCR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ImagePaths []string `json:"image_paths"`
		Provider   string   `json:"provider,omitempty"`
		Prompt     string   `json:"prompt,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.ImagePaths) == 0 {
		http.Error(w, "image_paths is required", http.StatusBadRequest)
		return
	}

	// Submit tasks
	var tasks []*task.Task
	for _, imagePath := range req.ImagePaths {
		ocrReq := &ocr.OCRRequest{
			ImagePath: imagePath,
			Prompt:    req.Prompt,
		}
		task := h.taskMgr.Submit(ocrReq)
		tasks = append(tasks, task)
	}

	writeJSON(w, http.StatusAccepted, tasks)
}

// handleTasks handles task list requests
func (h *APIHandler) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get status filter
	status := task.TaskStatus(r.URL.Query().Get("status"))

	// List tasks
	tasks := h.taskMgr.ListTasks(status)

	writeJSON(w, http.StatusOK, tasks)
}

// handleTaskByID handles task requests by ID
func (h *APIHandler) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	// Extract task ID from path
	id := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if id == "" {
		http.Error(w, "Task ID is required", http.StatusBadRequest)
		return
	}

	// Handle sub-paths
	switch {
	case strings.HasSuffix(id, "/result"):
		// Get task result
		taskID := strings.TrimSuffix(id, "/result")
		h.handleTaskResult(w, r, taskID)
	case strings.HasSuffix(id, "/cancel"):
		// Cancel task
		taskID := strings.TrimSuffix(id, "/cancel")
		h.handleCancelTask(w, r, taskID)
	default:
		// Get task status
		h.handleTaskStatus(w, r, id)
	}
}

// handleTaskStatus handles task status requests
func (h *APIHandler) handleTaskStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	task, ok := h.taskMgr.GetTask(id)
	if !ok {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, task)
}

// handleTaskResult handles task result requests
func (h *APIHandler) handleTaskResult(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	task, ok := h.taskMgr.GetTask(id)
	if !ok {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	if task.Status != "completed" {
		http.Error(w, "Task not completed", http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, task.Result)
}

// handleCancelTask handles task cancellation requests
func (h *APIHandler) handleCancelTask(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ok := h.taskMgr.CancelTask(id)
	if !ok {
		http.Error(w, "Task not found or cannot be cancelled", http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// handleProviders handles provider list requests
func (h *APIHandler) handleProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := h.engine.GetProviders()
	providerStatuses := make(map[string]ocr.ProviderStatus)

	for name, provider := range statuses {
		start := time.Now()
		err := provider.HealthCheck(r.Context())
		latency := time.Since(start)

		status := ocr.ProviderStatus{
			Name:      name,
			Type:      provider.Type(),
			Healthy:   err == nil,
			Latency:   latency,
			CheckedAt: time.Now(),
		}
		if err != nil {
			status.Error = err.Error()
		}
		providerStatuses[name] = status
	}

	writeJSON(w, http.StatusOK, providerStatuses)
}

// handleHealth handles health check requests
func (h *APIHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSON writes a JSON response
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// generateID generates a unique ID
func generateID(source string) string {
	base := filepath.Base(source)
	ext := filepath.Ext(base)
	name := base[:len(base)-len(ext)]
	return fmt.Sprintf("%s_%d", name, time.Now().UnixNano())
}

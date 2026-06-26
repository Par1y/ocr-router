package handler

import (
	"html/template"
	"net/http"
	"path/filepath"

	"ocr-router/internal/logger"
	"ocr-router/internal/ocr"
	"ocr-router/internal/task"
)

// WebUIHandler handles WebUI requests
type WebUIHandler struct {
	engine    *ocr.FallbackEngine
	taskMgr   *task.TaskManager
	logger    *logger.Logger
	templates map[string]*template.Template
}

// NewWebUIHandler creates a new WebUI handler
func NewWebUIHandler(
	engine *ocr.FallbackEngine,
	taskMgr *task.TaskManager,
	logger *logger.Logger,
) (*WebUIHandler, error) {
	// Parse templates
	templates := make(map[string]*template.Template)

	// List of page templates
	pages := []string{"index", "submit", "batch", "providers", "task"}

	for _, page := range pages {
		layoutPath := filepath.Join("templates", "layout.html")
		pagePath := filepath.Join("templates", page+".html")

		tmpl, err := template.ParseFiles(layoutPath, pagePath)
		if err != nil {
			return nil, err
		}

		templates[page] = tmpl
	}

	return &WebUIHandler{
		engine:    engine,
		taskMgr:   taskMgr,
		logger:    logger,
		templates: templates,
	}, nil
}

// RegisterRoutes registers the WebUI routes
func (h *WebUIHandler) RegisterRoutes(mux *http.ServeMux) {
	// Static files
	fs := http.FileServer(http.Dir("static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))

	// Pages
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/submit", h.handleSubmit)
	mux.HandleFunc("/batch", h.handleBatch)
	mux.HandleFunc("/providers", h.handleProviders)
	mux.HandleFunc("/task/", h.handleTask)
}

// handleIndex handles the index page
func (h *WebUIHandler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	h.renderTemplate(w, "index", nil)
}

// handleSubmit handles the submit page
func (h *WebUIHandler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	h.renderTemplate(w, "submit", nil)
}

// handleBatch handles the batch page
func (h *WebUIHandler) handleBatch(w http.ResponseWriter, r *http.Request) {
	h.renderTemplate(w, "batch", nil)
}

// handleProviders handles the providers page
func (h *WebUIHandler) handleProviders(w http.ResponseWriter, r *http.Request) {
	h.renderTemplate(w, "providers", nil)
}

// handleTask handles the task page
func (h *WebUIHandler) handleTask(w http.ResponseWriter, r *http.Request) {
	h.renderTemplate(w, "task", nil)
}

// renderTemplate renders a template
func (h *WebUIHandler) renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	tmpl, ok := h.templates[name]
	if !ok {
		http.Error(w, "Template not found", http.StatusInternalServerError)
		return
	}

	err := tmpl.ExecuteTemplate(w, "layout", data)
	if err != nil {
		h.logger.Error("Template error", &logger.LogEntry{
			Error: err.Error(),
		})
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

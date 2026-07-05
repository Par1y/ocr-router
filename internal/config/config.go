package config

import "time"

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	PDF       PDFConfig       `yaml:"pdf"`
	Providers ProvidersConfig `yaml:"providers"`
	Evaluator EvaluatorConfig `yaml:"evaluator"`
	Fallback  FallbackConfig  `yaml:"fallback"`
	Task      TaskConfig      `yaml:"task"`
	Logging   LoggingConfig   `yaml:"logging"`
}

// PDFConfig controls how PDF files are rasterized into images before OCR.
type PDFConfig struct {
	// Enabled toggles PDF support. When false, PDF inputs are rejected.
	Enabled bool `yaml:"enabled"`
	// Tool is the renderer to use: "pdftoppm" (default) or "mutool".
	// When empty, the first available tool is auto-detected at runtime.
	Tool string `yaml:"tool"`
	// BinPath overrides the path to the renderer binary. Empty = use PATH.
	BinPath string `yaml:"bin_path"`
	// InfoBinPath overrides the path to pdfinfo (used to count pages). Empty = use PATH.
	InfoBinPath string `yaml:"info_bin_path"`
	// DPI controls rendering resolution. Higher = sharper but bigger. Default 200.
	DPI int `yaml:"dpi"`
	// MaxPages limits the number of pages rendered per file. 0 = no limit.
	MaxPages int `yaml:"max_pages"`
	// Format is the output image format: "png" (default) or "jpeg".
	Format string `yaml:"format"`
	// TempDir overrides the temp directory used to store rendered pages. Empty = OS default.
	TempDir string `yaml:"temp_dir"`
	// JPEGQuality (1-95) used when Format == "jpeg". Default 85.
	JPEGQuality int `yaml:"jpeg_quality"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type StorageConfig struct {
	BaseDir string `yaml:"base_dir"`
	Format  string `yaml:"format"` // text, json, both
}

type ProvidersConfig struct {
	NVIDIA     ProviderConfig `yaml:"nvidia"`
	LLMVision  ProviderConfig `yaml:"llm_vision"`
	BrowserSSE ProviderConfig `yaml:"browser_sse"`
}

type ProviderConfig struct {
	Type        string            `yaml:"type"`
	Enabled     bool              `yaml:"enabled"`
	APIKey      string            `yaml:"api_key"`
	Endpoint    string            `yaml:"endpoint"`
	Model       string            `yaml:"model"`
	Prompt      string            `yaml:"prompt"`
	MaxTokens   int               `yaml:"max_tokens"`
	MaxB64Len   int               `yaml:"max_b64_len"`
	BaseURL     string            `yaml:"base_url"`
	Endpoints   map[string]string `yaml:"endpoints"`
	Headers     map[string]string `yaml:"headers"`
}

type EvaluatorConfig struct {
	Enabled        bool          `yaml:"enabled"`
	Endpoint       string        `yaml:"endpoint"`
	APIKey         string        `yaml:"api_key"`
	Model          string        `yaml:"model"`
	Threshold      float64       `yaml:"threshold"`
	MaxRetries     int           `yaml:"max_retries"`
	RetryDelay     time.Duration `yaml:"retry_delay"`
	Timeout        time.Duration `yaml:"timeout"`
	Prompt         string        `yaml:"prompt"`
	MaxTokens      int           `yaml:"max_tokens,omitempty"`
	ReasoningEffort string       `yaml:"reasoning_effort,omitempty"`
}

type FallbackConfig struct {
	Strategy    string             `yaml:"strategy"` // sequential, random
	MaxRetries  int                `yaml:"max_retries"`
	RetryDelay  time.Duration      `yaml:"retry_delay"`
	Providers   []ProviderPriority `yaml:"providers"`
}

type ProviderPriority struct {
	Name     string `yaml:"name"`
	Priority int    `yaml:"priority"`
	Enabled  bool   `yaml:"enabled"`
}

type TaskConfig struct {
	Workers      int           `yaml:"workers"`
	QueueSize    int           `yaml:"queue_size"`
	TaskTimeout  time.Duration `yaml:"task_timeout"`
}

type LoggingConfig struct {
	Level       string `yaml:"level"`
	File        string `yaml:"file"`
	Format      string `yaml:"format"`
	LogFallback bool   `yaml:"log_fallback"`
}

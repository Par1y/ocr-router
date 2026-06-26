package config

import "time"

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	Providers ProvidersConfig `yaml:"providers"`
	Evaluator EvaluatorConfig `yaml:"evaluator"`
	Fallback  FallbackConfig  `yaml:"fallback"`
	Task      TaskConfig      `yaml:"task"`
	Logging   LoggingConfig   `yaml:"logging"`
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
	Enabled    bool          `yaml:"enabled"`
	Endpoint   string        `yaml:"endpoint"`
	APIKey     string        `yaml:"api_key"`
	Model      string        `yaml:"model"`
	Threshold  float64       `yaml:"threshold"`
	MaxRetries int           `yaml:"max_retries"`
	RetryDelay time.Duration `yaml:"retry_delay"`
	Timeout    time.Duration `yaml:"timeout"`
	Prompt     string        `yaml:"prompt"`
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

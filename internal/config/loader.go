package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Expand environment variables
	expanded := expandEnvVars(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set defaults
	setDefaults(&cfg)

	return &cfg, nil
}

func expandEnvVars(s string) string {
	return os.Expand(s, func(key string) string {
		if val, ok := os.LookupEnv(key); ok {
			return val
		}
		return "${" + key + "}"
	})
}

func setDefaults(cfg *Config) {
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Storage.BaseDir == "" {
		cfg.Storage.BaseDir = "./ocr_output"
	}
	if cfg.Storage.Format == "" {
		cfg.Storage.Format = "both"
	}

	// NVIDIA defaults
	if cfg.Providers.NVIDIA.Endpoint == "" {
		cfg.Providers.NVIDIA.Endpoint = "https://ai.api.nvidia.com/v1/cv/nvidia/nemotron-ocr-v2"
	}
	if cfg.Providers.NVIDIA.MaxB64Len == 0 {
		cfg.Providers.NVIDIA.MaxB64Len = 180000
	}

	// LLM Vision defaults
	if cfg.Providers.LLMVision.Model == "" {
		cfg.Providers.LLMVision.Model = "step-router-v1"
	}
	if cfg.Providers.LLMVision.Prompt == "" {
		cfg.Providers.LLMVision.Prompt = "Extract all text from this image. Return only the text content."
	}
	if cfg.Providers.LLMVision.MaxTokens == 0 {
		cfg.Providers.LLMVision.MaxTokens = 4096
	}

	// Evaluator defaults
	if cfg.Evaluator.Threshold == 0 {
		cfg.Evaluator.Threshold = 0.7
	}
	if cfg.Evaluator.MaxRetries == 0 {
		cfg.Evaluator.MaxRetries = 3
	}
	if cfg.Evaluator.RetryDelay == 0 {
		cfg.Evaluator.RetryDelay = 1 * time.Second
	}
	if cfg.Evaluator.Timeout == 0 {
		cfg.Evaluator.Timeout = 30 * time.Second
	}
	if cfg.Evaluator.Model == "" {
		cfg.Evaluator.Model = "step-router-v1"
	}

	// Fallback defaults
	if cfg.Fallback.Strategy == "" {
		cfg.Fallback.Strategy = "sequential"
	}
	if cfg.Fallback.MaxRetries == 0 {
		cfg.Fallback.MaxRetries = 3
	}
	if cfg.Fallback.RetryDelay == 0 {
		cfg.Fallback.RetryDelay = 2 * time.Second
	}

	// Task defaults
	if cfg.Task.Workers == 0 {
		cfg.Task.Workers = 5
	}
	if cfg.Task.QueueSize == 0 {
		cfg.Task.QueueSize = 100
	}
	if cfg.Task.TaskTimeout == 0 {
		cfg.Task.TaskTimeout = 300 * time.Second
	}

	// Logging defaults
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}

	// PDF defaults
	if cfg.PDF.DPI == 0 {
		cfg.PDF.DPI = 200
	}
	if cfg.PDF.Format == "" {
		cfg.PDF.Format = "png"
	}
	if cfg.PDF.JPEGQuality == 0 {
		cfg.PDF.JPEGQuality = 85
	}

	// Provider defaults
	if cfg.Providers.NVIDIA.Type == "" {
		cfg.Providers.NVIDIA.Type = "nvidia"
	}
	if cfg.Providers.LLMVision.Type == "" {
		cfg.Providers.LLMVision.Type = "llm_vision"
	}
	if cfg.Providers.BrowserSSE.Type == "" {
		cfg.Providers.BrowserSSE.Type = "browser_sse"
	}
}

// GetProviderConfig returns the config for a specific provider
func (c *Config) GetProviderConfig(name string) (*ProviderConfig, error) {
	switch strings.ToLower(name) {
	case "nvidia":
		return &c.Providers.NVIDIA, nil
	case "llm_vision", "stepfun":
		return &c.Providers.LLMVision, nil
	case "browser_sse", "dots":
		return &c.Providers.BrowserSSE, nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", name)
	}
}

// GetEnabledProviders returns a list of enabled provider names
func (c *Config) GetEnabledProviders() []string {
	var providers []string
	if c.Providers.NVIDIA.Enabled {
		providers = append(providers, "nvidia")
	}
	if c.Providers.LLMVision.Enabled {
		providers = append(providers, "llm_vision")
	}
	if c.Providers.BrowserSSE.Enabled {
		providers = append(providers, "browser_sse")
	}
	return providers
}

// GetSortedProviders returns providers sorted by priority
func (c *Config) GetSortedProviders() []string {
	// Create a copy of providers for sorting
	priorities := make([]ProviderPriority, len(c.Fallback.Providers))
	copy(priorities, c.Fallback.Providers)

	// Sort by priority (lower number = higher priority)
	for i := 0; i < len(priorities); i++ {
		for j := i + 1; j < len(priorities); j++ {
			if priorities[i].Priority > priorities[j].Priority {
				priorities[i], priorities[j] = priorities[j], priorities[i]
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

package ocr

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Registry manages OCR providers
type Registry struct {
	providers map[string]Provider
	mu        sync.RWMutex
}

// NewRegistry creates a new provider registry
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register registers a provider with the given name
func (r *Registry) Register(name string, provider Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = provider
}

// Get returns a provider by name
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", name)
	}
	return provider, nil
}

// List returns all registered provider names
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// GetByType returns all providers of a specific type
func (r *Registry) GetByType(providerType ProviderType) []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var providers []Provider
	for _, p := range r.providers {
		if p.Type() == providerType {
			providers = append(providers, p)
		}
	}
	return providers
}

// HealthCheckAll checks the health of all registered providers
func (r *Registry) HealthCheckAll(ctx context.Context) map[string]ProviderStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	statuses := make(map[string]ProviderStatus)
	for name, provider := range r.providers {
		start := time.Now()
		err := provider.HealthCheck(ctx)
		latency := time.Since(start)

		status := ProviderStatus{
			Name:      name,
			Type:      provider.Type(),
			Healthy:   err == nil,
			Latency:   latency,
			CheckedAt: time.Now(),
		}
		if err != nil {
			status.Error = err.Error()
		}
		statuses[name] = status
	}
	return statuses
}

// DefaultRegistry is the global default registry
var DefaultRegistry = NewRegistry()

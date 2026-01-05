// Package secrets provides a pluggable secret store for fi-mcp.
//
// It supports multiple backends (env, keychain, file, 1Password) and provides
// a unified interface for secret management.
package secrets

import (
	"fmt"
	"sync"
)

// Backend is the interface that all secret store backends must implement.
type Backend interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
	List() ([]string, error)
	Name() string
	ReadOnly() bool
}

var ErrNotFound = fmt.Errorf("secret not found")
var ErrReadOnly = fmt.Errorf("backend is read-only")

// Manager coordinates multiple secret backends.
type Manager struct {
	backends []Backend
	primary  Backend
	mu       sync.RWMutex
}

func NewManager(backends ...Backend) *Manager {
	m := &Manager{backends: backends}
	for _, b := range backends {
		if !b.ReadOnly() {
			m.primary = b
			break
		}
	}
	return m
}

// Get retrieves a secret, searching backends in priority order.
// Returns the value and which backend it came from.
func (m *Manager) Get(key string) (string, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, b := range m.backends {
		val, err := b.Get(key)
		if err != nil {
			continue
		}
		if val != "" {
			return val, b.Name(), nil
		}
	}

	return "", "", ErrNotFound
}

func (m *Manager) GetValue(key string) string {
	val, _, _ := m.Get(key)
	return val
}

func (m *Manager) Set(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.primary == nil {
		return fmt.Errorf("no writable backend configured")
	}
	return m.primary.Set(key, value)
}

func (m *Manager) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.primary == nil {
		return fmt.Errorf("no writable backend configured")
	}
	return m.primary.Delete(key)
}

func (m *Manager) List() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	seen := make(map[string]bool)
	var keys []string

	for _, b := range m.backends {
		bKeys, err := b.List()
		if err != nil {
			continue
		}
		for _, k := range bKeys {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}

	return keys, nil
}

func (m *Manager) Backends() []Backend {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.backends
}

func (m *Manager) PrimaryBackend() Backend {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.primary
}

// DefaultManager creates a manager with the default backend configuration:
// 1. Environment variables (highest priority, read-only)
// 2. macOS Keychain (if available)
// 3. 1Password (if available)
// 4. Encrypted file store (fallback, writable)
func DefaultManager() (*Manager, error) {
	var backends []Backend

	backends = append(backends, NewEnvBackend())

	if kb, err := NewKeychainBackend(); err == nil {
		backends = append(backends, kb)
	}

	if op, err := NewOnePasswordBackend(""); err == nil {
		backends = append(backends, op)
	}

	if fb, err := NewFileBackend(""); err == nil {
		backends = append(backends, fb)
	}

	if len(backends) == 0 {
		return nil, fmt.Errorf("no secret backends available")
	}

	return NewManager(backends...), nil
}

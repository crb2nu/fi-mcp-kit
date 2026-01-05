package secrets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// OnePasswordBackend retrieves secrets from 1Password CLI (op).
// Requires 1Password CLI to be installed and authenticated.
type OnePasswordBackend struct {
	vault string
	mu    sync.RWMutex
	cache map[string]string
}

// NewOnePasswordBackend creates a new 1Password backend.
// vault can be empty to use the default vault.
func NewOnePasswordBackend(vault string) (*OnePasswordBackend, error) {
	if _, err := exec.LookPath("op"); err != nil {
		return nil, fmt.Errorf("1Password CLI (op) not found: %w", err)
	}

	cmd := exec.Command("op", "whoami", "--format=json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("1Password CLI not authenticated: %s", stderr.String())
	}

	return &OnePasswordBackend{
		vault: vault,
		cache: make(map[string]string),
	}, nil
}

type opItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Vault struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"vault"`
	Fields []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"fields"`
}

// Get retrieves a secret from 1Password.
// Key format: "item/field" or just "item" (defaults to "credential" field).
func (b *OnePasswordBackend) Get(key string) (string, error) {
	b.mu.RLock()
	if val, ok := b.cache[key]; ok {
		b.mu.RUnlock()
		return val, nil
	}
	b.mu.RUnlock()

	item, field := parseKey(key)

	args := []string{"item", "get", item, "--format=json"}
	if b.vault != "" {
		args = append(args, "--vault", b.vault)
	}

	cmd := exec.Command("op", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "not found") {
			return "", nil
		}
		return "", fmt.Errorf("op get: %s", stderr.String())
	}

	var itemResp opItem
	if err := json.Unmarshal(stdout.Bytes(), &itemResp); err != nil {
		return "", fmt.Errorf("parse 1Password response: %w", err)
	}

	for _, f := range itemResp.Fields {
		if f.Label == field || f.ID == field {
			b.mu.Lock()
			b.cache[key] = f.Value
			b.mu.Unlock()
			return f.Value, nil
		}
	}

	return "", nil
}

// Set stores a secret in 1Password.
// Creates a new "API Credential" item or updates existing.
func (b *OnePasswordBackend) Set(key, value string) error {
	item, field := parseKey(key)

	args := []string{"item", "get", item, "--format=json"}
	if b.vault != "" {
		args = append(args, "--vault", b.vault)
	}

	cmd := exec.Command("op", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		createArgs := []string{
			"item", "create",
			"--category", "API Credential",
			"--title", item,
			fmt.Sprintf("%s=%s", field, value),
		}
		if b.vault != "" {
			createArgs = append(createArgs, "--vault", b.vault)
		}

		cmd = exec.Command("op", createArgs...)
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("op create: %s", stderr.String())
		}
	} else {
		editArgs := []string{
			"item", "edit", item,
			fmt.Sprintf("%s=%s", field, value),
		}
		if b.vault != "" {
			editArgs = append(editArgs, "--vault", b.vault)
		}

		cmd = exec.Command("op", editArgs...)
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("op edit: %s", stderr.String())
		}
	}

	b.mu.Lock()
	b.cache[key] = value
	b.mu.Unlock()

	return nil
}

// Delete removes a secret from 1Password.
// Note: deletes the entire item, not just a field.
func (b *OnePasswordBackend) Delete(key string) error {
	item, _ := parseKey(key)

	args := []string{"item", "delete", item}
	if b.vault != "" {
		args = append(args, "--vault", b.vault)
	}

	cmd := exec.Command("op", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "not found") {
			return nil
		}
		return fmt.Errorf("op delete: %s", stderr.String())
	}

	b.mu.Lock()
	delete(b.cache, key)
	b.mu.Unlock()

	return nil
}

// List returns item titles (not individual fields).
func (b *OnePasswordBackend) List() ([]string, error) {
	args := []string{"item", "list", "--format=json"}
	if b.vault != "" {
		args = append(args, "--vault", b.vault)
	}

	cmd := exec.Command("op", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("op list: %s", stderr.String())
	}

	var items []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &items); err != nil {
		return nil, fmt.Errorf("parse 1Password list: %w", err)
	}

	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Title)
	}

	return keys, nil
}

func (b *OnePasswordBackend) Name() string {
	return "1password"
}

func (b *OnePasswordBackend) ReadOnly() bool {
	return false
}

func parseKey(key string) (item, field string) {
	parts := strings.SplitN(key, "/", 2)
	item = parts[0]
	if len(parts) > 1 {
		field = parts[1]
	} else {
		field = "credential"
	}
	return
}

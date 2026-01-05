package secrets

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// KeychainBackend uses macOS Keychain for secret storage.
// Uses the 'security' command-line tool.
type KeychainBackend struct {
	service       string
	legacyService string
}

// NewKeychainBackend creates a new macOS Keychain backend.
// Returns an error if not running on macOS or security command is not available.
func NewKeychainBackend() (*KeychainBackend, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("keychain backend only available on macOS")
	}
	if _, err := exec.LookPath("security"); err != nil {
		return nil, fmt.Errorf("security command not found: %w", err)
	}
	return &KeychainBackend{
		service:       "fi-mcp",
		legacyService: "loom",
	}, nil
}

func (b *KeychainBackend) Get(key string) (string, error) {
	if val, err := b.getFromService(b.service, key); err != nil {
		return "", err
	} else if val != "" {
		return val, nil
	}

	// Backwards compatibility with legacy "loom" keychain entries.
	return b.getFromService(b.legacyService, key)
}

func (b *KeychainBackend) getFromService(service, key string) (string, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", service,
		"-a", key,
		"-w",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "could not be found") ||
			strings.Contains(stderr.String(), "SecKeychainSearchCopyNext") {
			return "", nil
		}
		return "", fmt.Errorf("keychain get failed: %s", stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

func (b *KeychainBackend) Set(key, value string) error {
	_ = b.Delete(key)

	cmd := exec.Command("security", "add-generic-password",
		"-s", b.service,
		"-a", key,
		"-w", value,
		"-A",
		"-U",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain set failed: %s", stderr.String())
	}
	return nil
}

func (b *KeychainBackend) Delete(key string) error {
	// Delete from both services; ignore not-found errors.
	_ = b.deleteFromService(b.legacyService, key)
	return b.deleteFromService(b.service, key)
}

func (b *KeychainBackend) deleteFromService(service, key string) error {
	cmd := exec.Command("security", "delete-generic-password",
		"-s", service,
		"-a", key,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "could not be found") {
			return nil
		}
		return fmt.Errorf("keychain delete failed: %s", stderr.String())
	}

	return nil
}

func (b *KeychainBackend) List() ([]string, error) {
	cmd := exec.Command("security", "dump-keychain")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("keychain list failed: %w", err)
	}

	var keys []string
	lines := strings.Split(stdout.String(), "\n")

	inEntry := false
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.Contains(line, `"svce"`) && (strings.Contains(line, `"`+b.service+`"`) || strings.Contains(line, `"`+b.legacyService+`"`)) {
			inEntry = true
			continue
		}

		if inEntry && strings.Contains(line, `"acct"`) {
			if start := strings.Index(line, `="`); start != -1 {
				if end := strings.LastIndex(line, `"`); end > start+2 {
					keys = append(keys, line[start+2:end])
				}
			}
			inEntry = false
		}

		if strings.HasPrefix(line, "keychain:") {
			inEntry = false
		}
	}

	return keys, nil
}

func (b *KeychainBackend) Name() string {
	return "keychain"
}

func (b *KeychainBackend) ReadOnly() bool {
	return false
}

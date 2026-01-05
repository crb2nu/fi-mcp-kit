package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/pbkdf2"
)

// FileBackend stores secrets in an encrypted file.
// Uses AES-256-GCM for encryption.
type FileBackend struct {
	path   string
	key    []byte
	mu     sync.RWMutex
	cache  map[string]string
	loaded bool
}

func defaultFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fi-mcp", "secrets.enc")
}

func legacyFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "loom", "secrets.enc")
}

// NewFileBackend creates a new encrypted file backend.
//
// If path is empty, uses ~/.config/fi-mcp/secrets.enc (falling back to
// ~/.config/loom/secrets.enc if it exists).
//
// The encryption key is derived from FI_MCP_MASTER_KEY env var, legacy LOOM_MASTER_KEY,
// keychain, or a generated random value stored in keychain.
func NewFileBackend(path string) (*FileBackend, error) {
	if path == "" {
		path = defaultFilePath()
		if _, err := os.Stat(path); err != nil {
			if _, legacyErr := os.Stat(legacyFilePath()); legacyErr == nil {
				path = legacyFilePath()
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create secrets dir: %w", err)
	}

	key, err := getMasterKey()
	if err != nil {
		return nil, fmt.Errorf("get master key: %w", err)
	}

	return &FileBackend{
		path:  path,
		key:   key,
		cache: make(map[string]string),
	}, nil
}

func getMasterKey() ([]byte, error) {
	if envKey := os.Getenv("FI_MCP_MASTER_KEY"); envKey != "" {
		return deriveKey(envKey), nil
	}
	if envKey := os.Getenv("LOOM_MASTER_KEY"); envKey != "" {
		return deriveKey(envKey), nil
	}

	// Try to get from keychain (macOS)
	if kb, err := NewKeychainBackend(); err == nil {
		if key, err := kb.Get("_fi_mcp_master_key"); err == nil && key != "" {
			return deriveKey(key), nil
		}
		if key, err := kb.Get("_loom_master_key"); err == nil && key != "" {
			return deriveKey(key), nil
		}
	}

	key, err := generateMasterKey()
	if err != nil {
		return nil, err
	}

	if kb, err := NewKeychainBackend(); err == nil {
		_ = kb.Set("_fi_mcp_master_key", key)
		// Also set legacy key name to ease transitions with older tooling.
		_ = kb.Set("_loom_master_key", key)
	}

	return deriveKey(key), nil
}

func generateMasterKey() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", bytes), nil
}

func deriveKey(passphrase string) []byte {
	// Keep the legacy salt for compatibility with existing secret files.
	salt := []byte("loom-secrets-v1")
	return pbkdf2.Key([]byte(passphrase), salt, 100000, 32, sha256.New)
}

func (b *FileBackend) load() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.loaded {
		return nil
	}

	data, err := os.ReadFile(b.path)
	if os.IsNotExist(err) {
		b.cache = make(map[string]string)
		b.loaded = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("read secrets file: %w", err)
	}

	plaintext, err := b.decrypt(data)
	if err != nil {
		return fmt.Errorf("decrypt secrets: %w", err)
	}

	if err := json.Unmarshal(plaintext, &b.cache); err != nil {
		return fmt.Errorf("parse secrets: %w", err)
	}

	b.loaded = true
	return nil
}

func (b *FileBackend) save() error {
	plaintext, err := json.MarshalIndent(b.cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}

	ciphertext, err := b.encrypt(plaintext)
	if err != nil {
		return fmt.Errorf("encrypt secrets: %w", err)
	}

	tmpPath := b.path + ".tmp"
	if err := os.WriteFile(tmpPath, ciphertext, 0600); err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}

	if err := os.Rename(tmpPath, b.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename secrets: %w", err)
	}

	return nil
}

func (b *FileBackend) encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (b *FileBackend) decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce := ciphertext[:gcm.NonceSize()]
	ciphertext = ciphertext[gcm.NonceSize():]

	return gcm.Open(nil, nonce, ciphertext, nil)
}

func (b *FileBackend) Get(key string) (string, error) {
	if err := b.load(); err != nil {
		return "", err
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cache[key], nil
}

func (b *FileBackend) Set(key, value string) error {
	if err := b.load(); err != nil {
		return err
	}

	b.mu.Lock()
	b.cache[key] = value
	b.mu.Unlock()

	return b.save()
}

func (b *FileBackend) Delete(key string) error {
	if err := b.load(); err != nil {
		return err
	}

	b.mu.Lock()
	delete(b.cache, key)
	b.mu.Unlock()

	return b.save()
}

func (b *FileBackend) List() ([]string, error) {
	if err := b.load(); err != nil {
		return nil, err
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	keys := make([]string, 0, len(b.cache))
	for k := range b.cache {
		keys = append(keys, k)
	}
	return keys, nil
}

func (b *FileBackend) Name() string {
	return "file"
}

func (b *FileBackend) ReadOnly() bool {
	return false
}

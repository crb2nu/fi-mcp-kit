package secrets

import (
	"os"
	"strings"
)

// EnvBackend reads secrets from environment variables.
// This is read-only and has highest priority to allow runtime overrides.
type EnvBackend struct{}

func NewEnvBackend() *EnvBackend {
	return &EnvBackend{}
}

func (b *EnvBackend) Get(key string) (string, error) {
	return os.Getenv(key), nil
}

func (b *EnvBackend) Set(key, value string) error {
	return ErrReadOnly
}

func (b *EnvBackend) Delete(key string) error {
	return ErrReadOnly
}

func (b *EnvBackend) List() ([]string, error) {
	var keys []string

	secretSuffixes := []string{
		"_TOKEN", "_KEY", "_SECRET", "_PASSWORD", "_PAT",
		"_API_KEY", "_API_TOKEN", "_ACCESS_TOKEN",
	}

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		name := parts[0]

		for _, suffix := range secretSuffixes {
			if strings.HasSuffix(name, suffix) {
				keys = append(keys, name)
				break
			}
		}
	}

	return keys, nil
}

func (b *EnvBackend) Name() string {
	return "env"
}

func (b *EnvBackend) ReadOnly() bool {
	return true
}

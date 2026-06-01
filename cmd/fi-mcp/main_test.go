package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/proxy"
)

func TestParseProxyFlagsBackpressure(t *testing.T) {
	var out bytes.Buffer
	cfg, code := parseProxyFlags([]string{
		"--registry", "/tmp/registry.yaml",
		"--target", "codex",
		"--local-max-open", "3",
		"--hub-max-open", "4",
		"--backend-wait-timeout", "250ms",
	}, &out)

	if code != 0 {
		t.Fatalf("parseProxyFlags exit code = %d, output: %s", code, out.String())
	}
	if cfg.RegistryPath != "/tmp/registry.yaml" {
		t.Fatalf("registry path = %q", cfg.RegistryPath)
	}
	if cfg.LocalMaxOpen != 3 {
		t.Fatalf("local max open = %d, want 3", cfg.LocalMaxOpen)
	}
	if cfg.HubMaxOpen != 4 {
		t.Fatalf("hub max open = %d, want 4", cfg.HubMaxOpen)
	}
	if cfg.BackendWaitTimeout != 250*time.Millisecond {
		t.Fatalf("backend wait timeout = %s, want 250ms", cfg.BackendWaitTimeout)
	}
}

func TestParseProxyFlagsBackpressureDefaults(t *testing.T) {
	var out bytes.Buffer
	cfg, code := parseProxyFlags([]string{"--registry", "/tmp/registry.yaml"}, &out)

	if code != 0 {
		t.Fatalf("parseProxyFlags exit code = %d, output: %s", code, out.String())
	}
	if cfg.LocalMaxOpen != proxy.DefaultLocalMaxOpen {
		t.Fatalf("local max open = %d, want %d", cfg.LocalMaxOpen, proxy.DefaultLocalMaxOpen)
	}
	if cfg.HubMaxOpen != proxy.DefaultHubMaxOpen {
		t.Fatalf("hub max open = %d, want %d", cfg.HubMaxOpen, proxy.DefaultHubMaxOpen)
	}
	if cfg.BackendWaitTimeout != 0 {
		t.Fatalf("backend wait timeout = %s, want 0", cfg.BackendWaitTimeout)
	}
}

func TestParseProxyFlagsRejectsInvalidBackpressure(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "local max open",
			args: []string{"--registry", "/tmp/registry.yaml", "--local-max-open", "0"},
			want: "--local-max-open must be greater than 0",
		},
		{
			name: "hub max open",
			args: []string{"--registry", "/tmp/registry.yaml", "--hub-max-open", "-1"},
			want: "--hub-max-open must be greater than 0",
		},
		{
			name: "backend wait timeout",
			args: []string{"--registry", "/tmp/registry.yaml", "--backend-wait-timeout", "-1s"},
			want: "--backend-wait-timeout must be greater than or equal to 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			_, code := parseProxyFlags(tt.args, &out)

			if code != 2 {
				t.Fatalf("parseProxyFlags exit code = %d, want 2", code)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("output = %q, want to contain %q", out.String(), tt.want)
			}
		})
	}
}

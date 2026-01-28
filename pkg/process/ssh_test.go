package process

import (
	"testing"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

func TestExpandPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		result := expandPath(tt.input)
		if result != tt.expected {
			t.Errorf("expandPath(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}

	// Test ~ expansion (depends on HOME env)
	result := expandPath("~/test")
	if result == "~/test" {
		t.Error("expected ~ to be expanded")
	}
}

func TestBuildSSHAuthMethods_DefaultAgent(t *testing.T) {
	mgr := NewManager(nil, "test")

	spec := &registry.SSHSpec{
		Host: "example.com",
	}

	// This will try to use SSH agent which may or may not be available
	// Just verify it doesn't panic
	methods, err := mgr.buildSSHAuthMethods(spec)
	if err != nil && len(methods) == 0 {
		// Expected on systems without SSH agent or keys
		t.Skip("No SSH auth methods available")
	}
}

func TestBuildSSHAuthMethods_DisabledAgent(t *testing.T) {
	mgr := NewManager(nil, "test")

	useAgent := false
	spec := &registry.SSHSpec{
		Host:     "example.com",
		UseAgent: &useAgent,
	}

	// With agent disabled and no key file, should fail
	_, err := mgr.buildSSHAuthMethods(spec)
	if err == nil {
		// Only passes if there are default keys in ~/.ssh
		t.Log("Default SSH keys found")
	}
}

func TestBuildHostKeyCallback_InsecureMode(t *testing.T) {
	mgr := NewManager(nil, "test")

	strictChecking := false
	spec := &registry.SSHSpec{
		Host:                  "example.com",
		StrictHostKeyChecking: &strictChecking,
	}

	callback, err := mgr.buildHostKeyCallback(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callback == nil {
		t.Fatal("expected callback to be set")
	}
}

func TestBuildHostKeyCallback_DefaultStrict(t *testing.T) {
	mgr := NewManager(nil, "test")

	spec := &registry.SSHSpec{
		Host: "example.com",
	}

	// Default is strict, which tries to load known_hosts
	callback, err := mgr.buildHostKeyCallback(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callback == nil {
		t.Fatal("expected callback to be set")
	}
}

// Integration tests would require an actual SSH server
// Example:
//
// func TestSSHProcess_Integration(t *testing.T) {
//     if os.Getenv("SSH_TEST_HOST") == "" {
//         t.Skip("SSH_TEST_HOST not set")
//     }
//     // ... actual integration test
// }

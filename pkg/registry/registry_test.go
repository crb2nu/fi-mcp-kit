package registry_test

import (
	"os"
	"path/filepath"
	"testing"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

func TestLoadRegistry(t *testing.T) {
	// Create temp registry file
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry.yaml")

	content := `
version: 1
servers:
  - name: test-server
    common:
      command: echo
      args: ["hello"]
      description: "Test server"
    targets:
      local:
        description: "Local test"
      remote:
        command: remote-echo
`
	if err := os.WriteFile(regPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if reg.Version != 1 {
		t.Errorf("expected version 1, got %d", reg.Version)
	}

	if len(reg.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(reg.Servers))
	}

	if reg.Servers[0].Name != "test-server" {
		t.Errorf("expected server name 'test-server', got '%s'", reg.Servers[0].Name)
	}
}

func TestGetServerSpec(t *testing.T) {
	// Create temp registry file
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry.yaml")

	content := `
version: 1
servers:
  - name: test-server
    common:
      command: base-cmd
      args: ["arg1"]
      env:
        KEY1: value1
    targets:
      local:
        command: local-cmd
        env:
          KEY2: value2
`
	if err := os.WriteFile(regPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}

	// Test common spec
	spec, err := reg.GetServerSpec("test-server", "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != "base-cmd" {
		t.Errorf("expected command 'base-cmd', got '%s'", spec.Command)
	}

	// Test target merge
	spec, err = reg.GetServerSpec("test-server", "local")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != "local-cmd" {
		t.Errorf("expected command 'local-cmd', got '%s'", spec.Command)
	}
	if spec.Env["KEY1"] != "value1" {
		t.Errorf("expected KEY1=value1 from common")
	}
	if spec.Env["KEY2"] != "value2" {
		t.Errorf("expected KEY2=value2 from target")
	}
}

func TestListServers(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry.yaml")

	content := `
version: 1
servers:
  - name: server-a
  - name: server-b
  - name: server-c
`
	if err := os.WriteFile(regPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}

	servers := reg.ListServers()
	if len(servers) != 3 {
		t.Errorf("expected 3 servers, got %d", len(servers))
	}
}

func TestListServersByCategory(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry.yaml")

	content := `
version: 1
servers:
  - name: git-server
    categories: ["vcs", "local-only"]
  - name: k8s-server
    categories: ["cloud", "ops"]
  - name: other-server
    categories: ["ops"]
`
	if err := os.WriteFile(regPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}

	ops := reg.ListServersByCategory("ops")
	if len(ops) != 2 {
		t.Errorf("expected 2 ops servers, got %d", len(ops))
	}

	vcs := reg.ListServersByCategory("vcs")
	if len(vcs) != 1 {
		t.Errorf("expected 1 vcs server, got %d", len(vcs))
	}
}

func TestServerIsLocalOnly(t *testing.T) {
	server := &registry.Server{
		Name:       "local-server",
		Categories: []string{"filesystem", "vcs"},
	}

	if !server.IsLocalOnly() {
		t.Error("expected IsLocalOnly to be true for filesystem category")
	}

	cloud := &registry.Server{
		Name:       "cloud-server",
		Categories: []string{"cloud", "ops"},
	}

	if cloud.IsLocalOnly() {
		t.Error("expected IsLocalOnly to be false for cloud server")
	}
}

func TestEnvAliases(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry.yaml")

	content := `
version: 1
env_aliases:
  MY_SECRET:
    fallbacks:
      - MY_TOKEN
      - MY_KEY
servers: []
`
	if err := os.WriteFile(regPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}

	// Set fallback env var
	t.Setenv("MY_TOKEN", "secret-value")

	val, found := reg.ResolveEnv("MY_SECRET")
	if !found {
		t.Error("expected to find env var via fallback")
	}
	if val != "secret-value" {
		t.Errorf("expected 'secret-value', got '%s'", val)
	}
}

func TestStaticTools(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry.yaml")

	content := `
version: 1
servers:
  - name: test-server
    common:
      command: test
      tools:
        - name: do_thing
          description: "Does a thing"
          inputSchema:
            type: object
            properties:
              param:
                type: string
`
	if err := os.WriteFile(regPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatal(err)
	}

	if !reg.HasStaticTools() {
		t.Error("expected HasStaticTools to be true")
	}

	tools := reg.GetStaticTools("")
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	if tools[0].Name != "test-server__do_thing" {
		t.Errorf("expected namespaced tool name, got '%s'", tools[0].Name)
	}
}

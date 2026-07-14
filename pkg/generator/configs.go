package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

// GenerateConfigs generates MCP client configurations.
// registryPath is used to determine the workspace root for resolving ${repo} tokens.
func GenerateConfigs(reg *registry.Registry, outputDir string, targets []string, hubMode bool, hubURL string, proxyMode bool, proxyBinary string) error {
	return GenerateConfigsWithPath(reg, "", outputDir, targets, hubMode, hubURL, proxyMode, proxyBinary)
}

// GenerateConfigsWithPath generates MCP client configurations with an explicit registry path.
func GenerateConfigsWithPath(reg *registry.Registry, registryPath string, outputDir string, targets []string, hubMode bool, hubURL string, proxyMode bool, proxyBinary string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	if len(targets) == 0 || targets[0] == "all" {
		targets = []string{"codex", "kilocode", "vscode", "example_client", "example_desktop", "gemini", "antigravity"}
	}

	repoRoot := registry.GetRepoRoot(registryPath)

	for _, target := range targets {
		var err error
		switch target {
		case "vscode", "antigravity":
			err = generateJSONConfig(reg, registryPath, outputDir, target, hubMode, hubURL, proxyMode, proxyBinary, repoRoot)
		case "example_client":
			err = generateExampleClientConfig(reg, registryPath, outputDir, hubMode, hubURL, proxyMode, proxyBinary, repoRoot)
		case "example_desktop":
			err = generateExampleDesktopConfig(reg, registryPath, outputDir, hubMode, hubURL, proxyMode, proxyBinary, repoRoot)
		case "kilocode":
			err = generateKiloJSONConfig(reg, registryPath, outputDir, hubMode, hubURL, proxyMode, proxyBinary, repoRoot)
		default:
			err = generateTomlConfig(reg, registryPath, outputDir, target, hubMode, hubURL, proxyMode, proxyBinary, repoRoot)
		}
		if err != nil {
			return fmt.Errorf("generate %s: %w", target, err)
		}
	}

	return nil
}

func buildTargetMap(reg *registry.Registry, registryPath string, target string, hubMode bool, hubURL string, profile string, proxyMode bool, proxyBinary string, repoRoot string) (map[string]*registry.TargetSpec, error) {
	if proxyMode {
		args := []any{"proxy", "--target", profile}
		if strings.TrimSpace(registryPath) != "" {
			args = append(args, "--registry", registryPath)
		}
		if strings.TrimSpace(hubURL) != "" {
			args = append(args, "--hub-url", hubURL)
		}
		return map[string]*registry.TargetSpec{
			"fi_mcp": {
				Description: "fi-mcp proxy - unified access to all servers",
				Command:     proxyBinary,
				Args:        args,
				Hint:        "network",
				Timeout:     300,
				Type:        "stdio",
			},
		}, nil
	}

	resolved := make(map[string]*registry.TargetSpec)
	repoPath := repoRoot

	for _, server := range reg.Servers {
		spec, err := reg.GetServerSpec(server.Name, target)
		if err != nil {
			continue
		}

		spec.Command = ResolveCommand(spec.Command, repoPath, "local")
		resolvedArgs := ResolveArgs(spec.Args, repoPath, "local")
		spec.Args = make([]any, len(resolvedArgs))
		for i, v := range resolvedArgs {
			spec.Args[i] = v
		}
		newEnv := make(map[string]string)
		for k, v := range spec.Env {
			newEnv[k] = ResolveTokens(v, repoPath, "local")
		}
		spec.Env = newEnv

		if hubMode && !server.IsLocalOnly() {
			spec = convertToHubMode(spec, server.Name, hubURL, profile)
		}

		if spec.Command != "" {
			resolved[server.Name] = spec
		}
	}
	return resolved, nil
}

func convertToHubMode(spec *registry.TargetSpec, serverName, hubURL, profile string) *registry.TargetSpec {
	// v0: keep compatibility with existing hub client wrapper name.
	// v1: replace with `fi-mcp proxy --hub-url ...` once proxy supports hub transport.
	wrapper := "mcp-hub-wrapper"
	return &registry.TargetSpec{
		Description: spec.Description,
		Command:     wrapper,
		Args:        []any{serverName, "--profile", profile, "--hub-url", hubURL},
		Env:         spec.Env,
		Hint:        spec.Hint,
		Timeout:     spec.Timeout,
		AlwaysAllow: spec.AlwaysAllow,
		Type:        spec.Type,
	}
}

func generateExampleClientConfig(reg *registry.Registry, registryPath string, outputDir string, hubMode bool, hubURL string, proxyMode bool, proxyBinary string, repoRoot string) error {
	return generateJSONConfig(reg, registryPath, outputDir, "example_client", hubMode, hubURL, proxyMode, proxyBinary, repoRoot)
}

// generateJSONConfig generates mcp.json format configs for vscode and claude targets.
func generateJSONConfig(reg *registry.Registry, registryPath string, outputDir string, target string, hubMode bool, hubURL string, proxyMode bool, proxyBinary string, repoRoot string) error {
	targets, err := buildTargetMap(reg, registryPath, target, hubMode, hubURL, target, proxyMode, proxyBinary, repoRoot)
	if err != nil {
		return err
	}

	type JSONServer struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env,omitempty"`
	}

	config := map[string]map[string]JSONServer{"mcpServers": {}}
	for name, spec := range targets {
		args := []string{}
		for _, a := range spec.Args {
			args = append(args, fmt.Sprintf("%v", a))
		}

		config["mcpServers"][name] = JSONServer{
			Command: spec.Command,
			Args:    args,
			Env:     spec.Env,
		}
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	destDir := filepath.Join(outputDir, target)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "mcp.json"), data, 0644)
}

// generateKiloJSONConfig generates a Kilo 1.0 kilo.json config: a top-level
// `mcp` map (type local, array command, `environment`, millisecond timeouts)
// plus a `permission` map keyed by {server}_{tool} globs.
// Docs: https://kilo.ai/docs/automate/mcp/using-in-kilo-code
func generateKiloJSONConfig(reg *registry.Registry, registryPath string, outputDir string, hubMode bool, hubURL string, proxyMode bool, proxyBinary string, repoRoot string) error {
	targets, err := buildTargetMap(reg, registryPath, "kilocode", hubMode, hubURL, "kilocode", proxyMode, proxyBinary, repoRoot)
	if err != nil {
		return err
	}

	type KiloServer struct {
		Type        string            `json:"type"`
		Command     []string          `json:"command"`
		Environment map[string]string `json:"environment,omitempty"`
		Enabled     bool              `json:"enabled"`
		Timeout     int               `json:"timeout,omitempty"`
	}

	mcpServers := make(map[string]KiloServer)
	permission := make(map[string]string)
	for name, spec := range targets {
		cmd := []string{spec.Command}
		for _, a := range spec.Args {
			cmd = append(cmd, fmt.Sprintf("%v", a))
		}

		server := KiloServer{
			Type:    "local",
			Command: cmd,
			Enabled: true,
		}
		if len(spec.Env) > 0 {
			server.Environment = spec.Env
		}
		if spec.Timeout > 0 {
			server.Timeout = spec.Timeout * 1000 // Kilo uses milliseconds
		}
		mcpServers[name] = server

		// An empty AlwaysAllow keeps parity with the legacy always_allow=["*"].
		allows := spec.AlwaysAllow
		if len(allows) == 0 {
			allows = []string{"*"}
		}
		for _, tool := range allows {
			permission[name+"_"+tool] = "allow"
		}
	}

	config := map[string]any{
		"$schema": "https://app.kilo.ai/config.json",
		"mcp":     mcpServers,
	}
	if len(permission) > 0 {
		config["permission"] = permission
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	destDir := filepath.Join(outputDir, "kilocode")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "kilo.json"), data, 0644)
}

func generateExampleDesktopConfig(reg *registry.Registry, registryPath string, outputDir string, hubMode bool, hubURL string, proxyMode bool, proxyBinary string, repoRoot string) error {
	targets, err := buildTargetMap(reg, registryPath, "example_desktop", hubMode, hubURL, "example_desktop", proxyMode, proxyBinary, repoRoot)
	if err != nil {
		return err
	}

	type ExampleServer struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env,omitempty"`
	}

	config := map[string]map[string]ExampleServer{"mcpServers": {}}
	for name, spec := range targets {
		args := []string{}
		for _, a := range spec.Args {
			args = append(args, fmt.Sprintf("%v", a))
		}

		config["mcpServers"][name] = ExampleServer{
			Command: spec.Command,
			Args:    args,
			Env:     spec.Env,
		}
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	destDir := filepath.Join(outputDir, "example_desktop")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "example_desktop_config.json"), data, 0644)
}

func generateTomlConfig(reg *registry.Registry, registryPath string, outputDir, target string, hubMode bool, hubURL string, proxyMode bool, proxyBinary string, repoRoot string) error {
	targets, err := buildTargetMap(reg, registryPath, target, hubMode, hubURL, target, proxyMode, proxyBinary, repoRoot)
	if err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Generated MCP configuration for %s\n", target))
	sb.WriteString("# Source: registry.yaml\n\n")

	var names []string
	for name := range targets {
		names = append(names, name)
	}
	sortStrings(names)

	for _, name := range names {
		spec := targets[name]
		sb.WriteString(fmt.Sprintf("[mcp_servers.%s]\n", name))
		sb.WriteString(fmt.Sprintf("command = %q\n", spec.Command))

		argsJSON, _ := json.Marshal(spec.Args)
		sb.WriteString(fmt.Sprintf("args = %s\n", string(argsJSON)))

		if spec.Description != "" {
			sb.WriteString(fmt.Sprintf("description = %q\n", spec.Description))
		}
		if spec.Hint != "" {
			sb.WriteString(fmt.Sprintf("hint = %q\n", spec.Hint))
		}
		if spec.Timeout > 0 {
			sb.WriteString(fmt.Sprintf("timeout = %d\n", spec.Timeout))
		}
		if len(spec.AlwaysAllow) > 0 {
			allowJSON, _ := json.Marshal(spec.AlwaysAllow)
			sb.WriteString(fmt.Sprintf("always_allow = %s\n", string(allowJSON)))
		}

		if len(spec.Env) > 0 {
			sb.WriteString(fmt.Sprintf("[mcp_servers.%s.env]\n", name))

			var envKeys []string
			for k := range spec.Env {
				envKeys = append(envKeys, k)
			}
			sortStrings(envKeys)

			for _, k := range envKeys {
				sb.WriteString(fmt.Sprintf("%s = %q\n", k, spec.Env[k]))
			}
		}
		sb.WriteString("\n")
	}

	destDir := filepath.Join(outputDir, target)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "config.toml"), []byte(sb.String()), 0644)
}

func sortStrings(s []string) {
	sort.Strings(s)
}

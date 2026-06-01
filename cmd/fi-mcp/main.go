package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/generator"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/proxy"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "registry":
		os.Exit(runRegistry(os.Args[2:]))
	case "gen":
		os.Exit(runGen(os.Args[2:]))
	case "proxy":
		os.Exit(runProxy(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `fi-mcp

Usage:
  fi-mcp registry validate --registry <path>
  fi-mcp gen configs --registry <path> --output <dir> [--target all|codex|vscode|claude|claude_desktop|kilocode] [--hub-mode] [--hub-url <wss://...>]
  fi-mcp gen manifests --registry <path> --output <dir> [--namespace <ns>] [--image-registry <registry>]
  fi-mcp proxy --registry <path> [--target codex] [--local-max-open 10] [--hub-max-open 20] [--backend-wait-timeout 0s]

`)
}

func runRegistry(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}

	switch args[0] {
	case "validate":
		fs := flag.NewFlagSet("registry validate", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		regPath := fs.String("registry", "", "Path to registry.yaml")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if strings.TrimSpace(*regPath) == "" {
			fmt.Fprintln(os.Stderr, "missing --registry")
			return 2
		}

		reg, err := registry.Load(*regPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "registry load failed: %v\n", err)
			return 1
		}
		reg.MergeDefaultAliases()

		if errs := validateRegistry(reg); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "- %s\n", e)
			}
			return 1
		}

		fmt.Printf("OK: %d servers\n", len(reg.Servers))
		return 0
	default:
		usage()
		return 2
	}
}

func validateRegistry(reg *registry.Registry) []string {
	var errs []string
	if reg.Version <= 0 {
		errs = append(errs, "registry.version must be > 0")
	}
	if len(reg.Servers) == 0 {
		errs = append(errs, "registry.servers must not be empty")
		return errs
	}

	seen := make(map[string]bool)
	for _, s := range reg.Servers {
		if s == nil {
			errs = append(errs, "registry.servers contains null entry")
			continue
		}
		if strings.TrimSpace(s.Name) == "" {
			errs = append(errs, "server.name must not be empty")
			continue
		}
		if seen[s.Name] {
			errs = append(errs, fmt.Sprintf("duplicate server.name: %q", s.Name))
		}
		seen[s.Name] = true

		if s.Common == nil && len(s.Targets) == 0 {
			errs = append(errs, fmt.Sprintf("server %q: must define common or targets", s.Name))
		}
	}

	return errs
}

func runGen(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}

	switch args[0] {
	case "configs":
		fs := flag.NewFlagSet("gen configs", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		regPath := fs.String("registry", "", "Path to registry.yaml")
		outDir := fs.String("output", "generated/mcp", "Output directory")
		target := fs.String("target", "all", "Target config (all, vscode, codex, etc.)")
		hubMode := fs.Bool("hub-mode", false, "Generate hub-mode configs (uses mcp-hub-wrapper)")
		hubURL := fs.String("hub-url", "wss://mcp.flexinfer.ai/ws", "MCP hub WebSocket URL")
		proxyMode := fs.Bool("proxy-mode", false, "Generate a single fi-mcp proxy entry instead of individual servers")
		proxyBinary := fs.String("proxy-binary", "fi-mcp", "Proxy binary command/path (when --proxy-mode)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}

		if strings.TrimSpace(*regPath) == "" {
			fmt.Fprintln(os.Stderr, "missing --registry")
			return 2
		}

		reg, err := registry.Load(*regPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "registry load failed: %v\n", err)
			return 1
		}
		reg.MergeDefaultAliases()

		if err := generator.GenerateConfigsWithPath(reg, *regPath, *outDir, []string{*target}, *hubMode, *hubURL, *proxyMode, *proxyBinary); err != nil {
			fmt.Fprintf(os.Stderr, "gen configs failed: %v\n", err)
			return 1
		}

		fmt.Printf("Wrote configs to %s\n", *outDir)
		return 0

	case "manifests":
		fs := flag.NewFlagSet("gen manifests", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		regPath := fs.String("registry", "", "Path to registry.yaml")
		outDir := fs.String("output", "k3s/mcp-hub/servers", "Output directory")
		namespace := fs.String("namespace", "mcp-hub", "Kubernetes namespace")
		imageRegistry := fs.String("image-registry", "registry.harbor.lan/mcp", "Container image registry")
		includeGateway := fs.Bool("gateway", true, "Also generate fi-mcp-gateway manifests")
		gatewayHost := fs.String("gateway-host", "mcp.flexinfer.ai", "Gateway ingress host (empty disables ingress)")
		gatewayClass := fs.String("gateway-ingress-class", "", "Ingress class name (optional)")
		gatewayTLS := fs.String("gateway-tls-secret", "", "Ingress TLS secret name (optional)")
		gatewayImage := fs.String("gateway-image", "", "Gateway container image (default: <image-registry>/fi-mcp-gateway:latest)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}

		if strings.TrimSpace(*regPath) == "" {
			fmt.Fprintln(os.Stderr, "missing --registry")
			return 2
		}

		reg, err := registry.Load(*regPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "registry load failed: %v\n", err)
			return 1
		}
		reg.MergeDefaultAliases()

		if err := generator.GenerateManifests(reg, *outDir, generator.ManifestsOptions{
			Namespace:     *namespace,
			ImageRegistry: *imageRegistry,
			Gateway: generator.GatewayManifests{
				Enabled:          *includeGateway,
				Image:            *gatewayImage,
				IngressHost:      *gatewayHost,
				IngressClassName: *gatewayClass,
				TLSSecretName:    *gatewayTLS,
			},
		}); err != nil {
			fmt.Fprintf(os.Stderr, "gen manifests failed: %v\n", err)
			return 1
		}

		fmt.Printf("Wrote manifests to %s\n", *outDir)
		return 0
	default:
		usage()
		return 2
	}
}

func runProxy(args []string) int {
	cfg, code := parseProxyFlags(args, os.Stderr)
	if code != 0 {
		return code
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	p, err := proxy.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxy init failed: %v\n", err)
		return 1
	}
	defer p.Close()

	if err := p.Prepare(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "proxy prepare failed: %v\n", err)
		return 1
	}

	if err := p.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "proxy exited: %v\n", err)
		return 1
	}

	return 0
}

func parseProxyFlags(args []string, output io.Writer) (proxy.Config, int) {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	fs.SetOutput(output)
	regPath := fs.String("registry", "", "Path to registry.yaml")
	target := fs.String("target", "codex", "Registry target to use (codex, vscode, claude, etc.)")
	name := fs.String("name", "fi-mcp", "Proxy server name")
	version := fs.String("version", "0.0.0-dev", "Proxy server version")
	hubURL := fs.String("hub-url", "", "Hub WebSocket URL (e.g. wss://mcp.flexinfer.ai/ws)")
	hubToken := fs.String("hub-token", "", "Authentication token for the MCP hub")
	localMaxOpen := fs.Int("local-max-open", proxy.DefaultLocalMaxOpen, "Maximum open local backend connections per server")
	hubMaxOpen := fs.Int("hub-max-open", proxy.DefaultHubMaxOpen, "Maximum open hub backend connections per server")
	backendWaitTimeout := fs.Duration("backend-wait-timeout", 0, "How long to wait for a pooled backend connection when saturated (0 disables waiting)")
	if err := fs.Parse(args); err != nil {
		return proxy.Config{}, 2
	}

	if strings.TrimSpace(*regPath) == "" {
		fmt.Fprintln(output, "missing --registry")
		return proxy.Config{}, 2
	}
	if *localMaxOpen <= 0 {
		fmt.Fprintln(output, "--local-max-open must be greater than 0")
		return proxy.Config{}, 2
	}
	if *hubMaxOpen <= 0 {
		fmt.Fprintln(output, "--hub-max-open must be greater than 0")
		return proxy.Config{}, 2
	}
	if *backendWaitTimeout < 0 {
		fmt.Fprintln(output, "--backend-wait-timeout must be greater than or equal to 0")
		return proxy.Config{}, 2
	}

	return proxy.Config{
		RegistryPath:       *regPath,
		Target:             *target,
		ProxyName:          *name,
		ProxyVersion:       *version,
		HubURL:             *hubURL,
		HubToken:           *hubToken,
		LocalMaxOpen:       *localMaxOpen,
		HubMaxOpen:         *hubMaxOpen,
		BackendWaitTimeout: *backendWaitTimeout,
	}, 0
}

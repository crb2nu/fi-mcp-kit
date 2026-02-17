package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/generator"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/pool"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/router"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/secrets"
	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

type Config struct {
	RegistryPath string
	Target       string
	ProxyName    string
	ProxyVersion string
	HubURL       string
	HubToken     string
}

type Proxy struct {
	cfg Config

	reg     *registry.Registry
	router  *router.Router
	repoDir string

	secretManager *secrets.Manager

	server *mcp.Server

	reqID atomic.Int64

	hubPool   *pool.Pool
	localPool *pool.Pool
	authHooks []AuthHook
}

// AuthHook is a function that can validate a request before it is proxied.
type AuthHook func(ctx context.Context, serverName string, method string) error

func New(cfg Config) (*Proxy, error) {
	if strings.TrimSpace(cfg.RegistryPath) == "" {
		return nil, fmt.Errorf("RegistryPath is required")
	}
	if strings.TrimSpace(cfg.Target) == "" {
		cfg.Target = "codex"
	}
	if cfg.ProxyName == "" {
		cfg.ProxyName = "fi-mcp"
	}
	if cfg.ProxyVersion == "" {
		cfg.ProxyVersion = "0.0.0-dev"
	}

	reg, err := registry.Load(cfg.RegistryPath)
	if err != nil {
		return nil, err
	}
	reg.MergeDefaultAliases()

	sm, err := secrets.DefaultManager()
	if err != nil {
		return nil, err
	}

	r := router.New(router.Config{
		Registry:   reg,
		HubEnabled: cfg.HubURL != "",
		HubURL:     cfg.HubURL,
	})

	p := &Proxy{
		cfg:           cfg,
		reg:           reg,
		router:        r,
		repoDir:       registry.GetRepoRoot(cfg.RegistryPath),
		secretManager: sm,
	}

	p.hubPool = pool.New(pool.Config{
		MaxIdle:     5,
		MaxOpen:     20,
		IdleTimeout: 10 * time.Minute,
		DialFunc:    p.dialHub,
	})

	p.localPool = pool.New(pool.Config{
		MaxIdle:     2,
		MaxOpen:     10,
		IdleTimeout: 30 * time.Minute,
		DialFunc:    p.dialLocal,
	})

	p.server = mcp.NewServer(cfg.ProxyName, cfg.ProxyVersion)
	p.server.SetInstructions("Unified MCP proxy: routes tools to local MCP servers defined in registry.yaml.")

	return p, nil
}

func (p *Proxy) dialLocal(ctx context.Context, serverName string) (mcp.Transport, error) {
	spec, err := p.reg.GetServerSpec(serverName, p.cfg.Target)
	if err != nil || spec == nil {
		return nil, fmt.Errorf("local server spec not found: %s", serverName)
	}

	if strings.TrimSpace(spec.Command) == "" {
		return nil, fmt.Errorf("server %s has no local command defined", serverName)
	}

	resolvedCmd := generator.ResolveCommand(spec.Command, p.repoDir, "local")
	resolvedArgs := generator.ResolveArgs(spec.Args, p.repoDir, "local")

	cmd := exec.Command(resolvedCmd, resolvedArgs...)
	cmd.Dir = p.repoDir
	cmd.Env = p.buildEnv(spec.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Best-effort: keep stderr visible for debugging.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	transport := mcp.NewStdioTransport(stdout, stdin)

	lt := &localTransport{
		Transport: transport,
		cmd:       cmd,
	}

	// Initialize backend
	b := &backend{
		transport: lt,
		reqID:     &p.reqID,
	}
	if err := b.initialize(ctx); err != nil {
		lt.Close()
		return nil, fmt.Errorf("initialize local backend %s: %w", serverName, err)
	}

	return lt, nil
}

type localTransport struct {
	mcp.Transport
	cmd *exec.Cmd
}

func (t *localTransport) Close() error {
	err := t.Transport.Close()
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_, _ = t.cmd.Process.Wait()
	}
	return err
}

func (p *Proxy) dialHub(ctx context.Context, serverName string) (mcp.Transport, error) {
	u := p.cfg.HubURL
	if strings.Contains(u, "?") {
		u += "&server=" + serverName
	} else {
		u += "?server=" + serverName
	}

	headers := make(map[string]string)
	if p.cfg.HubToken != "" {
		headers["Authorization"] = "Bearer " + p.cfg.HubToken
	}

	transport, err := mcp.NewWebSocketTransport(ctx, mcp.WebSocketConfig{
		URL:        u,
		Headers:    headers,
		ClientInfo: mcp.ClientInfo{Name: p.cfg.ProxyName, Version: p.cfg.ProxyVersion},
	}, serverName)
	if err != nil {
		return nil, err
	}

	// Initialize connection
	if err := transport.Initialize(ctx); err != nil {
		transport.Close()
		return nil, err
	}

	return transport, nil
}

// AddAuthHook adds a hook for authentication/authorization.
func (p *Proxy) AddAuthHook(hook AuthHook) {
	p.authHooks = append(p.authHooks, hook)
}

// Prepare starts backend processes, discovers tools, and registers proxy tool handlers.
func (p *Proxy) Prepare(ctx context.Context) error {
	for _, srv := range p.reg.Servers {
		if srv == nil {
			continue
		}
		// Skip if no spec for target, but we'll try to get it in withBackend anyway if it's local
		// Actually, Prepare should probably just list tools.

		serverName := srv.Name
		var tools []mcp.Tool
		err := p.withBackend(ctx, serverName, func(b *backend) error {
			var err error
			tools, err = b.listTools(ctx)
			return err
		})
		if err != nil {
			log.Printf("Failed to discover tools for %s: %v", serverName, err)
			continue
		}

		for _, tool := range tools {
			baseToolName := tool.Name
			namespaced := serverName + "__" + baseToolName

			proxyTool := mcp.Tool{
				Name:        namespaced,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			}

			// Local tool call handler
			toolName := baseToolName
			p.server.AddTool(proxyTool, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
				var result *mcp.CallToolResult
				err := p.withBackend(ctx, serverName, func(b *backend) error {
					var err error
					result, err = b.callTool(ctx, toolName, args)
					return err
				})
				if err != nil {
					return mcp.ErrorResult(err), nil
				}
				return result, nil
			})
		}
	}

	if err := p.discoverHubTools(ctx); err != nil {
		log.Printf("Hub discovery failed: %v", err)
	}

	return nil
}

func (p *Proxy) Run(ctx context.Context) error {
	return p.server.Run(ctx)
}

func (p *Proxy) withBackend(ctx context.Context, serverName string, fn func(b *backend) error) error {
	decision, err := p.router.Route(ctx, serverName)
	if err != nil {
		return err
	}

	var conn *pool.Conn
	var pRef *pool.Pool
	if decision.Target == router.TargetHub {
		pRef = p.hubPool
	} else {
		pRef = p.localPool
	}

	conn, err = pRef.Get(ctx, serverName)
	if err != nil {
		return err
	}
	defer pRef.Put(conn)

	b := &backend{
		name:      serverName,
		transport: conn.Transport,
		reqID:     &p.reqID,
		pooled:    conn,
	}

	// Initialize if needed (though pool might have already initialized it via DialFunc)
	// For now, dialLocal and dialHub both initialize the connection.
	// But local backends need MCP initialization.

	return fn(b)
}

var (
	envRefRe      = regexp.MustCompile(`^\$\{env:([^}]+)\}$`)
	secretRefRe   = regexp.MustCompile(`^\$\{secret:([^}]+)\}$`)
	keychainRefRe = regexp.MustCompile(`^\$\{keychain:([^}]+)\}$`)
)

func (p *Proxy) buildEnv(env map[string]string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+len(env))
	out = append(out, base...)

	for k, v := range env {
		resolved := generator.ResolveTokens(v, p.repoDir, "local")
		resolved = p.resolveSecretRefs(resolved)
		out = append(out, fmt.Sprintf("%s=%s", k, resolved))
	}

	return out
}

func (p *Proxy) resolveSecretRefs(value string) string {
	if m := envRefRe.FindStringSubmatch(value); len(m) == 2 {
		return os.Getenv(m[1])
	}
	if m := secretRefRe.FindStringSubmatch(value); len(m) == 2 {
		return p.secretManager.GetValue(m[1])
	}
	if m := keychainRefRe.FindStringSubmatch(value); len(m) == 2 {
		return p.secretManager.GetValue(m[1])
	}
	return value
}

type backend struct {
	name string

	transport mcp.Transport
	reqID     *atomic.Int64

	mu sync.Mutex

	pooled *pool.Conn
}

func (b *backend) nextID() int64 {
	return b.reqID.Add(1)
}

func (b *backend) initialize(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID()

	initReq, err := mcp.NewRequest(id, "initialize", mcp.InitializeParams{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities: mcp.Capabilities{
			Sampling: &mcp.SamplingCapability{},
			Resources: &mcp.ResourcesCapability{
				Subscribe:   true,
				ListChanged: true,
			},
		},
		ClientInfo: mcp.ClientInfo{Name: "fi-mcp-proxy", Version: "0.0.0-dev"},
	})
	if err != nil {
		return err
	}

	if sendErr := b.transport.Send(ctx, initReq); sendErr != nil {
		return sendErr
	}
	if _, recvErr := b.recvResponse(ctx, id); recvErr != nil {
		return recvErr
	}

	initialized, err := mcp.NewRequest(nil, "notifications/initialized", nil)
	if err != nil {
		return err
	}
	return b.transport.Send(ctx, initialized)
}

func (b *backend) listTools(ctx context.Context) ([]mcp.Tool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID()
	req, err := mcp.NewRequest(id, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	if sendErr := b.transport.Send(ctx, req); sendErr != nil {
		return nil, sendErr
	}

	msg, err := b.recvResponse(ctx, id)
	if err != nil {
		return nil, err
	}

	var result mcp.ToolsListResult
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func (b *backend) callTool(ctx context.Context, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID()
	req, err := mcp.NewRequest(id, "tools/call", mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return nil, err
	}
	if sendErr := b.transport.Send(ctx, req); sendErr != nil {
		return nil, sendErr
	}

	msg, err := b.recvResponse(ctx, id)
	if err != nil {
		return nil, err
	}

	var result mcp.CallToolResult
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		// Some servers return error via msg.Error.
		if msg.Error != nil {
			return mcp.ErrorResult(errors.New(msg.Error.Message)), nil
		}
		return nil, err
	}
	return &result, nil
}

func (b *backend) recvResponse(ctx context.Context, id int64) (*mcp.Message, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	for {
		msg, err := b.transport.Recv(deadlineCtx)
		if err != nil {
			return nil, err
		}

		// Ignore notifications, etc.
		if msg.ID == nil {
			continue
		}

		// IDs may arrive as float64 if decoded differently; normalize.
		switch v := msg.ID.(type) {
		case int:
			if int64(v) != id {
				continue
			}
		case int64:
			if v != id {
				continue
			}
		case float64:
			if int64(v) != id {
				continue
			}
		default:
			continue
		}

		if msg.Error != nil {
			return nil, fmt.Errorf("rpc error (%d): %s", msg.Error.Code, msg.Error.Message)
		}
		return msg, nil
	}
}

func (b *backend) Close() error {
	// backend no longer closes the transport directly, the pool does.
	return nil
}

func (p *Proxy) Close() error {
	p.hubPool.Close()
	p.localPool.Close()
	return nil
}

func (p *Proxy) DefaultRegistryDir() string {
	return filepath.Dir(p.cfg.RegistryPath)
}

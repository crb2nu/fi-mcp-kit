// Package router provides intelligent routing between local and hub MCP servers.
package router

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

// ToolIndex maps tool names to the servers that provide them.
type ToolIndex map[string][]string

// RouteDecision describes where to route a request.
type RouteDecision struct {
	Target     Target
	ServerName string
	Reason     string
}

// Target indicates the routing destination.
type Target int

const (
	TargetLocal Target = iota
	TargetHub
	TargetUnavailable
)

func (t Target) String() string {
	switch t {
	case TargetLocal:
		return "local"
	case TargetHub:
		return "hub"
	default:
		return "unavailable"
	}
}

// Health tracks server health status.
type Health struct {
	Healthy      bool
	LastCheck    time.Time
	LastSuccess  time.Time
	LastError    time.Time
	ErrorMessage string
	ConsecFails  int
	AvgLatencyMs float64
}

// Router decides between local and hub routing.
type Router struct {
	registry    *registry.Registry
	localHealth map[string]*Health
	hubHealth   map[string]*Health
	hubEnabled  bool
	hubURL      string
	mu          sync.RWMutex

	failureThreshold int
	recoveryTime     time.Duration

	toolIndex ToolIndex
}

// Config configures the router.
type Config struct {
	Registry         *registry.Registry
	HubEnabled       bool
	HubURL           string
	FailureThreshold int
	RecoveryTime     time.Duration
}

// New creates a new router.
func New(cfg Config) *Router {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 3
	}
	if cfg.RecoveryTime <= 0 {
		cfg.RecoveryTime = 30 * time.Second
	}

	r := &Router{
		registry:         cfg.Registry,
		localHealth:      make(map[string]*Health),
		hubHealth:        make(map[string]*Health),
		hubEnabled:       cfg.HubEnabled,
		hubURL:           cfg.HubURL,
		failureThreshold: cfg.FailureThreshold,
		recoveryTime:     cfg.RecoveryTime,
		toolIndex:        make(ToolIndex),
	}

	if cfg.Registry != nil {
		for _, srv := range cfg.Registry.Servers {
			r.localHealth[srv.Name] = &Health{Healthy: true}
			r.hubHealth[srv.Name] = &Health{Healthy: true}
		}
		r.BuildToolIndex()
	}

	return r
}

func (r *Router) HubEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hubEnabled
}

// BuildToolIndex populates the in-memory index of available tools.
func (r *Router) BuildToolIndex() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.registry == nil {
		return
	}

	r.toolIndex = make(ToolIndex)
	for _, srv := range r.registry.Servers {
		if srv == nil {
			continue
		}

		// Check Common config
		if srv.Common != nil {
			for _, tool := range srv.Common.Tools {
				r.addToolToIndex(tool.Name, srv.Name)
			}
		}

		// Check target-specific configs
		if srv.Targets != nil {
			for _, target := range srv.Targets {
				if target != nil {
					for _, tool := range target.Tools {
						r.addToolToIndex(tool.Name, srv.Name)
					}
				}
			}
		}
	}
}

func (r *Router) addToolToIndex(toolName, serverName string) {
	servers, ok := r.toolIndex[toolName]
	if !ok {
		r.toolIndex[toolName] = []string{serverName}
		return
	}

	for _, s := range servers {
		if s == serverName {
			return
		}
	}
	r.toolIndex[toolName] = append(servers, serverName)
}

// AddToolToIndex adds a tool to the global index for smart routing.
func (r *Router) AddToolToIndex(toolName, serverName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addToolToIndex(toolName, serverName)
}

// ResolveServer determines the best server for a given tool name and arguments.
func (r *Router) ResolveServer(profile, toolName string, args map[string]any) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.registry == nil {
		return "", nil
	}

	// 1. Check Explicit Routing Rules
	for _, rule := range r.registry.Routing {
		if rule == nil || rule.ToolName != toolName {
			continue
		}

		argVal, ok := args[rule.Argument].(string)
		if ok {
			for _, c := range rule.Cases {
				if matchPattern(argVal, c.Match) {
					return c.Server, nil
				}
			}
		}

		if rule.Default != "" {
			return rule.Default, nil
		}
	}

	// 2. Check AlwaysAllow & Prefix
	for _, srv := range r.registry.Servers {
		if srv == nil {
			continue
		}

		spec, err := r.registry.GetServerSpec(srv.Name, profile)
		if err == nil && spec != nil {
			for _, allowed := range spec.AlwaysAllow {
				if allowed == toolName {
					return srv.Name, nil
				}
			}
		}

		prefix := srv.Name + "__"
		if strings.HasPrefix(toolName, prefix) {
			return srv.Name, nil
		}
	}

	// 3. Check Tool Index
	servers, ok := r.toolIndex[toolName]
	if ok {
		if len(servers) == 1 {
			return servers[0], nil
		}
		if len(servers) > 1 {
			return "", fmt.Errorf("ambiguous tool %q provided by multiple servers: %v", toolName, servers)
		}
	}

	return "", nil
}

func matchPattern(value, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(value, strings.TrimPrefix(pattern, "*"))
	}
	return value == pattern
}

// Route decides where to send a request.
func (r *Router) Route(ctx context.Context, serverName string) (*RouteDecision, error) {
	_ = ctx

	r.mu.RLock()
	defer r.mu.RUnlock()

	var server *registry.Server
	if r.registry != nil {
		for _, s := range r.registry.Servers {
			if s.Name == serverName {
				server = s
				break
			}
		}
	}
	if server == nil {
		if r.hubEnabled {
			return &RouteDecision{
				Target:     TargetHub,
				ServerName: serverName,
				Reason:     "server not in registry, routing to hub",
			}, nil
		}
		return &RouteDecision{
			Target:     TargetUnavailable,
			ServerName: serverName,
			Reason:     "server not in registry",
		}, nil
	}

	if server.IsLocalOnly() {
		localHealth := r.localHealth[serverName]
		if r.isHealthy(localHealth) {
			return &RouteDecision{
				Target:     TargetLocal,
				ServerName: serverName,
				Reason:     "local-only server",
			}, nil
		}
		return &RouteDecision{
			Target:     TargetUnavailable,
			ServerName: serverName,
			Reason:     fmt.Sprintf("local-only server unhealthy: %s", localHealth.ErrorMessage),
		}, nil
	}

	localHealth := r.localHealth[serverName]
	hubHealth := r.hubHealth[serverName]

	if r.isHealthy(localHealth) {
		return &RouteDecision{
			Target:     TargetLocal,
			ServerName: serverName,
			Reason:     "local healthy",
		}, nil
	}

	if r.hubEnabled && r.isHealthy(hubHealth) {
		return &RouteDecision{
			Target:     TargetHub,
			ServerName: serverName,
			Reason:     fmt.Sprintf("local unhealthy (%s) or missing, hub fallback", localHealth.ErrorMessage),
		}, nil
	}

	reason := fmt.Sprintf("local: %s", localHealth.ErrorMessage)
	if r.hubEnabled {
		reason = fmt.Sprintf("local: %s, hub: %s", localHealth.ErrorMessage, hubHealth.ErrorMessage)
	}
	return &RouteDecision{
		Target:     TargetUnavailable,
		ServerName: serverName,
		Reason:     reason,
	}, nil
}

func (r *Router) isHealthy(h *Health) bool {
	if h == nil {
		return false
	}

	if h.ConsecFails >= r.failureThreshold {
		if time.Since(h.LastError) < r.recoveryTime {
			return false
		}
	}

	return h.Healthy
}

func (r *Router) RecordSuccess(serverName string, target Target, latencyMs float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var h *Health
	switch target {
	case TargetLocal:
		h = r.localHealth[serverName]
	case TargetHub:
		h = r.hubHealth[serverName]
	default:
		return
	}

	if h == nil {
		h = &Health{}
		if target == TargetLocal {
			r.localHealth[serverName] = h
		} else {
			r.hubHealth[serverName] = h
		}
	}

	now := time.Now()
	h.Healthy = true
	h.LastCheck = now
	h.LastSuccess = now
	h.ConsecFails = 0
	h.ErrorMessage = ""

	if h.AvgLatencyMs == 0 {
		h.AvgLatencyMs = latencyMs
	} else {
		h.AvgLatencyMs = h.AvgLatencyMs*0.8 + latencyMs*0.2
	}
}

func (r *Router) RecordFailure(serverName string, target Target, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var h *Health
	switch target {
	case TargetLocal:
		h = r.localHealth[serverName]
	case TargetHub:
		h = r.hubHealth[serverName]
	default:
		return
	}

	if h == nil {
		h = &Health{}
		if target == TargetLocal {
			r.localHealth[serverName] = h
		} else {
			r.hubHealth[serverName] = h
		}
	}

	now := time.Now()
	h.LastCheck = now
	h.LastError = now
	h.ConsecFails++
	h.ErrorMessage = err.Error()

	if h.ConsecFails >= r.failureThreshold {
		h.Healthy = false
	}
}

func (r *Router) GetHealth(serverName string) (local, hub *Health) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.localHealth[serverName], r.hubHealth[serverName]
}

func (r *Router) GetAllHealth() map[string]struct{ Local, Hub *Health } {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]struct{ Local, Hub *Health })
	for name, h := range r.localHealth {
		result[name] = struct{ Local, Hub *Health }{Local: h, Hub: r.hubHealth[name]}
	}
	return result
}

// Proxy wraps transports and records health metrics.
type Proxy struct {
	router    *Router
	transport mcp.Transport
	server    string
	target    Target
}

func NewProxy(router *Router, transport mcp.Transport, server string, target Target) *Proxy {
	return &Proxy{
		router:    router,
		transport: transport,
		server:    server,
		target:    target,
	}
}

func (p *Proxy) Send(ctx context.Context, msg *mcp.Message) error {
	start := time.Now()
	err := p.transport.Send(ctx, msg)
	latency := float64(time.Since(start).Milliseconds())

	if err != nil {
		p.router.RecordFailure(p.server, p.target, err)
	} else {
		p.router.RecordSuccess(p.server, p.target, latency)
	}
	return err
}

func (p *Proxy) Recv(ctx context.Context) (*mcp.Message, error) {
	msg, err := p.transport.Recv(ctx)
	if err != nil {
		p.router.RecordFailure(p.server, p.target, err)
	}
	return msg, err
}

func (p *Proxy) Close() error {
	return p.transport.Close()
}

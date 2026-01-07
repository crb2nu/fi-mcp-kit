package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"gitlab.flexinfer.ai/libs/mcp-go"
)

func (p *Proxy) startHubBackend(ctx context.Context, serverName string) (*backend, error) {
	conn, err := p.hubPool.Get(ctx, serverName)
	if err != nil {
		return nil, err
	}

	b := &backend{
		name:      serverName,
		transport: conn.Transport,
		reqID:     &p.reqID,
		pooled:    conn,
	}

	// Double check or re-init if needed. pool.Get uses dialHub which initializes.
	// But backend.initialize sends MCP-level init.
	// Note: We might want to skip b.initialize if transport.Initialize already did it (it did).
	// However, backend.initialize is harmless if idempotent or if we need to sync state.
	// Actually mcp.WebSocketTransport.Initialize ALREADY does the MCP handshake.
	// So we can skip it here to avoid "already initialized" errors.

	p.backendsMu.Lock()
	p.backends[serverName] = b
	p.backendsMu.Unlock()

	return b, nil
}

func (p *Proxy) discoverHubTools(ctx context.Context) error {
	if !p.router.HubEnabled() {
		return nil
	}

	// 1. Fetch hosts from gateway
	hostsURL := p.cfg.HubURL
	if strings.HasPrefix(hostsURL, "ws") {
		hostsURL = "http" + strings.TrimPrefix(hostsURL, "ws")
	}
	// Trim /ws suffix if present to get base URL
	hostsURL = strings.TrimSuffix(hostsURL, "/ws")
	hostsURL += "/hosts"

	req, err := http.NewRequestWithContext(ctx, "GET", hostsURL, nil)
	if err != nil {
		return err
	}
	if p.cfg.HubToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.HubToken)
		// Also try query param for compatibility if needed, but header is standard
		q := req.URL.Query()
		q.Add("token", p.cfg.HubToken)
		req.URL.RawQuery = q.Encode()
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch hub hosts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fetch hub hosts failed (%d): %s", resp.StatusCode, string(body))
	}

	var hostNames []string
	if err := json.NewDecoder(resp.Body).Decode(&hostNames); err != nil {
		return fmt.Errorf("decode hub hosts: %w", err)
	}

	log.Printf("Discovered %d remote hosts on hub", len(hostNames))

	// 2. Prepare tools for each remote host
	for _, name := range hostNames {
		// Skip if already managed locally (local takes precedence)
		p.backendsMu.Lock()
		if _, exists := p.backends[name]; exists {
			p.backendsMu.Unlock()
			continue
		}
		p.backendsMu.Unlock()

		log.Printf("Auto-bridging remote host: %s", name)
		b, err := p.startHubBackend(ctx, name)
		if err != nil {
			log.Printf("Failed to bridge remote host %s: %v", name, err)
			continue
		}

		tools, err := b.listTools(ctx)
		if err != nil {
			log.Printf("Failed to list tools for remote host %s: %v", name, err)
			continue
		}

		for _, tool := range tools {
			namespaced := name + "__" + tool.Name
			proxyTool := mcp.Tool{
				Name:        namespaced,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			}

			serverName := name
			tn := tool.Name
			p.server.AddTool(proxyTool, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
				b, err := p.getBackend(ctx, serverName)
				if err != nil {
					return mcp.ErrorResult(err), nil
				}
				return b.callTool(ctx, tn, args)
			})
		}
	}

	return nil
}

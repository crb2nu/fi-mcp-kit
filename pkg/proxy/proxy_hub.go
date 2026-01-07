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
		log.Printf("Auto-bridging remote host: %s", name)
		var tools []mcp.Tool
		err := p.withBackend(ctx, name, func(b *backend) error {
			var err error
			tools, err = b.listTools(ctx)
			return err
		})
		if err != nil {
			log.Printf("Failed to bridge remote host %s: %v", name, err)
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
				var result *mcp.CallToolResult
				err := p.withBackend(ctx, serverName, func(b *backend) error {
					var err error
					result, err = b.callTool(ctx, tn, args)
					return err
				})
				if err != nil {
					return mcp.ErrorResult(err), nil
				}
				return result, nil
			})
		}
	}

	return nil
}

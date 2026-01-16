package proxy

import (
	"context"
	"log"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/router"
	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

func (p *Proxy) discoverHubTools(ctx context.Context) error {
	if !p.router.HubEnabled() {
		return nil
	}

	client := router.NewHubClient(p.cfg.HubURL, p.cfg.HubToken)

	// 1. Fetch hosts from gateway
	hostNames, err := client.DiscoverHosts(ctx)
	if err != nil {
		return err
	}

	log.Printf("Discovered %d remote hosts on hub", len(hostNames))

	// 2. Prepare tools for each remote host
	for _, name := range hostNames {
		log.Printf("Auto-bridging remote host: %s", name)

		// Fetch tools from Hub
		tools, err := client.FetchTools(ctx, name)
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

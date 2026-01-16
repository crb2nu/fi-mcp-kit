package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

// HubClient provides helper methods for interacting with an MCP Hub (Gateway).
type HubClient struct {
	url   string
	token string
}

func NewHubClient(url, token string) *HubClient {
	return &HubClient{
		url:   url,
		token: token,
	}
}

// DiscoverHosts fetches the list of available servers from the Hub.
func (c *HubClient) DiscoverHosts(ctx context.Context) ([]string, error) {
	hostsURL := c.url
	if strings.HasPrefix(hostsURL, "ws") {
		hostsURL = "http" + strings.TrimPrefix(hostsURL, "ws")
	}
	hostsURL = strings.TrimSuffix(hostsURL, "/ws")
	hostsURL += "/hosts"

	req, err := http.NewRequestWithContext(ctx, "GET", hostsURL, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch hub hosts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch hub hosts failed (%d): %s", resp.StatusCode, string(body))
	}

	var hostNames []string
	if err := json.NewDecoder(resp.Body).Decode(&hostNames); err != nil {
		return nil, fmt.Errorf("decode hub hosts: %w", err)
	}

	return hostNames, nil
}

// FetchTools fetches tools for a specific server from the Hub.
func (c *HubClient) FetchTools(ctx context.Context, serverName string) ([]mcp.Tool, error) {
	// Create a temporary WebSocket connection to fetch tools
	transport, err := mcp.NewWebSocketTransport(ctx, mcp.WebSocketConfig{
		URL:     c.url,
		Headers: map[string]string{"Authorization": "Bearer " + c.token},
	}, serverName)
	if err != nil {
		return nil, err
	}
	defer transport.Close()

	if initErr := transport.Initialize(ctx); initErr != nil {
		return nil, fmt.Errorf("init hub transport for tools: %w", initErr)
	}

	// tools/list
	id := int64(1)
	req, _ := mcp.NewRequest(id, "tools/list", nil)
	if sendErr := transport.Send(ctx, req); sendErr != nil {
		return nil, sendErr
	}

	resp, err := transport.Recv(ctx)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("hub rpc error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, err
	}

	return result.Tools, nil
}

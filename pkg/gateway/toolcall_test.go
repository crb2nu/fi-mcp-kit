package gateway

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

// fakeToolBackend is an in-process WebSocket MCP backend speaking just
// enough MCP for one-shot calls: initialize + tools/call.
type fakeToolBackend struct {
	srv *httptest.Server

	// callResult is returned as the JSON-RPC result of tools/call.
	callResult map[string]any

	mu       sync.Mutex
	lastTool string
	lastArgs map[string]any
}

func startFakeToolBackend(t *testing.T, callResult map[string]any) *fakeToolBackend {
	t.Helper()

	fb := &fakeToolBackend{callResult: callResult}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
				continue
			}

			var req map[string]any
			if err := json.Unmarshal(msg, &req); err != nil {
				continue
			}

			method, _ := req["method"].(string)
			id, hasID := req["id"]
			if !hasID {
				continue // notifications (e.g. notifications/initialized)
			}

			var resp map[string]any
			switch method {
			case "initialize":
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"protocolVersion": "2024-11-05",
						"serverInfo":      map[string]any{"name": "fake-backend", "version": "0.0.0"},
						"capabilities":    map[string]any{"tools": map[string]any{}},
					},
				}
			case "tools/call":
				params, _ := req["params"].(map[string]any)
				fb.mu.Lock()
				fb.lastTool, _ = params["name"].(string)
				fb.lastArgs, _ = params["arguments"].(map[string]any)
				fb.mu.Unlock()
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  fb.callResult,
				}
			default:
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"error":   map[string]any{"code": -32601, "message": "method not found"},
				}
			}

			out, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, out)
		}
	})

	fb.srv = httptest.NewServer(mux)
	t.Cleanup(fb.srv.Close)
	return fb
}

// wsURL returns the backend URL in the ws:// form stored in the registry
// (the loom-gateway-registry ConfigMap URLs include the /ws path).
func (fb *fakeToolBackend) wsURL() string {
	return "ws" + strings.TrimPrefix(fb.srv.URL, "http") + "/ws"
}

// deadBackendWSURL returns a ws:// URL that refuses connections.
func deadBackendWSURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return "ws://127.0.0.1:" + strconv.Itoa(port) + "/ws"
}

// toolCallTestRegistry mirrors the loom-gateway-registry ConfigMap shape:
// per-server url + top-level always_allow.
func toolCallTestRegistry(backendURL string) *registry.Registry {
	return &registry.Registry{
		Version: 1,
		Servers: []*registry.Server{
			{
				Name:        "atlassian",
				URL:         backendURL,
				AlwaysAllow: []string{"jira_search", "confluence_search"},
			},
			{
				Name:       "local-fs",
				Categories: []string{"local-only"},
				Common: &registry.TargetSpec{
					AlwaysAllow: []string{"read_file"},
				},
			},
		},
	}
}

func newToolCallServer(t *testing.T, hub *Hub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// Mirrors the cmd/fi-mcp-gateway/main.go wiring.
	mux.HandleFunc("POST /api/v1/tools/{server}/{tool}", func(w http.ResponseWriter, r *http.Request) {
		ToolCallHandler(hub, w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func postToolCall(t *testing.T, baseURL, path, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if readErr != nil {
			break
		}
	}
	return resp, sb.String()
}

func TestToolCallHandler(t *testing.T) {
	successResult := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": `{"hits":[{"key":"ICC-1"}]}`},
		},
		"isError": false,
	}
	errorResult := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "jql parse failure"},
		},
		"isError": true,
	}

	tests := []struct {
		name         string
		path         string
		body         string
		callResult   map[string]any // nil = no backend (dead URL)
		wantStatus   int
		wantContains string
	}{
		{
			name:         "happy path returns raw CallToolResult",
			path:         "/api/v1/tools/atlassian/jira_search",
			body:         `{"jql":"project = ICC","limit":5}`,
			callResult:   successResult,
			wantStatus:   http.StatusOK,
			wantContains: "ICC-1",
		},
		{
			name:         "non-allowlisted tool is forbidden",
			path:         "/api/v1/tools/atlassian/jira_create_issue",
			body:         `{"summary":"nope"}`,
			callResult:   successResult,
			wantStatus:   http.StatusForbidden,
			wantContains: "not allowlisted",
		},
		{
			name:         "unknown server is not found",
			path:         "/api/v1/tools/nonexistent/jira_search",
			body:         `{}`,
			callResult:   successResult,
			wantStatus:   http.StatusNotFound,
			wantContains: "unknown server",
		},
		{
			name:         "local-only server is not found",
			path:         "/api/v1/tools/local-fs/read_file",
			body:         `{"path":"/etc/hosts"}`,
			callResult:   successResult,
			wantStatus:   http.StatusNotFound,
			wantContains: "unknown server",
		},
		{
			name:         "isError result maps to bad gateway",
			path:         "/api/v1/tools/atlassian/jira_search",
			body:         `{"jql":"broken (("}`,
			callResult:   errorResult,
			wantStatus:   http.StatusBadGateway,
			wantContains: "jql parse failure",
		},
		{
			name:         "unreachable backend maps to bad gateway",
			path:         "/api/v1/tools/atlassian/jira_search",
			body:         `{"jql":"project = ICC"}`,
			callResult:   nil,
			wantStatus:   http.StatusBadGateway,
			wantContains: "tool call failed",
		},
		{
			name:         "invalid body is bad request",
			path:         "/api/v1/tools/atlassian/jira_search",
			body:         `["not","an","object"]`,
			callResult:   successResult,
			wantStatus:   http.StatusBadRequest,
			wantContains: "JSON object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var backendURL string
			if tt.callResult != nil {
				backendURL = startFakeToolBackend(t, tt.callResult).wsURL()
			} else {
				backendURL = deadBackendWSURL(t)
			}

			hub := NewHub()
			hub.Registry = toolCallTestRegistry(backendURL)
			hub.ToolCallTimeout = 5 * time.Second

			srv := newToolCallServer(t, hub)
			resp, body := postToolCall(t, srv.URL, tt.path, tt.body)

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, resp.StatusCode, body)
			}
			if tt.wantContains != "" && !strings.Contains(body, tt.wantContains) {
				t.Errorf("expected body to contain %q, got %s", tt.wantContains, body)
			}

			if tt.wantStatus == http.StatusOK {
				var result struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				}
				if err := json.Unmarshal([]byte(body), &result); err != nil {
					t.Fatalf("failed to parse CallToolResult: %v", err)
				}
				if result.IsError {
					t.Error("expected isError=false on 200 response")
				}
				if len(result.Content) == 0 || result.Content[0].Type != "text" {
					t.Fatalf("expected text content, got %+v", result.Content)
				}
			}
		})
	}
}

func TestToolCallHandler_NoRegistryIsNotFound(t *testing.T) {
	hub := NewHub()
	hub.ToolCallTimeout = time.Second

	srv := newToolCallServer(t, hub)
	resp, body := postToolCall(t, srv.URL, "/api/v1/tools/atlassian/jira_search", `{}`)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404 without a registry, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "no registry") {
		t.Errorf("expected body to mention missing registry, got %s", body)
	}
}

func TestToolCallHandler_ForwardsArguments(t *testing.T) {
	fb := startFakeToolBackend(t, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
		"isError": false,
	})

	hub := NewHub()
	hub.Registry = toolCallTestRegistry(fb.wsURL())
	hub.ToolCallTimeout = 5 * time.Second

	srv := newToolCallServer(t, hub)
	body := `{"jql":"project = ICC ORDER BY updated DESC","limit":50}`
	resp, respBody := postToolCall(t, srv.URL, "/api/v1/tools/atlassian/jira_search", body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, respBody)
	}

	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.lastTool != "jira_search" {
		t.Errorf("expected backend to receive tool 'jira_search', got %q", fb.lastTool)
	}
	if fb.lastArgs["jql"] != "project = ICC ORDER BY updated DESC" {
		t.Errorf("expected jql argument forwarded, got %v", fb.lastArgs)
	}
	if fb.lastArgs["limit"] != float64(50) {
		t.Errorf("expected limit argument forwarded, got %v", fb.lastArgs)
	}
}

func TestToolCallHandler_EmptyBodyMeansNoArguments(t *testing.T) {
	fb := startFakeToolBackend(t, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
		"isError": false,
	})

	hub := NewHub()
	hub.Registry = toolCallTestRegistry(fb.wsURL())
	hub.ToolCallTimeout = 5 * time.Second

	srv := newToolCallServer(t, hub)
	resp, respBody := postToolCall(t, srv.URL, "/api/v1/tools/atlassian/confluence_search", "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, respBody)
	}

	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.lastTool != "confluence_search" {
		t.Errorf("expected backend to receive tool 'confluence_search', got %q", fb.lastTool)
	}
	if len(fb.lastArgs) != 0 {
		t.Errorf("expected empty arguments, got %v", fb.lastArgs)
	}
}

func TestToolCallHandler_SpecLevelAlwaysAllow(t *testing.T) {
	// Client-config registry shape: always_allow inside the common spec
	// rather than at the server level. Both shapes must gate identically.
	fb := startFakeToolBackend(t, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
		"isError": false,
	})

	hub := NewHub()
	hub.Registry = &registry.Registry{
		Version: 1,
		Servers: []*registry.Server{{
			Name: "tavily",
			URL:  fb.wsURL(),
			Common: &registry.TargetSpec{
				AlwaysAllow: []string{"search"},
			},
		}},
	}
	hub.ToolCallTimeout = 5 * time.Second

	srv := newToolCallServer(t, hub)

	resp, respBody := postToolCall(t, srv.URL, "/api/v1/tools/tavily/search", `{"query":"mcp"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for spec-level always_allow, got %d: %s", resp.StatusCode, respBody)
	}

	resp, respBody = postToolCall(t, srv.URL, "/api/v1/tools/tavily/crawl", `{}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected status 403 for non-allowlisted tool, got %d: %s", resp.StatusCode, respBody)
	}
}

func TestToolCallHandler_UnknownStaticToolIsNotFound(t *testing.T) {
	hub := NewHub()
	hub.Registry = &registry.Registry{
		Version: 1,
		Servers: []*registry.Server{{
			Name:        "atlassian",
			URL:         "ws://127.0.0.1:1/ws",
			AlwaysAllow: []string{"jira_search"},
			Common: &registry.TargetSpec{
				Tools: []registry.ToolSchema{
					{Name: "jira_search"},
					{Name: "jira_create_issue"},
				},
			},
		}},
	}
	hub.ToolCallTimeout = time.Second

	srv := newToolCallServer(t, hub)

	// Not in static tools and not allowlisted: unknown tool.
	resp, respBody := postToolCall(t, srv.URL, "/api/v1/tools/atlassian/no_such_tool", `{}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404 for unknown tool, got %d: %s", resp.StatusCode, respBody)
	}

	// In static tools but not allowlisted: forbidden.
	resp, respBody = postToolCall(t, srv.URL, "/api/v1/tools/atlassian/jira_create_issue", `{}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected status 403 for non-allowlisted known tool, got %d: %s", resp.StatusCode, respBody)
	}
}

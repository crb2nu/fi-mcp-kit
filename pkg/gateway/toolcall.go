package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/libs/mcp-go"
)

const (
	// DefaultToolCallTimeout bounds a single REST tool invocation
	// (dial + initialize handshake + tools/call). Override with the
	// FI_MCP_TOOL_CALL_TIMEOUT environment variable or Hub.ToolCallTimeout.
	DefaultToolCallTimeout = 30 * time.Second

	// maxToolRequestBody caps the tool-arguments payload size.
	maxToolRequestBody = 1 << 20 // 1 MiB

	defaultToolCallProfile = "common"

	// JSON-RPC ids used for the one-shot call sequence.
	toolCallInitializeID = 1
	toolCallRequestID    = 2
)

// ToolCallHandler implements POST /api/v1/tools/{server}/{tool}.
//
// The route is intentionally keyless (it does not consult hub.Authenticator)
// so in-cluster callers can use it without credentials. The server-side
// guarantee is the registry always_allow allowlist: only tools listed there
// for the target server may be invoked, which keeps this route restricted to
// whatever the registry deems safe (read-only tools).
//
// Contract (mirrors services/fi-mcp-gateway MR !7):
//   - 200: success; body is the raw MCP CallToolResult JSON
//   - 400: request body is not a JSON object
//   - 403: tool not in the server's registry always_allow
//   - 404: no registry loaded, unknown or local-only server, or unknown tool
//     (when static tool schemas are declared for the server)
//   - 502: backend unreachable, backend JSON-RPC error, or isError result
func ToolCallHandler(hub *Hub, w http.ResponseWriter, r *http.Request) {
	serverName := strings.TrimSpace(r.PathValue("server"))
	toolName := strings.TrimSpace(r.PathValue("tool"))
	if serverName == "" || toolName == "" {
		writeToolCallError(w, http.StatusNotFound, "unknown server or tool")
		return
	}

	reg := hub.Registry
	if reg == nil {
		writeToolCallError(w, http.StatusNotFound, "no registry loaded")
		return
	}
	srv := reg.GetServer(serverName)
	if srv == nil || srv.IsLocalOnly() {
		writeToolCallError(w, http.StatusNotFound, fmt.Sprintf("unknown server: %s", serverName))
		return
	}

	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	if profile == "" {
		profile = defaultToolCallProfile
	}

	if !reg.IsToolAlwaysAllowed(serverName, profile, toolName) {
		// When the registry declares static tool schemas for this server we
		// can distinguish an unknown tool from a known-but-not-allowlisted one.
		if spec, err := reg.GetServerSpec(serverName, profile); err == nil && spec != nil &&
			len(spec.Tools) > 0 && !specHasTool(spec.Tools, toolName) {
			writeToolCallError(w, http.StatusNotFound, fmt.Sprintf("unknown tool: %s", toolName))
			return
		}
		writeToolCallError(w, http.StatusForbidden, fmt.Sprintf("tool not allowlisted: %s/%s", serverName, toolName))
		return
	}

	args, err := readToolCallArguments(r)
	if err != nil {
		writeToolCallError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), hub.toolCallTimeout())
	defer cancel()

	raw, err := hub.CallTool(ctx, serverName, toolName, args)
	if err != nil {
		writeToolCallError(w, http.StatusBadGateway, fmt.Sprintf("tool call failed: %v", err))
		return
	}

	var result mcp.CallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeToolCallError(w, http.StatusBadGateway, fmt.Sprintf("invalid backend result: %v", err))
		return
	}
	if result.IsError {
		writeToolCallError(w, http.StatusBadGateway, "tool returned error: "+toolErrorText(result.Content))
		return
	}

	// REST consumers json-parse text payloads; rewrite TOON-encoded
	// structured text to JSON (see normalizeToolResultText).
	raw = normalizeToolResultText(raw)

	// Return the CallToolResult as the backend produced it (modulo TOON
	// text normalization above).
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// CallTool performs a one-shot MCP tools/call against a backend server:
// dial, initialize handshake, tools/call, close. Backend resolution reuses
// the same path as the WebSocket proxy (registry URL first, template
// fallback). Context cancellation closes the connection, which unblocks any
// pending read.
func (h *Hub) CallTool(ctx context.Context, serverName, toolName string, arguments json.RawMessage) (json.RawMessage, error) {
	backendURL, err := h.backendURL(serverName)
	if err != nil {
		return nil, fmt.Errorf("resolve backend for %q: %w", serverName, err)
	}

	dialer := h.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	conn, resp, err := dialer.DialContext(ctx, backendURL, nil) //nolint:bodyclose // gorilla manages the handshake resp.Body on success; it is closed on the error path below
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("dial backend %q: %w", serverName, err)
	}
	defer conn.Close()

	// Close the connection when ctx is cancelled so blocked reads return.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	if err := toolCallInitialize(ctx, conn); err != nil {
		return nil, err
	}

	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}{Name: toolName, Arguments: arguments}

	callReq, err := mcp.NewRequest(toolCallRequestID, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("build tools/call request: %w", err)
	}
	if err := writeToolCallMessage(conn, callReq); err != nil {
		return nil, fmt.Errorf("send tools/call: %w", err)
	}

	callResp, err := readToolCallResponse(ctx, conn, toolCallRequestID)
	if err != nil {
		return nil, fmt.Errorf("recv tools/call: %w", err)
	}
	if callResp.Error != nil {
		return nil, fmt.Errorf("backend error: %s", callResp.Error.Message)
	}
	return callResp.Result, nil
}

// toolCallInitialize runs the MCP initialize handshake on a fresh connection.
func toolCallInitialize(ctx context.Context, conn *websocket.Conn) error {
	initReq, err := mcp.NewRequest(toolCallInitializeID, "initialize", mcp.InitializeParams{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities:    mcp.Capabilities{},
		ClientInfo:      mcp.ClientInfo{Name: "fi-mcp-gateway-rest", Version: "1.0.0"},
	})
	if err != nil {
		return fmt.Errorf("build initialize request: %w", err)
	}
	if err := writeToolCallMessage(conn, initReq); err != nil {
		return fmt.Errorf("send initialize: %w", err)
	}

	resp, err := readToolCallResponse(ctx, conn, toolCallInitializeID)
	if err != nil {
		return fmt.Errorf("recv initialize: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}

	initialized := &mcp.Message{JSONRPC: mcp.JSONRPCVersion, Method: "notifications/initialized"}
	if err := writeToolCallMessage(conn, initialized); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}
	return nil
}

func writeToolCallMessage(conn *websocket.Conn, msg *mcp.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// readToolCallResponse reads messages until it sees the response matching
// wantID, skipping notifications and unrelated server-initiated messages.
func readToolCallResponse(ctx context.Context, conn *websocket.Conn, wantID int) (*mcp.Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		var msg mcp.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("unmarshal backend message: %w", err)
		}
		if msg.IsResponse() && toolCallIDMatches(msg.ID, wantID) {
			return &msg, nil
		}
	}
}

// toolCallIDMatches compares a decoded JSON-RPC id against an expected
// integer id.
func toolCallIDMatches(id any, want int) bool {
	switch v := id.(type) {
	case float64:
		return int(v) == want
	case int:
		return v == want
	case int64:
		return int(v) == want
	case json.Number:
		n, err := v.Int64()
		return err == nil && int(n) == want
	case string:
		return v == strconv.Itoa(want)
	default:
		return false
	}
}

// readToolCallArguments reads and validates the request body as a JSON
// object of tool arguments. An empty body means no arguments.
func readToolCallArguments(r *http.Request) (json.RawMessage, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxToolRequestBody+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(body) > maxToolRequestBody {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxToolRequestBody)
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}

	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, fmt.Errorf("request body must be a JSON object of tool arguments")
	}
	return json.RawMessage(trimmed), nil
}

// toolErrorText extracts human-readable error text from an isError
// CallToolResult.
func toolErrorText(content []mcp.Content) string {
	var parts []string
	for _, c := range content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimSpace(c.Text))
		}
	}
	if len(parts) == 0 {
		return "tool reported an error"
	}
	return strings.Join(parts, "; ")
}

func specHasTool(tools []registry.ToolSchema, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func writeToolCallError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

// toolCallTimeout returns the per-invocation timeout: Hub.ToolCallTimeout if
// set, else FI_MCP_TOOL_CALL_TIMEOUT, else DefaultToolCallTimeout.
func (h *Hub) toolCallTimeout() time.Duration {
	if h.ToolCallTimeout > 0 {
		return h.ToolCallTimeout
	}
	v := strings.TrimSpace(os.Getenv("FI_MCP_TOOL_CALL_TIMEOUT"))
	if v == "" {
		return DefaultToolCallTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return DefaultToolCallTimeout
	}
	return d
}

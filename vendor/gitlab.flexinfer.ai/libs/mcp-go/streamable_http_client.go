package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// StreamableHTTPClientConfig configures the client-side Streamable HTTP transport.
type StreamableHTTPClientConfig struct {
	// Endpoint is the URL of the MCP Streamable HTTP server (e.g., "https://host:8088/mcp").
	Endpoint string

	// Headers are additional HTTP headers sent with every request (e.g., Authorization).
	Headers map[string]string

	// HTTPClient is an optional custom HTTP client. If nil, http.DefaultClient is used.
	HTTPClient *http.Client
}

// StreamableHTTPTransport implements the Transport interface for Streamable HTTP clients.
type StreamableHTTPTransport struct {
	endpoint  string
	sessionID string
	headers   map[string]string
	incoming  chan *Message
	client    *http.Client
	closed    bool
	mu        sync.Mutex
}

// NewStreamableHTTPTransport creates a new client-side Streamable HTTP transport.
func NewStreamableHTTPTransport(cfg StreamableHTTPClientConfig) *StreamableHTTPTransport {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &StreamableHTTPTransport{
		endpoint: cfg.Endpoint,
		headers:  cfg.Headers,
		incoming: make(chan *Message, 16),
		client:   client,
	}
}

// Send sends a JSON-RPC message to the server via HTTP POST.
// For requests (messages with ID), the response is pushed to the incoming channel.
// For notifications (no ID), only the HTTP status is checked.
func (t *StreamableHTTPTransport) Send(ctx context.Context, msg *Message) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("transport closed")
	}
	t.mu.Unlock()

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	// Set session ID if we have one
	t.mu.Lock()
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.mu.Unlock()

	// Set custom headers
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Capture session ID from initialize response
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	// Notifications get 202 Accepted — no body to read
	if resp.StatusCode == http.StatusAccepted {
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	// Read the response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if len(respBody) == 0 {
		return nil
	}

	var respMsg Message
	if err := json.Unmarshal(respBody, &respMsg); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	// Push response to incoming channel for Recv()
	select {
	case t.incoming <- &respMsg:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// Recv receives the next response message from the server.
func (t *StreamableHTTPTransport) Recv(ctx context.Context) (*Message, error) {
	select {
	case msg, ok := <-t.incoming:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes the transport and sends a DELETE to terminate the session.
func (t *StreamableHTTPTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	sessionID := t.sessionID
	t.mu.Unlock()

	close(t.incoming)

	// Send DELETE to terminate session
	if sessionID != "" {
		req, err := http.NewRequest(http.MethodDelete, t.endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Mcp-Session-Id", sessionID)
		for k, v := range t.headers {
			req.Header.Set(k, v)
		}
		resp, err := t.client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
	}

	return nil
}

// SessionID returns the current session ID, if any.
func (t *StreamableHTTPTransport) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

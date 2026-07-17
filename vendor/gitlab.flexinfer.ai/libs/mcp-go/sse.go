package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/google/uuid"
)

// SSEServer manages SSE sessions for an MCP server.
// Deprecated: Use StreamableHTTPServer for new implementations.
// See https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#streamable-http
type SSEServer struct {
	server *Server
	mu     sync.RWMutex
	// sessions maps sessionID to the transport
	sessions map[string]*SSETransport
}

// NewSSEServer creates a new SSE server helper.
func NewSSEServer(server *Server) *SSEServer {
	return &SSEServer{
		server:   server,
		sessions: make(map[string]*SSETransport),
	}
}

// HandleSSE handles the GET /sse endpoint.
func (s *SSEServer) HandleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	sessionID := uuid.New().String()
	transport := newSSETransport(w, r, sessionID)

	s.mu.Lock()
	s.sessions[sessionID] = transport
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()
		_ = transport.Close()
	}()

	// Headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send endpoint event
	// The endpoint URL is relative or absolute. We assume a query param or separate endpoint.
	// For simplicity, we assume the POST endpoint is at the same path (or caller configures it).
	// But standard practice is often `?session_id=` handling on the same path or a specific /message path.
	// We'll send the session ID and let the client construct the URL or we send a relative URL.
	//
	// Spec says: `event: endpoint`, `data: URI`
	// We'll assume the client knows where to POST or we provide a default relative path `?session_id=...`
	// if the handler serves both.

	// Determine the POST endpoint.
	// For now, we send `?session_id=<uuid>` assuming the client posts to the same URL or similar.
	// Actually, let's just send the session ID.
	endpoint := fmt.Sprintf("?session_id=%s", sessionID)
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint)
	flusher.Flush()

	// Start the server for this transport in a separate goroutine?
	// No, ServeTransport blocks. We can call it here.
	// But ServeTransport reads from transport.Recv().
	// SSETransport.Recv() waits for messages from HandleMessage.

	ctx := r.Context()
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.server.ServeTransport(ctx, transport)
	}()

	// Wait for context done or server error
	select {
	case <-ctx.Done():
		// Client disconnected
	case err := <-errCh:
		if err != nil {
			s.server.logger.Error("SSE session ended with error", "err", err)
		}
	}
}

// HandleMessage handles the POST /message endpoint (or same endpoint with POST).
func (s *SSEServer) HandleMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "Missing session_id", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	transport, ok := s.sessions[sessionID]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusInternalServerError)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Push to transport
	select {
	case transport.incoming <- &msg:
		w.WriteHeader(http.StatusAccepted)
	case <-transport.ctx.Done():
		http.Error(w, "Session closed", http.StatusGone)
	default:
		// Buffer full?
		http.Error(w, "Server busy", http.StatusServiceUnavailable)
	}
}

type SSETransport struct {
	w         io.Writer
	mu        sync.Mutex
	incoming  chan *Message
	ctx       context.Context
	sessionID string
	flusher   http.Flusher
}

func newSSETransport(w http.ResponseWriter, r *http.Request, sessionID string) *SSETransport {
	return &SSETransport{
		w:         w,
		incoming:  make(chan *Message, 10), // Buffer
		ctx:       r.Context(),
		sessionID: sessionID,
		flusher:   w.(http.Flusher),
	}
}

func (t *SSETransport) Send(ctx context.Context, msg *Message) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	// SSE format: `event: message\ndata: JSON\n\n`
	if _, err := fmt.Fprintf(t.w, "event: message\ndata: %s\n\n", data); err != nil {
		return err
	}
	t.flusher.Flush()
	return nil
}

func (t *SSETransport) Recv(ctx context.Context) (*Message, error) {
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

func (t *SSETransport) Close() error {
	// We can't strictly close the writer as it's controlled by the HTTP handler,
	// but we can close the channel.
	// Note: Closing channel might panic if multiple writers, but here only one Handler writes.
	return nil
}

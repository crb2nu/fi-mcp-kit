package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// ProtocolVersion20250618 is the MCP protocol version for Streamable HTTP.
const ProtocolVersion20250618 = "2025-06-18"

// MessageHandler processes an MCP message and returns a response.
// Used by StreamableHTTPServer to bridge HTTP requests to the daemon's message dispatch.
type MessageHandler func(ctx context.Context, msg *Message) (*Message, error)

// DefaultMaxRequestBodySize is the default maximum request body size (10 MB).
const DefaultMaxRequestBodySize int64 = 10 << 20

// StreamableHTTPConfig configures the Streamable HTTP server.
type StreamableHTTPConfig struct {
	// AllowedOrigins restricts which origins can connect (DNS rebinding protection).
	// Empty slice allows all origins.
	AllowedOrigins []string

	// SessionRequired requires Mcp-Session-Id header after initialization.
	SessionRequired bool

	// SessionTimeout is how long idle sessions are kept before expiry.
	SessionTimeout time.Duration

	// MaxSessions limits the number of concurrent sessions (0 = unlimited).
	MaxSessions int

	// MaxRequestBodySize limits the size of incoming request bodies in bytes.
	// Requests exceeding this limit receive HTTP 413 (Request Entity Too Large).
	// Defaults to DefaultMaxRequestBodySize (10 MB). Set to -1 to disable the limit.
	MaxRequestBodySize int64

	// SSEEnabled enables the GET SSE endpoint for server-initiated messages.
	// When false (default), GET returns 405.
	SSEEnabled bool

	// SSEKeepaliveInterval is the interval for SSE keepalive comments.
	// Defaults to 30 seconds.
	SSEKeepaliveInterval time.Duration

	// NotificationBufferSize is the per-session notification buffer size.
	// Defaults to 64.
	NotificationBufferSize int
}

// DefaultStreamableHTTPConfig returns sensible defaults.
func DefaultStreamableHTTPConfig() StreamableHTTPConfig {
	return StreamableHTTPConfig{
		SessionRequired:        true,
		SessionTimeout:         30 * time.Minute,
		MaxSessions:            1000,
		MaxRequestBodySize:     DefaultMaxRequestBodySize,
		SSEKeepaliveInterval:   30 * time.Second,
		NotificationBufferSize: 64,
	}
}

// StreamableHTTPServer handles MCP Streamable HTTP transport.
// It implements http.Handler and dispatches JSON-RPC messages to a MessageHandler.
type StreamableHTTPServer struct {
	handler  MessageHandler
	sessions sync.Map // sessionID → *httpSession
	config   StreamableHTTPConfig
	logger   func(msg string, args ...any) // optional structured logger
}

type httpSession struct {
	id          string
	createdAt   time.Time
	lastAccess  time.Time
	initialized bool
	mu          sync.Mutex

	// SSE fields for server-initiated messages.
	notifCh   chan *Message // buffered channel for server-initiated messages
	sseActive atomic.Bool   // true when a GET SSE client is connected
	closeOnce sync.Once     // guard for closing notifCh
}

// NewStreamableHTTPServer creates a new Streamable HTTP server.
func NewStreamableHTTPServer(handler MessageHandler, cfg StreamableHTTPConfig) *StreamableHTTPServer {
	if cfg.SessionTimeout == 0 {
		cfg.SessionTimeout = 30 * time.Minute
	}
	if cfg.SSEKeepaliveInterval == 0 {
		cfg.SSEKeepaliveInterval = 30 * time.Second
	}
	if cfg.NotificationBufferSize <= 0 {
		cfg.NotificationBufferSize = 64
	}
	return &StreamableHTTPServer{
		handler: handler,
		config:  cfg,
	}
}

// SetLogger sets an optional structured logger.
func (s *StreamableHTTPServer) SetLogger(fn func(msg string, args ...any)) {
	s.logger = fn
}

func (s *StreamableHTTPServer) log(msg string, args ...any) {
	if s.logger != nil {
		s.logger(msg, args...)
	}
}

// ServeHTTP implements http.Handler.
func (s *StreamableHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handlePOST(w, r)
	case http.MethodGet:
		s.handleGET(w, r)
	case http.MethodDelete:
		s.handleDELETE(w, r)
	case http.MethodOptions:
		s.handleOPTIONS(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *StreamableHTTPServer) handlePOST(w http.ResponseWriter, r *http.Request) {
	// Validate Content-Type
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	// Validate Accept header
	accept := r.Header.Get("Accept")
	if accept == "" {
		accept = "application/json"
	}
	if !strings.Contains(accept, "application/json") && !strings.Contains(accept, "*/*") {
		http.Error(w, "Accept must include application/json", http.StatusNotAcceptable)
		return
	}

	// Validate Origin (DNS rebinding protection)
	if !s.validateOrigin(r) {
		http.Error(w, "Origin not allowed", http.StatusForbidden)
		return
	}

	// Enforce request body size limit.
	if s.config.MaxRequestBodySize >= 0 {
		r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxRequestBodySize)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if isMaxBytesError(err) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	if len(body) == 0 {
		http.Error(w, "Empty request body", http.StatusBadRequest)
		return
	}

	// Parse message
	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		resp := NewErrorResponse(nil, ParseError, "Invalid JSON: "+err.Error())
		s.writeJSON(w, http.StatusOK, resp)
		return
	}

	// Session validation for non-initialize requests
	sessionID := r.Header.Get("Mcp-Session-Id")

	if msg.Method == "initialize" {
		// Initialize requests must not have a session ID
		resp, err := s.handler(r.Context(), &msg)
		if err != nil {
			s.writeJSON(w, http.StatusOK, NewErrorResponse(msg.ID, InternalError, err.Error()))
			return
		}

		// Create session
		bufSize := s.config.NotificationBufferSize
		if bufSize <= 0 {
			bufSize = 64
		}
		session := &httpSession{
			id:          uuid.New().String(),
			createdAt:   time.Now(),
			lastAccess:  time.Now(),
			initialized: true,
			notifCh:     make(chan *Message, bufSize),
		}

		// Enforce max sessions
		if s.config.MaxSessions > 0 {
			count := 0
			s.sessions.Range(func(_, _ any) bool {
				count++
				return count < s.config.MaxSessions
			})
			if count >= s.config.MaxSessions {
				s.writeJSON(w, http.StatusOK, NewErrorResponse(msg.ID, InternalError, "too many sessions"))
				return
			}
		}

		s.sessions.Store(session.id, session)
		s.log("session created", "session_id", session.id)

		w.Header().Set("Mcp-Session-Id", session.id)
		s.writeJSON(w, http.StatusOK, resp)
		return
	}

	// Non-initialize: validate session if required
	if s.config.SessionRequired {
		if sessionID == "" {
			http.Error(w, "Mcp-Session-Id header required", http.StatusBadRequest)
			return
		}
		val, ok := s.sessions.Load(sessionID)
		if !ok {
			http.Error(w, "Session not found or expired", http.StatusNotFound)
			return
		}
		session := val.(*httpSession)
		session.mu.Lock()
		session.lastAccess = time.Now()
		session.mu.Unlock()
	}

	// Handle notifications (no ID, no response expected)
	if msg.IsNotification() {
		_, _ = s.handler(r.Context(), &msg)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Handle requests
	resp, err := s.handler(r.Context(), &msg)
	if err != nil {
		s.writeJSON(w, http.StatusOK, NewErrorResponse(msg.ID, InternalError, err.Error()))
		return
	}

	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	s.writeJSON(w, http.StatusOK, resp)
}

func (s *StreamableHTTPServer) handleDELETE(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "Mcp-Session-Id header required", http.StatusBadRequest)
		return
	}

	val, ok := s.sessions.LoadAndDelete(sessionID)
	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	session := val.(*httpSession)
	session.closeOnce.Do(func() {
		close(session.notifCh)
	})

	s.log("session deleted", "session_id", sessionID)
	w.WriteHeader(http.StatusOK)
}

func (s *StreamableHTTPServer) handleOPTIONS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST, GET, DELETE, OPTIONS")
	w.WriteHeader(http.StatusNoContent)
}

func (s *StreamableHTTPServer) validateOrigin(r *http.Request) bool {
	if len(s.config.AllowedOrigins) == 0 {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No origin header (e.g., server-to-server) — allow
		return true
	}
	for _, allowed := range s.config.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func (s *StreamableHTTPServer) writeJSON(w http.ResponseWriter, status int, msg *Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// ReapExpiredSessions removes sessions that have been idle longer than the timeout.
// Call this periodically from a background goroutine.
func (s *StreamableHTTPServer) ReapExpiredSessions() int {
	reaped := 0
	now := time.Now()
	s.sessions.Range(func(key, value any) bool {
		session := value.(*httpSession)
		session.mu.Lock()
		idle := now.Sub(session.lastAccess)
		session.mu.Unlock()
		if idle > s.config.SessionTimeout {
			s.sessions.Delete(key)
			session.closeOnce.Do(func() {
				close(session.notifCh)
			})
			s.log("session expired", "session_id", session.id, "idle", idle)
			reaped++
		}
		return true
	})
	return reaped
}

// SessionCount returns the number of active sessions.
func (s *StreamableHTTPServer) SessionCount() int {
	count := 0
	s.sessions.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// GetSession returns session info for a given session ID.
func (s *StreamableHTTPServer) GetSession(sessionID string) (createdAt, lastAccess time.Time, ok bool) {
	val, found := s.sessions.Load(sessionID)
	if !found {
		return time.Time{}, time.Time{}, false
	}
	session := val.(*httpSession)
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.createdAt, session.lastAccess, true
}

func (s *StreamableHTTPServer) handleGET(w http.ResponseWriter, r *http.Request) {
	if !s.config.SSEEnabled {
		http.Error(w, "GET not supported (SSE not enabled)", http.StatusMethodNotAllowed)
		return
	}

	// Validate Accept header.
	accept := r.Header.Get("Accept")
	if !strings.Contains(accept, "text/event-stream") && !strings.Contains(accept, "*/*") {
		http.Error(w, "Accept must include text/event-stream", http.StatusNotAcceptable)
		return
	}

	// Validate Origin (DNS rebinding protection).
	if !s.validateOrigin(r) {
		http.Error(w, "Origin not allowed", http.StatusForbidden)
		return
	}

	// Validate session.
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "Mcp-Session-Id header required", http.StatusBadRequest)
		return
	}
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	session := val.(*httpSession)

	// Enforce single SSE stream per session via atomic CAS.
	if !session.sseActive.CompareAndSwap(false, true) {
		http.Error(w, "SSE stream already active for this session", http.StatusConflict)
		return
	}
	defer session.sseActive.Store(false)

	// Update last access.
	session.mu.Lock()
	session.lastAccess = time.Now()
	session.mu.Unlock()

	// Verify streaming support.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	s.log("SSE stream opened", "session_id", sessionID)

	ticker := time.NewTicker(s.config.SSEKeepaliveInterval)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			s.log("SSE stream closed (client disconnected)", "session_id", sessionID)
			return
		case msg, open := <-session.notifCh:
			if !open {
				s.log("SSE stream closed (session removed)", "session_id", sessionID)
				return
			}
			if err := s.writeSSEEvent(w, flusher, msg); err != nil {
				s.log("SSE write error", "session_id", sessionID, "error", err)
				return
			}
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				s.log("SSE keepalive write error", "session_id", sessionID, "error", err)
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEEvent marshals a Message as JSON and writes it as an SSE event.
func (s *StreamableHTTPServer) writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal SSE event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", data); err != nil {
		return fmt.Errorf("write SSE event: %w", err)
	}
	flusher.Flush()
	return nil
}

// SendNotification queues a notification for delivery to a session's SSE stream.
// Non-blocking: returns error if session not found or buffer full.
func (s *StreamableHTTPServer) SendNotification(sessionID string, msg *Message) error {
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}
	session := val.(*httpSession)
	select {
	case session.notifCh <- msg:
		return nil
	default:
		return fmt.Errorf("notification buffer full for session %q", sessionID)
	}
}

// BroadcastNotification sends a notification to all active sessions.
// Drops notifications for sessions with full buffers.
func (s *StreamableHTTPServer) BroadcastNotification(msg *Message) {
	s.sessions.Range(func(_, value any) bool {
		session := value.(*httpSession)
		select {
		case session.notifCh <- msg:
		default:
			s.log("notification dropped (buffer full)", "session_id", session.id)
		}
		return true
	})
}

// isMaxBytesError reports whether err was caused by http.MaxBytesReader.
func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

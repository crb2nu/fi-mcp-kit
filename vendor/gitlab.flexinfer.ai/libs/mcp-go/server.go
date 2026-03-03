package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// ToolHandler is a function that handles a tool call.
type ToolHandler func(ctx context.Context, args map[string]any) (*CallToolResult, error)

// Server is an MCP server that handles tool calls.
type Server struct {
	name         string
	version      string
	instructions string
	tools        []Tool
	handlers     map[string]ToolHandler
	transport    Transport
	logger       *slog.Logger
	validator    *Validator

	// Concurrency control
	concurrencyLimit int
	semaphore        chan struct{}
	wg               sync.WaitGroup
}

// NewServer creates a new MCP server.
func NewServer(name, version string) *Server {
	return &Server{
		name:             name,
		version:          version,
		tools:            []Tool{},
		handlers:         make(map[string]ToolHandler),
		logger:           slog.Default(),
		validator:        NewValidator(true),
		concurrencyLimit: 1, // Default to sequential for backward compatibility
	}
}

// SetLogger sets the server logger.
func (s *Server) SetLogger(logger *slog.Logger) {
	s.logger = logger
}

// SetInstructions sets the server instructions.
func (s *Server) SetInstructions(instructions string) {
	s.instructions = instructions
}

// SetConcurrencyLimit sets the maximum number of concurrent requests.
// A limit of 1 (default) means sequential handling.
// A limit <= 0 means unlimited concurrency (one goroutine per request).
func (s *Server) SetConcurrencyLimit(limit int) {
	s.concurrencyLimit = limit
	if limit > 0 {
		s.semaphore = make(chan struct{}, limit)
	} else {
		s.semaphore = nil
	}
}

// AddTool registers a tool with the server.
func (s *Server) AddTool(tool Tool, handler ToolHandler) {
	s.tools = append(s.tools, tool)
	s.handlers[tool.Name] = handler
}

// Tools returns the list of registered tools.
func (s *Server) Tools() []Tool {
	return s.tools
}

// Run starts the server on stdio.
func (s *Server) Run(ctx context.Context) error {
	return s.RunWithIO(ctx, os.Stdin, os.Stdout)
}

// RunWithIO starts the server using a stdio transport backed by the provided reader/writer.
func (s *Server) RunWithIO(ctx context.Context, r io.Reader, w io.Writer) error {
	return s.RunWithTransport(ctx, NewStdioTransport(r, w))
}

// RunWithTransport starts the server using the provided transport.
func (s *Server) RunWithTransport(ctx context.Context, transport Transport) error {
	s.transport = transport
	return s.ServeTransport(ctx, transport)
}

// ServeTransport serves the MCP protocol over the provided transport.
// It is safe to call this method concurrently on the same Server instance with different transports.
func (s *Server) ServeTransport(ctx context.Context, transport Transport) error {
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return ctx.Err()
		default:
		}

		msg, err := transport.Recv(ctx)
		if err != nil {
			if err != io.EOF {
				s.logger.Error("recv error", "err", err)
			}
			break // Client disconnected
		}

		// Handle notifications synchronously to preserve ordering
		if msg.IsNotification() {
			_, _ = s.handleMessage(ctx, msg)
			continue
		}

		// Handle requests (concurrently if limit > 1 or <= 0)
		if s.concurrencyLimit == 1 {
			// Sequential mode
			s.handleAndSend(ctx, transport, msg)
		} else {
			// Concurrent mode
			if s.semaphore != nil {
				s.semaphore <- struct{}{}
			}
			s.wg.Add(1)
			go func(m *Message) {
				defer s.wg.Done()
				if s.semaphore != nil {
					defer func() { <-s.semaphore }()
				}
				s.handleAndSend(ctx, transport, m)
			}(msg)
		}
	}

	s.wg.Wait()
	return nil
}

func (s *Server) handleAndSend(ctx context.Context, transport Transport, msg *Message) {
	s.logger.Debug("handle message", "method", msg.Method, "id", msg.ID)

	resp, err := s.handleMessage(ctx, msg)
	if err != nil {
		resp = NewErrorResponse(msg.ID, InternalError, err.Error())
	}

	if resp != nil {
		s.logger.Debug("send response", "id", resp.ID, "method", resp.Method)
		if err := transport.Send(ctx, resp); err != nil {
			s.logger.Error("send response error", "err", err, "id", resp.ID)
		}
	}
}

func (s *Server) handleMessage(ctx context.Context, msg *Message) (*Message, error) {
	// Validation Layer
	if msg.IsRequest() {
		if err := s.validator.ValidateRequest(msg); err != nil {
			return NewErrorResponse(msg.ID, InvalidRequest, err.Error()), nil
		}
	} else if msg.Method == "" && msg.ID == nil {
		return nil, fmt.Errorf("empty message")
	}

	switch msg.Method {
	case "initialize":
		return s.handleInitialize(msg)
	case "notifications/initialized":
		return nil, nil // No response for notifications
	case "tools/list":
		return s.handleToolsList(msg)
	case "tools/call":
		return s.handleToolsCall(ctx, msg)
	case "resources/list":
		return s.handleResourcesList(msg)
	case "prompts/list":
		return s.handlePromptsList(msg)
	default:
		// If it's a request, return MethodNotFound. If notification, ignore.
		if msg.ID != nil {
			s.logger.Warn("unknown method", "method", msg.Method, "id", msg.ID)
			return NewErrorResponse(msg.ID, MethodNotFound, fmt.Sprintf("unknown method: %s", msg.Method)), nil
		}
		return nil, nil
	}
}

func (s *Server) handleInitialize(msg *Message) (*Message, error) {
	s.logger.Info("client connected", "client_info", msg.Params)
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: Capabilities{
			Tools: &ToolsCapability{},
		},
		ServerInfo: ServerInfo{
			Name:    s.name,
			Version: s.version,
		},
		Instructions: s.instructions,
	}
	return NewResponse(msg.ID, result)
}

func (s *Server) handleToolsList(msg *Message) (*Message, error) {
	return NewResponse(msg.ID, ToolsListResult{Tools: s.tools})
}

func (s *Server) handleToolsCall(ctx context.Context, msg *Message) (*Message, error) {
	var params CallToolParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return NewErrorResponse(msg.ID, InvalidParams, err.Error()), nil
	}

	s.logger.Info("tool call", "name", params.Name, "id", msg.ID)
	start := time.Now()

	handler, ok := s.handlers[params.Name]
	if !ok {
		s.logger.Warn("unknown tool", "name", params.Name)
		return NewErrorResponse(msg.ID, InvalidParams, fmt.Sprintf("unknown tool: %s", params.Name)), nil
	}

	result, err := handler(ctx, params.Arguments)
	duration := time.Since(start)

	if err != nil {
		s.logger.Error("tool call failed", "name", params.Name, "duration", duration, "error", err)
		return NewResponse(msg.ID, &CallToolResult{
			Content: []Content{{Type: "text", Text: err.Error()}},
			IsError: true,
		})
	}

	s.logger.Info("tool call success", "name", params.Name, "duration", duration)
	return NewResponse(msg.ID, result)
}

func (s *Server) handleResourcesList(msg *Message) (*Message, error) {
	return NewResponse(msg.ID, ResourcesListResult{Resources: []Resource{}})
}

func (s *Server) handlePromptsList(msg *Message) (*Message, error) {
	return NewResponse(msg.ID, PromptsListResult{Prompts: []Prompt{}})
}

// TextResult creates a simple text result.
func TextResult(text string) *CallToolResult {
	return &CallToolResult{
		Content: []Content{{Type: "text", Text: text}},
	}
}

// JSONResult creates a JSON result.
func JSONResult(v any) (*CallToolResult, error) {
	text, err := formatStructuredResult(v)
	if err != nil {
		return nil, err
	}
	return &CallToolResult{
		Content: []Content{{Type: "text", Text: text}},
	}, nil
}

// ErrorResult creates an error result.
func ErrorResult(err error) *CallToolResult {
	return &CallToolResult{
		Content: []Content{{Type: "text", Text: err.Error()}},
		IsError: true,
	}
}

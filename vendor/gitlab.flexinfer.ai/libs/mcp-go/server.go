package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ToolHandler is a function that handles a tool call.
type ToolHandler func(ctx context.Context, args map[string]any) (*CallToolResult, error)

// StreamingToolHandler is a function that handles a tool call with streaming updates.
// It receives a channel for sending incremental updates during execution.
// The handler should send updates to the channel and return the final result.
// Close the channel when done to signal completion.
type StreamingToolHandler func(ctx context.Context, args map[string]any, updates chan<- StreamUpdate) (*CallToolResult, error)

// ProgressReporter allows tools to report progress during execution.
type ProgressReporter interface {
	// ReportProgress sends a progress update to the client.
	ReportProgress(progress, total float64, message string) error
}

// ResourceHandler is a function that handles a resource read.
// It receives the resource URI and returns the resource contents.
type ResourceHandler func(ctx context.Context, uri string) (*ReadResourceResult, error)

// PromptHandler is a function that handles a prompt get request.
// It receives the prompt arguments and returns the prompt messages.
type PromptHandler func(ctx context.Context, args map[string]string) (*GetPromptResult, error)

// SamplingHandler is a function that handles a sampling/createMessage request from the server.
// This is called when the server wants to request an LLM completion from the client.
type SamplingHandler func(ctx context.Context, params CreateMessageParams) (*CreateMessageResult, error)

// SamplingClient allows tools to request LLM completions from the client.
type SamplingClient interface {
	// CreateMessage sends a sampling request to the client and waits for the response.
	CreateMessage(ctx context.Context, params CreateMessageParams) (*CreateMessageResult, error)
}

// Server is an MCP server that handles tool calls.
type Server struct {
	name         string
	version      string
	instructions string
	tools        []Tool
	handlers     map[string]ToolHandler
	streamHdlr   map[string]StreamingToolHandler // streaming handlers
	resources    []Resource
	resourceTmpl []ResourceTemplate
	resourceHdlr map[string]ResourceHandler // keyed by URI or URI template
	resourceSubs map[string]struct{}        // subscribed resource URIs
	resourceSubM sync.RWMutex
	prompts      []Prompt
	promptHdlr   map[string]PromptHandler
	transport    Transport
	logger       *slog.Logger
	validator    *Validator

	// Sampling support
	samplingEnabled        bool
	clientSupportsSampling bool
	pendingRequests        map[string]chan *Message // pending sampling requests by normalized ID
	pendingRequestMu       sync.Mutex
	requestIDCounter       int64

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
		streamHdlr:       make(map[string]StreamingToolHandler),
		resources:        []Resource{},
		resourceTmpl:     []ResourceTemplate{},
		resourceHdlr:     make(map[string]ResourceHandler),
		resourceSubs:     make(map[string]struct{}),
		prompts:          []Prompt{},
		promptHdlr:       make(map[string]PromptHandler),
		pendingRequests:  make(map[string]chan *Message),
		logger:           slog.Default(),
		validator:        NewValidator(true),
		concurrencyLimit: 1, // Default to sequential for backward compatibility
	}
}

// EnableSampling enables the server to request LLM completions from the client.
// This should be called before running the server if you want to use sampling.
func (s *Server) EnableSampling() {
	s.samplingEnabled = true
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

// AddStreamingTool registers a streaming tool with the server.
// Streaming tools can send incremental updates during execution.
func (s *Server) AddStreamingTool(tool Tool, handler StreamingToolHandler) {
	s.tools = append(s.tools, tool)
	s.streamHdlr[tool.Name] = handler
}

// Tools returns the list of registered tools.
func (s *Server) Tools() []Tool {
	return s.tools
}

// AddResource registers a static resource with the server.
// The handler is called when the resource is read.
func (s *Server) AddResource(resource Resource, handler ResourceHandler) {
	s.resources = append(s.resources, resource)
	s.resourceHdlr[resource.URI] = handler
}

// AddResourceTemplate registers a resource template with the server.
// Templates allow dynamic resource URIs using URI template syntax (RFC 6570).
// The handler receives the full URI and should parse template variables.
func (s *Server) AddResourceTemplate(template ResourceTemplate, handler ResourceHandler) {
	s.resourceTmpl = append(s.resourceTmpl, template)
	s.resourceHdlr[template.URITemplate] = handler
}

// Resources returns the list of registered resources.
func (s *Server) Resources() []Resource {
	return s.resources
}

// ResourceTemplates returns the list of registered resource templates.
func (s *Server) ResourceTemplates() []ResourceTemplate {
	return s.resourceTmpl
}

// AddPrompt registers a prompt with the server.
// The handler is called when the prompt is requested with arguments.
func (s *Server) AddPrompt(prompt Prompt, handler PromptHandler) {
	s.prompts = append(s.prompts, prompt)
	s.promptHdlr[prompt.Name] = handler
}

// Prompts returns the list of registered prompts.
func (s *Server) Prompts() []Prompt {
	return s.prompts
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

// ServeStreamableHTTP returns an http.Handler that serves the MCP protocol
// over the Streamable HTTP transport. The server's handleMessage method is used
// as the message handler, enabling reuse of existing tool dispatch logic.
func (s *Server) ServeStreamableHTTP(cfg StreamableHTTPConfig) *StreamableHTTPServer {
	handler := func(ctx context.Context, msg *Message) (*Message, error) {
		return s.handleMessage(ctx, msg)
	}
	srv := NewStreamableHTTPServer(handler, cfg)
	srv.SetLogger(func(msg string, args ...any) {
		s.logger.Info(msg, args...)
	})
	return srv
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
	// Check if this is a response to a pending request (for sampling)
	if msg.IsResponse() {
		s.handlePendingResponse(msg)
		return nil, nil
	}

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
	case "resources/read":
		return s.handleResourcesRead(ctx, msg)
	case "resources/subscribe":
		return s.handleResourcesSubscribe(msg)
	case "resources/unsubscribe":
		return s.handleResourcesUnsubscribe(msg)
	case "prompts/list":
		return s.handlePromptsList(msg)
	case "prompts/get":
		return s.handlePromptsGet(ctx, msg)
	default:
		// If it's a request, return MethodNotFound. If notification, ignore.
		if msg.ID != nil {
			s.logger.Warn("unknown method", "method", msg.Method, "id", msg.ID)
			return NewErrorResponse(msg.ID, MethodNotFound, fmt.Sprintf("unknown method: %s", msg.Method)), nil
		}
		return nil, nil
	}
}

// handlePendingResponse handles responses to requests we sent (e.g., sampling)
func (s *Server) handlePendingResponse(msg *Message) {
	if msg.ID == nil {
		return
	}
	key := normalizeRequestID(msg.ID)

	s.pendingRequestMu.Lock()
	ch, ok := s.pendingRequests[key]
	if ok {
		delete(s.pendingRequests, key)
	}
	s.pendingRequestMu.Unlock()

	if ok {
		ch <- msg
		close(ch)
	}
}

func (s *Server) handleInitialize(msg *Message) (*Message, error) {
	s.logger.Info("client connected", "client_info", msg.Params)

	var params InitializeParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return NewErrorResponse(msg.ID, InvalidParams, err.Error()), nil
	}
	s.clientSupportsSampling = params.Capabilities.Sampling != nil

	caps := Capabilities{}

	if s.samplingEnabled {
		caps.Sampling = &SamplingCapability{}
	}

	// Advertise tools if any are registered
	if len(s.tools) > 0 {
		caps.Tools = &ToolsCapability{}
	}

	// Advertise resources if any are registered
	if len(s.resources) > 0 || len(s.resourceTmpl) > 0 {
		caps.Resources = &ResourcesCapability{
			Subscribe:   true,
			ListChanged: true,
		}
	}

	// Advertise prompts if any are registered
	if len(s.prompts) > 0 {
		caps.Prompts = &PromptsCapability{
			ListChanged: false,
		}
	}

	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    caps,
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
	var params CallToolParamsWithMeta
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return NewErrorResponse(msg.ID, InvalidParams, err.Error()), nil
	}

	s.logger.Info("tool call", "name", params.Name, "id", msg.ID)
	start := time.Now()

	// Check for streaming handler first
	if streamHandler, ok := s.streamHdlr[params.Name]; ok {
		return s.handleStreamingToolCall(ctx, msg, params, streamHandler, start)
	}

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

func (s *Server) handleStreamingToolCall(ctx context.Context, msg *Message, params CallToolParamsWithMeta, handler StreamingToolHandler, start time.Time) (*Message, error) {
	// Create channel for streaming updates
	updates := make(chan StreamUpdate, 10)

	// Extract progress token if provided
	var progressToken any
	if params.Meta != nil && params.Meta.ProgressToken != nil {
		progressToken = params.Meta.ProgressToken
	}

	// Run handler in goroutine
	resultCh := make(chan struct {
		result *CallToolResult
		err    error
	}, 1)

	go func() {
		result, err := handler(ctx, params.Arguments, updates)
		close(updates) // Signal completion
		resultCh <- struct {
			result *CallToolResult
			err    error
		}{result, err}
	}()

	// Process streaming updates
	for update := range updates {
		// Send progress notification if we have a token
		if progressToken != nil && update.Progress > 0 {
			if err := s.sendProgressNotification(ctx, progressToken, update.Progress, 1.0, update.Message); err != nil {
				s.logger.Warn("failed to send progress notification", "error", err)
			}
		}

		// For now, we accumulate updates but don't stream them to the client
		// Full streaming would require a different transport protocol
		// This is a foundation for when we add proper streaming support
	}

	// Wait for final result
	res := <-resultCh
	duration := time.Since(start)

	if res.err != nil {
		s.logger.Error("streaming tool call failed", "name", params.Name, "duration", duration, "error", res.err)
		return NewResponse(msg.ID, &CallToolResult{
			Content: []Content{{Type: "text", Text: res.err.Error()}},
			IsError: true,
		})
	}

	s.logger.Info("streaming tool call success", "name", params.Name, "duration", duration)
	return NewResponse(msg.ID, res.result)
}

// sendProgressNotification sends a progress notification to the client.
func (s *Server) sendProgressNotification(ctx context.Context, progressToken any, progress, total float64, message string) error {
	if s.transport == nil {
		return nil // No transport available (shouldn't happen during a request)
	}

	notification, err := NewRequest(nil, "notifications/progress", ProgressNotification{
		ProgressToken: progressToken,
		Progress:      progress,
		Total:         total,
		Message:       message,
	})
	if err != nil {
		return err
	}

	return s.transport.Send(ctx, notification)
}

// SendLogNotification sends a log notification to the client.
func (s *Server) SendLogNotification(ctx context.Context, level LoggingLevel, logger string, data any) error {
	if s.transport == nil {
		return fmt.Errorf("no transport available")
	}

	notification, err := NewRequest(nil, "notifications/message", LogNotification{
		Level:  level,
		Logger: logger,
		Data:   data,
	})
	if err != nil {
		return err
	}

	return s.transport.Send(ctx, notification)
}

// CreateMessage sends a sampling request to the client and waits for the response.
// This allows the server to request LLM completions from the client.
// The client must have advertised sampling capability during initialization.
func (s *Server) CreateMessage(ctx context.Context, params CreateMessageParams) (*CreateMessageResult, error) {
	if s.transport == nil {
		return nil, fmt.Errorf("no transport available")
	}

	if !s.samplingEnabled {
		return nil, fmt.Errorf("sampling not enabled on server")
	}
	if !s.clientSupportsSampling {
		return nil, fmt.Errorf("client does not support sampling")
	}

	// Generate unique request ID
	id := atomic.AddInt64(&s.requestIDCounter, 1)

	// Create response channel
	responseCh := make(chan *Message, 1)
	key := normalizeRequestID(id)

	s.pendingRequestMu.Lock()
	s.pendingRequests[key] = responseCh
	s.pendingRequestMu.Unlock()

	// Clean up on exit
	defer func() {
		s.pendingRequestMu.Lock()
		delete(s.pendingRequests, key)
		s.pendingRequestMu.Unlock()
	}()

	// Send the sampling request
	req, err := NewRequest(id, "sampling/createMessage", params)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if err := s.transport.Send(ctx, req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg := <-responseCh:
		if msg.Error != nil {
			return nil, fmt.Errorf("sampling error: %s", msg.Error.Message)
		}

		var result CreateMessageResult
		if err := json.Unmarshal(msg.Result, &result); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}

		return &result, nil
	}
}

// SamplingContext provides sampling capabilities to tool handlers.
// It wraps the server and provides a convenient interface for requesting completions.
type SamplingContext struct {
	server *Server
	ctx    context.Context
}

// NewSamplingContext creates a new sampling context for use within a tool handler.
func (s *Server) NewSamplingContext(ctx context.Context) *SamplingContext {
	return &SamplingContext{server: s, ctx: ctx}
}

// Complete sends a simple completion request with a single user message.
func (sc *SamplingContext) Complete(prompt string, maxTokens int) (string, error) {
	result, err := sc.server.CreateMessage(sc.ctx, CreateMessageParams{
		Messages: []SamplingMessage{{
			Role:    "user",
			Content: Content{Type: "text", Text: prompt},
		}},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return "", err
	}

	return result.Content.Text, nil
}

// CompleteWithSystem sends a completion request with a system prompt.
func (sc *SamplingContext) CompleteWithSystem(systemPrompt, userPrompt string, maxTokens int) (string, error) {
	result, err := sc.server.CreateMessage(sc.ctx, CreateMessageParams{
		SystemPrompt: systemPrompt,
		Messages: []SamplingMessage{{
			Role:    "user",
			Content: Content{Type: "text", Text: userPrompt},
		}},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return "", err
	}

	return result.Content.Text, nil
}

func (s *Server) handleResourcesList(msg *Message) (*Message, error) {
	result := ResourcesListResultWithTemplates{
		Resources:         s.resources,
		ResourceTemplates: s.resourceTmpl,
	}
	return NewResponse(msg.ID, result)
}

func (s *Server) handleResourcesRead(ctx context.Context, msg *Message) (*Message, error) {
	var params ReadResourceParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return NewErrorResponse(msg.ID, InvalidParams, err.Error()), nil
	}

	s.logger.Info("resource read", "uri", params.URI, "id", msg.ID)

	// Try exact match first
	handler, ok := s.resourceHdlr[params.URI]
	if !ok {
		// Try template matching
		handler = s.matchResourceTemplate(params.URI)
	}

	if handler == nil {
		s.logger.Warn("unknown resource", "uri", params.URI)
		return NewErrorResponse(msg.ID, InvalidParams, fmt.Sprintf("unknown resource: %s", params.URI)), nil
	}

	result, err := handler(ctx, params.URI)
	if err != nil {
		s.logger.Error("resource read failed", "uri", params.URI, "error", err)
		return NewErrorResponse(msg.ID, InternalError, err.Error()), nil
	}

	return NewResponse(msg.ID, result)
}

func (s *Server) handleResourcesSubscribe(msg *Message) (*Message, error) {
	var params SubscribeResourceParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return NewErrorResponse(msg.ID, InvalidParams, err.Error()), nil
	}
	if params.URI == "" {
		return NewErrorResponse(msg.ID, InvalidParams, "missing uri"), nil
	}
	if !s.resourceExists(params.URI) {
		return NewErrorResponse(msg.ID, InvalidParams, fmt.Sprintf("unknown resource: %s", params.URI)), nil
	}

	s.resourceSubM.Lock()
	s.resourceSubs[params.URI] = struct{}{}
	s.resourceSubM.Unlock()

	return NewResponse(msg.ID, EmptyResult{})
}

func (s *Server) handleResourcesUnsubscribe(msg *Message) (*Message, error) {
	var params UnsubscribeResourceParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return NewErrorResponse(msg.ID, InvalidParams, err.Error()), nil
	}
	if params.URI == "" {
		return NewErrorResponse(msg.ID, InvalidParams, "missing uri"), nil
	}

	s.resourceSubM.Lock()
	delete(s.resourceSubs, params.URI)
	s.resourceSubM.Unlock()

	return NewResponse(msg.ID, EmptyResult{})
}

// matchResourceTemplate finds a handler for a URI by matching against templates.
// Simple implementation that checks if URI starts with the template prefix.
func (s *Server) matchResourceTemplate(uri string) ResourceHandler {
	for _, tmpl := range s.resourceTmpl {
		// Simple prefix matching for templates like "file:///{path}"
		// A full implementation would use proper RFC 6570 URI template matching
		prefix := extractTemplatePrefix(tmpl.URITemplate)
		if len(prefix) > 0 && len(uri) >= len(prefix) && uri[:len(prefix)] == prefix {
			return s.resourceHdlr[tmpl.URITemplate]
		}
	}
	return nil
}

func (s *Server) resourceExists(uri string) bool {
	if _, ok := s.resourceHdlr[uri]; ok {
		return true
	}
	return s.matchResourceTemplate(uri) != nil
}

// extractTemplatePrefix extracts the static prefix before the first template variable.
func extractTemplatePrefix(template string) string {
	for i, c := range template {
		if c == '{' {
			return template[:i]
		}
	}
	return template
}

func normalizeRequestID(id any) string {
	return fmt.Sprintf("%v", id)
}

// NotifyResourceUpdated sends notifications/resources/updated for subscribed resources.
func (s *Server) NotifyResourceUpdated(ctx context.Context, uri string) error {
	if s.transport == nil {
		return fmt.Errorf("no transport available")
	}

	s.resourceSubM.RLock()
	_, subscribed := s.resourceSubs[uri]
	s.resourceSubM.RUnlock()
	if !subscribed {
		return nil
	}

	notification, err := NewRequest(nil, "notifications/resources/updated", ResourceUpdatedNotification{URI: uri})
	if err != nil {
		return err
	}
	return s.transport.Send(ctx, notification)
}

// NotifyResourcesListChanged sends notifications/resources/list_changed.
func (s *Server) NotifyResourcesListChanged(ctx context.Context) error {
	if s.transport == nil {
		return fmt.Errorf("no transport available")
	}

	notification, err := NewRequest(nil, "notifications/resources/list_changed", nil)
	if err != nil {
		return err
	}
	return s.transport.Send(ctx, notification)
}

func (s *Server) handlePromptsList(msg *Message) (*Message, error) {
	return NewResponse(msg.ID, PromptsListResult{Prompts: s.prompts})
}

func (s *Server) handlePromptsGet(ctx context.Context, msg *Message) (*Message, error) {
	var params GetPromptParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return NewErrorResponse(msg.ID, InvalidParams, err.Error()), nil
	}

	s.logger.Info("prompt get", "name", params.Name, "id", msg.ID)

	handler, ok := s.promptHdlr[params.Name]
	if !ok {
		s.logger.Warn("unknown prompt", "name", params.Name)
		return NewErrorResponse(msg.ID, InvalidParams, fmt.Sprintf("unknown prompt: %s", params.Name)), nil
	}

	result, err := handler(ctx, params.Arguments)
	if err != nil {
		s.logger.Error("prompt get failed", "name", params.Name, "error", err)
		return NewErrorResponse(msg.ID, InternalError, err.Error()), nil
	}

	return NewResponse(msg.ID, result)
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

// TextResource creates a simple text resource result.
func TextResource(uri, text string) *ReadResourceResult {
	return &ReadResourceResult{
		Contents: []ResourceContents{{
			URI:      uri,
			MimeType: "text/plain",
			Text:     text,
		}},
	}
}

// JSONResource creates a JSON resource result.
func JSONResource(uri string, v any) (*ReadResourceResult, error) {
	text, err := formatStructuredResult(v)
	if err != nil {
		return nil, err
	}
	return &ReadResourceResult{
		Contents: []ResourceContents{{
			URI:      uri,
			MimeType: "application/json",
			Text:     text,
		}},
	}, nil
}

// MarkdownResource creates a markdown resource result.
func MarkdownResource(uri, text string) *ReadResourceResult {
	return &ReadResourceResult{
		Contents: []ResourceContents{{
			URI:      uri,
			MimeType: "text/markdown",
			Text:     text,
		}},
	}
}

// BinaryResource creates a binary resource result with base64 encoded data.
func BinaryResource(uri, mimeType, base64Data string) *ReadResourceResult {
	return &ReadResourceResult{
		Contents: []ResourceContents{{
			URI:      uri,
			MimeType: mimeType,
			Blob:     base64Data,
		}},
	}
}

// TextPrompt creates a simple text prompt result with user role.
func TextPrompt(text string) *GetPromptResult {
	return &GetPromptResult{
		Messages: []PromptMessage{{
			Role:    "user",
			Content: Content{Type: "text", Text: text},
		}},
	}
}

// MultiMessagePrompt creates a prompt with multiple messages.
func MultiMessagePrompt(messages ...PromptMessage) *GetPromptResult {
	return &GetPromptResult{
		Messages: messages,
	}
}

// UserMessage creates a user message for prompts.
func UserMessage(text string) PromptMessage {
	return PromptMessage{
		Role:    "user",
		Content: Content{Type: "text", Text: text},
	}
}

// AssistantMessage creates an assistant message for prompts.
func AssistantMessage(text string) PromptMessage {
	return PromptMessage{
		Role:    "assistant",
		Content: Content{Type: "text", Text: text},
	}
}

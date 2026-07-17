// Package mcp implements the Model Context Protocol (MCP) for building AI tool servers.
//
// MCP is a protocol for connecting AI assistants to external tools and data sources.
// This package provides types, server framework, and transports for implementing MCP servers.
//
// Quick start:
//
//	server := mcp.NewServer("my-server", "1.0.0")
//	server.AddTool(mcp.Tool{
//	    Name:        "hello",
//	    Description: "Says hello",
//	    InputSchema: mcp.InputSchema{Type: "object"},
//	}, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
//	    return mcp.TextResult("Hello, world!"), nil
//	})
//	server.Run(ctx)
//
// Reference: https://modelcontextprotocol.io/
package mcp

import (
	"encoding/json"
	"strings"
)

// JSONRPCVersion is the JSON-RPC version used by MCP.
const JSONRPCVersion = "2.0"

// ProtocolVersion is the MCP protocol version (legacy).
const ProtocolVersion = "2024-11-05"

// SupportedProtocolVersions lists all protocol versions this library supports.
var SupportedProtocolVersions = []string{ProtocolVersion, ProtocolVersion20250618}

// Message is the base JSON-RPC message structure.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error represents a JSON-RPC error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// InitializeParams contains the parameters for the initialize request.
type InitializeParams struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ClientInfo      ClientInfo   `json:"clientInfo"`
}

// Capabilities describes client/server capabilities.
type Capabilities struct {
	Roots        *RootsCapability     `json:"roots,omitempty"`
	Sampling     *SamplingCapability  `json:"sampling,omitempty"`
	Experimental map[string]any       `json:"experimental,omitempty"`
	Tools        *ToolsCapability     `json:"tools,omitempty"`
	Resources    *ResourcesCapability `json:"resources,omitempty"`
	Prompts      *PromptsCapability   `json:"prompts,omitempty"`
	Logging      *LoggingCapability   `json:"logging,omitempty"`
}

// RootsCapability indicates roots support.
type RootsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// SamplingCapability indicates sampling support.
type SamplingCapability struct{}

// ToolsCapability indicates tools support.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability indicates resources support.
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability indicates prompts support.
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// LoggingCapability indicates logging support.
type LoggingCapability struct{}

// ClientInfo describes the client.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerInfo describes the server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the result of the initialize request.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Instructions    string       `json:"instructions,omitempty"`
}

// Tool describes an MCP tool.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema describes the tool's input schema.
type InputSchema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
	Required   []string       `json:"required,omitempty"`
}

// ToolsListResult is the result of tools/list.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// CallToolParams are the parameters for tools/call.
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// CallToolResult is the result of tools/call.
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Content represents content in a tool result.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// MarshalJSON ensures we emit required fields even when they are empty strings.
//
// MCP text content requires the `text` field. Using `omitempty` on a string would
// drop it for empty outputs (e.g., commands with no stdout), which breaks strict
// client-side validators.
func (c Content) MarshalJSON() ([]byte, error) {
	// Always emit the type field.
	m := map[string]any{
		"type": c.Type,
	}

	// For text content, `text` is required even if empty.
	if strings.EqualFold(c.Type, "text") {
		m["text"] = c.Text
	} else if c.Text != "" {
		// For non-text content, keep previous behavior.
		m["text"] = c.Text
	}

	if c.MimeType != "" {
		m["mimeType"] = c.MimeType
	}
	if c.Data != "" {
		m["data"] = c.Data
	}

	return json.Marshal(m)
}

// Resource describes an MCP resource.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourcesListResult is the result of resources/list.
type ResourcesListResult struct {
	Resources []Resource `json:"resources"`
}

// Prompt describes an MCP prompt.
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument describes a prompt argument.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptsListResult is the result of prompts/list.
type PromptsListResult struct {
	Prompts []Prompt `json:"prompts"`
}

// ReadResourceParams are the parameters for resources/read.
type ReadResourceParams struct {
	URI string `json:"uri"`
}

// SubscribeResourceParams are the parameters for resources/subscribe.
type SubscribeResourceParams struct {
	URI string `json:"uri"`
}

// UnsubscribeResourceParams are the parameters for resources/unsubscribe.
type UnsubscribeResourceParams struct {
	URI string `json:"uri"`
}

// ResourceContents represents the contents of a resource.
type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"` // Base64 encoded binary data
}

// ReadResourceResult is the result of resources/read.
type ReadResourceResult struct {
	Contents []ResourceContents `json:"contents"`
}

// EmptyResult is a generic empty JSON-RPC result object.
type EmptyResult struct{}

// ResourceUpdatedNotification is sent when a subscribed resource changes.
type ResourceUpdatedNotification struct {
	URI string `json:"uri"`
}

// GetPromptParams are the parameters for prompts/get.
type GetPromptParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// PromptMessage represents a message in a prompt response.
type PromptMessage struct {
	Role    string  `json:"role"` // "user" or "assistant"
	Content Content `json:"content"`
}

// GetPromptResult is the result of prompts/get.
type GetPromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// ResourceTemplate describes a resource template with URI patterns.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourcesListResultWithTemplates extends ResourcesListResult with templates.
type ResourcesListResultWithTemplates struct {
	Resources         []Resource         `json:"resources"`
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates,omitempty"`
}

// SamplingMessage represents a message for sampling requests.
type SamplingMessage struct {
	Role    string  `json:"role"` // "user" or "assistant"
	Content Content `json:"content"`
}

// CreateMessageParams are the parameters for sampling/createMessage.
type CreateMessageParams struct {
	Messages         []SamplingMessage      `json:"messages"`
	ModelPreferences *ModelPreferences      `json:"modelPreferences,omitempty"`
	SystemPrompt     string                 `json:"systemPrompt,omitempty"`
	IncludeContext   string                 `json:"includeContext,omitempty"` // "none", "thisServer", "allServers"
	Temperature      *float64               `json:"temperature,omitempty"`
	MaxTokens        int                    `json:"maxTokens"`
	StopSequences    []string               `json:"stopSequences,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
}

// ModelPreferences specifies model selection preferences.
type ModelPreferences struct {
	Hints           []ModelHint `json:"hints,omitempty"`
	CostPriority    *float64    `json:"costPriority,omitempty"`         // 0-1
	SpeedPriority   *float64    `json:"speedPriority,omitempty"`        // 0-1
	IntelligencePri *float64    `json:"intelligencePriority,omitempty"` // 0-1
}

// ModelHint provides hints about which model to use.
type ModelHint struct {
	Name string `json:"name,omitempty"`
}

// CreateMessageResult is the result of sampling/createMessage.
type CreateMessageResult struct {
	Role       string  `json:"role"` // Always "assistant"
	Content    Content `json:"content"`
	Model      string  `json:"model"`
	StopReason string  `json:"stopReason,omitempty"` // "endTurn", "stopSequence", "maxTokens"
}

// ProgressNotification is sent during long-running operations.
type ProgressNotification struct {
	ProgressToken any     `json:"progressToken"`
	Progress      float64 `json:"progress"` // Current progress value
	Total         float64 `json:"total,omitempty"`
	Message       string  `json:"message,omitempty"`
}

// StreamUpdate represents an incremental update during a streaming tool call.
type StreamUpdate struct {
	// Content to append to the result (for incremental text)
	Content []Content `json:"content,omitempty"`
	// Progress for progress bar display (0-1)
	Progress float64 `json:"progress,omitempty"`
	// Message for status updates
	Message string `json:"message,omitempty"`
	// IsFinal indicates this is the last update
	IsFinal bool `json:"isFinal,omitempty"`
	// IsError indicates an error occurred
	IsError bool `json:"isError,omitempty"`
}

// CallToolParamsWithMeta includes metadata for tool calls.
type CallToolParamsWithMeta struct {
	CallToolParams
	Meta *CallToolMeta `json:"_meta,omitempty"`
}

// CallToolMeta contains optional metadata for tool calls.
type CallToolMeta struct {
	ProgressToken any `json:"progressToken,omitempty"`
}

// LoggingLevel represents MCP logging levels.
type LoggingLevel string

const (
	LogLevelDebug     LoggingLevel = "debug"
	LogLevelInfo      LoggingLevel = "info"
	LogLevelNotice    LoggingLevel = "notice"
	LogLevelWarning   LoggingLevel = "warning"
	LogLevelError     LoggingLevel = "error"
	LogLevelCritical  LoggingLevel = "critical"
	LogLevelAlert     LoggingLevel = "alert"
	LogLevelEmergency LoggingLevel = "emergency"
)

// LogNotification is sent for logging messages.
type LogNotification struct {
	Level  LoggingLevel `json:"level"`
	Logger string       `json:"logger,omitempty"`
	Data   any          `json:"data"`
}

// NewRequest creates a new JSON-RPC request message.
func NewRequest(id any, method string, params any) (*Message, error) {
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		rawParams = b
	}
	return &Message{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  method,
		Params:  rawParams,
	}, nil
}

// NewResponse creates a new JSON-RPC response message.
func NewResponse(id any, result any) (*Message, error) {
	var rawResult json.RawMessage
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		rawResult = b
	}
	return &Message{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result:  rawResult,
	}, nil
}

// NewErrorResponse creates a new JSON-RPC error response.
func NewErrorResponse(id any, code int, message string) *Message {
	return &Message{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	}
}

// IsRequest returns true if the message is a request.
func (m *Message) IsRequest() bool {
	return m.Method != "" && m.ID != nil
}

// IsNotification returns true if the message is a notification.
func (m *Message) IsNotification() bool {
	return m.Method != "" && m.ID == nil
}

// IsResponse returns true if the message is a response.
func (m *Message) IsResponse() bool {
	return m.Method == "" && (m.Result != nil || m.Error != nil)
}

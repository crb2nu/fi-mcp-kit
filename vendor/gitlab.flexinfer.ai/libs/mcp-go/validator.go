package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Validator validates incoming MCP messages.
type Validator struct {
	// StrictMode enables stricter validation checks (e.g., forbidding unknown fields).
	StrictMode bool
}

// NewValidator creates a new Validator.
func NewValidator(strict bool) *Validator {
	return &Validator{StrictMode: strict}
}

// ValidateRequest validates a JSON-RPC request message.
func (v *Validator) ValidateRequest(msg *Message) error {
	if msg.JSONRPC != JSONRPCVersion {
		return fmt.Errorf("invalid jsonrpc version: %s (expected %s)", msg.JSONRPC, JSONRPCVersion)
	}

	if msg.Method == "" {
		return errors.New("missing method")
	}

	// Validate params based on method
	switch msg.Method {
	case "initialize":
		return v.validateInitialize(msg.Params)
	case "tools/call":
		return v.validateCallTool(msg.Params)
		// Add other methods as needed
	}

	return nil
}

func (v *Validator) validateInitialize(params json.RawMessage) error {
	if len(params) == 0 {
		return errors.New("missing params for initialize")
	}

	var p InitializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("invalid params for initialize: %w", err)
	}

	if p.ProtocolVersion == "" {
		return errors.New("missing protocolVersion")
	}
	if p.ClientInfo.Name == "" {
		return errors.New("missing clientInfo.name")
	}

	return nil
}

func (v *Validator) validateCallTool(params json.RawMessage) error {
	if len(params) == 0 {
		return errors.New("missing params for tools/call")
	}

	var p CallToolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("invalid params for tools/call: %w", err)
	}

	if p.Name == "" {
		return errors.New("missing tool name")
	}

	return nil
}

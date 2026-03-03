package mcp

// ToolBuilder helps construct Tool definitions with a fluent API.
type ToolBuilder struct {
	tool Tool
}

// NewTool creates a new ToolBuilder.
func NewTool(name, description string) *ToolBuilder {
	return &ToolBuilder{
		tool: Tool{
			Name:        name,
			Description: description,
			InputSchema: InputSchema{
				Type:       "object",
				Properties: make(map[string]any),
				Required:   []string{},
			},
		},
	}
}

// WithString adds a string argument to the tool.
func (b *ToolBuilder) WithString(name, description string, required bool) *ToolBuilder {
	b.tool.InputSchema.Properties[name] = map[string]any{
		"type":        "string",
		"description": description,
	}
	if required {
		b.tool.InputSchema.Required = append(b.tool.InputSchema.Required, name)
	}
	return b
}

// WithNumber adds a number argument to the tool.
func (b *ToolBuilder) WithNumber(name, description string, required bool) *ToolBuilder {
	b.tool.InputSchema.Properties[name] = map[string]any{
		"type":        "number",
		"description": description,
	}
	if required {
		b.tool.InputSchema.Required = append(b.tool.InputSchema.Required, name)
	}
	return b
}

// WithBoolean adds a boolean argument to the tool.
func (b *ToolBuilder) WithBoolean(name, description string, required bool) *ToolBuilder {
	b.tool.InputSchema.Properties[name] = map[string]any{
		"type":        "boolean",
		"description": description,
	}
	if required {
		b.tool.InputSchema.Required = append(b.tool.InputSchema.Required, name)
	}
	return b
}

// WithEnum adds a string enum argument to the tool.
func (b *ToolBuilder) WithEnum(name, description string, values []string, required bool) *ToolBuilder {
	b.tool.InputSchema.Properties[name] = map[string]any{
		"type":        "string",
		"description": description,
		"enum":        values,
	}
	if required {
		b.tool.InputSchema.Required = append(b.tool.InputSchema.Required, name)
	}
	return b
}

// Build returns the constructed Tool.
func (b *ToolBuilder) Build() Tool {
	return b.tool
}

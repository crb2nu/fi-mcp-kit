package validator

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed schemas/mcp_json.json
var mcpJSONSchemaBytes []byte

//go:embed schemas/claude_settings.json
var claudeSettingsSchemaBytes []byte

//go:embed schemas/gemini_settings.json
var geminiSettingsSchemaBytes []byte

//go:embed schemas/codex_config.json
var codexConfigSchemaBytes []byte

var (
	mcpJSONSchema        *jsonschema.Schema
	claudeSettingsSchema *jsonschema.Schema
	geminiSettingsSchema *jsonschema.Schema
	codexConfigSchema    *jsonschema.Schema
)

func init() {
	mcpJSONSchema = mustCompileSchema("mcp_json.json", mcpJSONSchemaBytes)
	claudeSettingsSchema = mustCompileSchema("claude_settings.json", stripNonRE2Patterns(claudeSettingsSchemaBytes))
	geminiSettingsSchema = mustCompileSchema("gemini_settings.json", geminiSettingsSchemaBytes)
	codexConfigSchema = mustCompileSchema("codex_config.json", codexConfigSchemaBytes)
}

func mustCompileSchema(name string, data []byte) *jsonschema.Schema {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(name, strings.NewReader(string(data))); err != nil {
		panic(fmt.Sprintf("failed to load embedded schema %s: %v", name, err))
	}
	schema, err := compiler.Compile(name)
	if err != nil {
		panic(fmt.Sprintf("failed to compile schema %s: %v", name, err))
	}
	return schema
}

// stripNonRE2Patterns removes regex patterns from a JSON schema that use ECMA-262
// features (lookaheads, lookbehinds) unsupported by Go's RE2 engine. The library
// validates patterns during compilation using regexp.Compile, so non-RE2 patterns
// cause panics. Removing them preserves full structural validation while skipping
// pattern-level string matching.
func stripNonRE2Patterns(data []byte) []byte {
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		return data // Fall through to compilation error
	}
	stripPatternsRecursive(schema)
	out, err := json.Marshal(schema)
	if err != nil {
		return data
	}
	return out
}

func stripPatternsRecursive(v any) {
	switch m := v.(type) {
	case map[string]any:
		if p, ok := m["pattern"].(string); ok {
			if strings.Contains(p, "(?") {
				delete(m, "pattern")
			}
		}
		for _, val := range m {
			stripPatternsRecursive(val)
		}
	case []any:
		for _, val := range v.([]any) {
			stripPatternsRecursive(val)
		}
	}
}

// ValidateJSONSchema validates JSON config content against the MCP schema.
func ValidateJSONSchema(target, filePath string, content []byte) *ValidationResult {
	result := &ValidationResult{
		Target: target,
		File:   filePath,
		Valid:  true,
	}

	// Parse JSON
	var data interface{}
	if err := json.Unmarshal(content, &data); err != nil {
		result.AddError(CodeInvalidSchema, "", fmt.Sprintf("invalid JSON: %v", err))
		result.Valid = false
		return result
	}

	// Validate against schema
	if err := mcpJSONSchema.Validate(data); err != nil {
		// Parse validation errors
		if ve, ok := err.(*jsonschema.ValidationError); ok {
			for _, cause := range flattenValidationErrors(ve) {
				field := cause.InstanceLocation
				if field == "" {
					field = "/"
				}
				result.AddError(CodeInvalidSchema, field, cause.Message)
			}
		} else {
			result.AddError(CodeInvalidSchema, "", err.Error())
		}
		result.Valid = false
	}

	// Additional semantic validation
	validateJSONSemantics(data, result)

	result.Valid = !result.HasErrors()
	return result
}

// flattenValidationErrors extracts all leaf errors from a validation error tree.
func flattenValidationErrors(ve *jsonschema.ValidationError) []*jsonschema.ValidationError {
	var errors []*jsonschema.ValidationError
	if len(ve.Causes) == 0 {
		errors = append(errors, ve)
	}
	for _, cause := range ve.Causes {
		errors = append(errors, flattenValidationErrors(cause)...)
	}
	return errors
}

// validateJSONSemantics performs additional semantic checks beyond schema validation.
func validateJSONSemantics(data interface{}, result *ValidationResult) {
	m, ok := data.(map[string]interface{})
	if !ok {
		return
	}

	// Claude Code / Antigravity / Zed use "mcpServers"; VS Code's mcp.json
	// only reads "servers" (it silently ignores "mcpServers") — see
	// https://code.visualstudio.com/docs/copilot/customization/mcp-servers.
	rootKey := "mcpServers"
	servers, ok := m[rootKey].(map[string]interface{})
	if !ok {
		rootKey = "servers"
		servers, ok = m[rootKey].(map[string]interface{})
	}
	if !ok {
		result.AddError(CodeMissingRootKey, "", "missing or invalid mcpServers/servers key")
		return
	}

	for name, serverData := range servers {
		server, ok := serverData.(map[string]interface{})
		if !ok {
			continue
		}

		field := fmt.Sprintf("%s.%s", rootKey, name)

		// Check command is not empty
		cmd, _ := server["command"].(string)
		if cmd == "" {
			result.AddError(CodeMissingCommand, field+".command", "command is required")
		}

		// Check args type if present
		if args, exists := server["args"]; exists {
			if _, ok := args.([]interface{}); !ok {
				result.AddError(CodeInvalidArgsType, field+".args", "args must be an array")
			}
		}

		// Check env type if present
		if env, exists := server["env"]; exists {
			if _, ok := env.(map[string]interface{}); !ok {
				result.AddError(CodeInvalidEnvType, field+".env", "env must be an object")
			}
		}
	}
}

// TOMLServerConfig represents a server configuration in TOML format.
type TOMLServerConfig struct {
	Command     string            `toml:"command"`
	Args        []string          `toml:"args"`
	Description string            `toml:"description"`
	Hint        string            `toml:"hint"`
	Timeout     int               `toml:"timeout"`
	AlwaysAllow []string          `toml:"always_allow"`
	Env         map[string]string `toml:"env"`
}

// TOMLConfig represents the TOML config file structure.
type TOMLConfig struct {
	MCPServers map[string]TOMLServerConfig `toml:"mcp_servers"`
}

// ValidateTOMLStructure validates TOML config structure for Codex/Kilocode/Gemini.
func ValidateTOMLStructure(target, filePath string, content []byte) *ValidationResult {
	result := &ValidationResult{
		Target: target,
		File:   filePath,
		Valid:  true,
	}

	var cfg TOMLConfig
	if err := toml.Unmarshal(content, &cfg); err != nil {
		result.AddError(CodeInvalidSchema, "", fmt.Sprintf("invalid TOML: %v", err))
		result.Valid = false
		return result
	}

	// Check for mcp_servers section
	if len(cfg.MCPServers) == 0 {
		result.AddError(CodeMissingRootKey, "", "missing or empty mcp_servers section")
		result.Valid = false
		return result
	}

	// Validate each server
	for name, server := range cfg.MCPServers {
		field := fmt.Sprintf("mcp_servers.%s", name)

		// Command is required
		if server.Command == "" {
			result.AddError(CodeMissingCommand, field+".command", "command is required")
		}

		// Timeout must be non-negative
		if server.Timeout < 0 {
			result.AddError(CodeInvalidTimeout, field+".timeout",
				fmt.Sprintf("timeout must be non-negative, got %d", server.Timeout))
		}
	}

	result.Valid = !result.HasErrors()
	return result
}

// ValidateClaudeSettings validates a Claude Code settings.json against the upstream schema.
func ValidateClaudeSettings(filePath string, content []byte) *ValidationResult {
	return validateUpstreamJSON("claude", filePath, content, claudeSettingsSchema)
}

// ValidateGeminiSettings validates a Gemini CLI settings.json against the upstream schema.
func ValidateGeminiSettings(filePath string, content []byte) *ValidationResult {
	return validateUpstreamJSON("gemini", filePath, content, geminiSettingsSchema)
}

// ValidateCodexConfig validates a Codex config.toml against the upstream schema.
// The TOML content is converted to JSON for schema validation.
func ValidateCodexConfig(filePath string, content []byte) *ValidationResult {
	result := &ValidationResult{
		Target: "codex",
		File:   filePath,
		Valid:  true,
	}

	// Parse TOML into generic map
	var data map[string]any
	if err := toml.Unmarshal(content, &data); err != nil {
		result.AddError(CodeInvalidSchema, "", fmt.Sprintf("invalid TOML: %v", err))
		result.Valid = false
		return result
	}

	// Validate the TOML-as-JSON against the Codex schema
	if err := codexConfigSchema.Validate(data); err != nil {
		if ve, ok := err.(*jsonschema.ValidationError); ok {
			for _, cause := range flattenValidationErrors(ve) {
				field := cause.InstanceLocation
				if field == "" {
					field = "/"
				}
				result.AddWarning(CodeUpstreamSchema, field, cause.Message)
			}
		} else {
			result.AddWarning(CodeUpstreamSchema, "", err.Error())
		}
	}

	result.Valid = !result.HasErrors()
	return result
}

// validateUpstreamJSON validates JSON content against an upstream schema.
// Validation failures are reported as warnings (non-blocking) since upstream
// schemas may evolve faster than our vendored copies.
func validateUpstreamJSON(target, filePath string, content []byte, schema *jsonschema.Schema) *ValidationResult {
	result := &ValidationResult{
		Target: target,
		File:   filePath,
		Valid:  true,
	}

	var data any
	if err := json.Unmarshal(content, &data); err != nil {
		result.AddError(CodeInvalidSchema, "", fmt.Sprintf("invalid JSON: %v", err))
		result.Valid = false
		return result
	}

	if err := schema.Validate(data); err != nil {
		if ve, ok := err.(*jsonschema.ValidationError); ok {
			for _, cause := range flattenValidationErrors(ve) {
				field := cause.InstanceLocation
				if field == "" {
					field = "/"
				}
				result.AddWarning(CodeUpstreamSchema, field, cause.Message)
			}
		} else {
			result.AddWarning(CodeUpstreamSchema, "", err.Error())
		}
	}

	result.Valid = !result.HasErrors()
	return result
}

// UpstreamSchemaInfo describes a vendored upstream schema.
type UpstreamSchemaInfo struct {
	Platform string // e.g., "claude", "gemini", "codex"
	Name     string // File name in schemas/ directory
	URL      string // Canonical upstream URL for fetching updates
}

// UpstreamSchemas returns metadata for all vendored upstream schemas.
func UpstreamSchemas() []UpstreamSchemaInfo {
	return []UpstreamSchemaInfo{
		{
			Platform: "claude",
			Name:     "claude_settings.json",
			URL:      "https://json.schemastore.org/claude-code-settings.json",
		},
		{
			Platform: "gemini",
			Name:     "gemini_settings.json",
			URL:      "https://raw.githubusercontent.com/google-gemini/gemini-cli/main/schemas/settings.schema.json",
		},
		{
			Platform: "codex",
			Name:     "codex_config.json",
			URL:      "https://developers.openai.com/codex/config-schema.json",
		},
	}
}

// GetEmbeddedSchema returns the raw bytes of a vendored schema by filename.
func GetEmbeddedSchema(name string) ([]byte, bool) {
	switch name {
	case "claude_settings.json":
		return claudeSettingsSchemaBytes, true
	case "gemini_settings.json":
		return geminiSettingsSchemaBytes, true
	case "codex_config.json":
		return codexConfigSchemaBytes, true
	case "mcp_json.json":
		return mcpJSONSchemaBytes, true
	default:
		return nil, false
	}
}

// IsJSONTarget returns true if the target uses JSON format.
func IsJSONTarget(target string) bool {
	switch target {
	case "claude", "claude_desktop", "vscode", "antigravity":
		return true
	default:
		return false
	}
}

// IsTOMLTarget returns true if the target uses TOML format.
func IsTOMLTarget(target string) bool {
	switch target {
	case "codex", "kilocode", "gemini":
		return true
	default:
		return false
	}
}

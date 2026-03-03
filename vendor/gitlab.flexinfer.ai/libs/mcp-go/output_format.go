package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// OutputFormat controls how structured results are serialized into tool text output.
//
// This is intentionally environment-driven so it can be toggled without code changes.
//
// Values:
// - "toon" (default): Token-Oriented Object Notation (compact, LLM-friendly)
// - "json": compact JSON
// - "json_pretty": indented JSON (legacy behavior)
// - "auto": choose the smaller of TOON vs compact JSON (approx tokens)
type OutputFormat string

const (
	OutputFormatTOON       OutputFormat = "toon"
	OutputFormatJSON       OutputFormat = "json"
	OutputFormatJSONPretty OutputFormat = "json_pretty"
	OutputFormatAuto       OutputFormat = "auto"
)

const outputFormatEnv = "LOOM_MCP_OUTPUT_FORMAT"

func outputFormatFromEnv() OutputFormat {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(outputFormatEnv)))
	if v == "" {
		return OutputFormatTOON
	}
	switch OutputFormat(v) {
	case OutputFormatTOON, OutputFormatJSON, OutputFormatJSONPretty, OutputFormatAuto:
		return OutputFormat(v)
	default:
		// Fail-safe: keep existing behavior on unknown values.
		return OutputFormatJSONPretty
	}
}

// EstimateTokensApprox estimates token usage for quick comparisons.
//
// This is not model-exact; it's intended for before/after deltas.
func EstimateTokensApprox(s string) int {
	if s == "" {
		return 0
	}
	// Matches the heuristic used elsewhere in this workspace (bytes/4).
	return len(s) / 4
}

func formatStructuredResult(v any) (string, error) {
	switch outputFormatFromEnv() {
	case OutputFormatJSONPretty:
		return formatJSON(v, true)
	case OutputFormatJSON:
		return formatJSON(v, false)
	case OutputFormatAuto:
		jsonCompact, err := formatJSON(v, false)
		if err != nil {
			return "", err
		}
		toon, err := formatTOON(v)
		if err != nil {
			// If TOON encoding fails for any reason, fall back to compact JSON.
			return jsonCompact, nil
		}
		if EstimateTokensApprox(toon) <= EstimateTokensApprox(jsonCompact) {
			return toon, nil
		}
		return jsonCompact, nil
	case OutputFormatTOON:
		toon, err := formatTOON(v)
		if err == nil {
			return toon, nil
		}
		// Fail-safe: never break tool execution due to formatting.
		return formatJSON(v, true)
	default:
		return "", fmt.Errorf("unsupported output format: %q", outputFormatFromEnv())
	}
}

func formatJSON(v any, pretty bool) (string, error) {
	var (
		b   []byte
		err error
	)
	if pretty {
		b, err = json.MarshalIndent(v, "", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

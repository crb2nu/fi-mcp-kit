package gateway

import (
	"encoding/json"
	"strings"

	"gitlab.flexinfer.ai/libs/mcp-go"
)

// normalizeToolResultText rewrites TOON-encoded text content in a successful
// CallToolResult to compact JSON.
//
// The loom-hub MCP servers emit structured tool results as TOON text by
// default (mcp-go formatStructuredResult, LOOM_MCP_OUTPUT_FORMAT=toon).
// That is a good trade for agents on the WS path, but REST consumers are
// programs that json-parse the text payload (reference consumer: ICC's
// connectors.py _unwrap_tool_payload). This pins the REST endpoint's
// contract as "text content is JSON whenever the result is structured";
// the WS/agent path keeps TOON.
//
// Rules per content item of type "text" (non-isError results only):
//   - already valid JSON: left untouched
//   - TOON that decodes to an object or array: replaced with compact JSON
//   - anything else (decode failure, bare primitives = plain prose):
//     passed through unchanged
//
// On any structural surprise the original raw bytes are returned unchanged —
// normalization must never break a response that would otherwise be served.
func normalizeToolResultText(raw json.RawMessage) json.RawMessage {
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return raw
	}
	if isErr, _ := result["isError"].(bool); isErr {
		return raw
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		return raw
	}

	changed := false
	for _, item := range content {
		m, ok := item.(map[string]any)
		if !ok || m["type"] != "text" {
			continue
		}
		text, ok := m["text"].(string)
		if !ok {
			continue
		}
		if normalized, ok := toonTextToJSON(text); ok {
			m["text"] = normalized
			changed = true
		}
	}
	if !changed {
		return raw
	}

	out, err := json.Marshal(result)
	if err != nil {
		return raw
	}
	return out
}

// toonTextToJSON converts a TOON-encoded structured payload to compact JSON.
// It reports ok=false when the text is already JSON, is empty, fails to
// decode, or decodes to a bare primitive — a single prose line such as
// "Transited issue ICC-1 to Done" decodes as a TOON string primitive and
// must survive unchanged. (A prose line containing a colon is inherently
// ambiguous with a TOON key/value pair and will be normalized; structured
// results dominate the REST tool surface, so that trade favors consumers.)
func toonTextToJSON(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	if json.Valid([]byte(trimmed)) {
		return "", false
	}

	v, err := mcp.DecodeTOON(text)
	if err != nil {
		return "", false
	}
	switch v.(type) {
	case map[string]any, []any:
	default:
		return "", false
	}

	v = unwrapAnonymousItems(v)

	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// unwrapAnonymousItems undoes the TOON encoder's root-array wrapping: a
// root-level JSON array is encoded as an anonymous "items" field
// (mcp-go encodeTOONValue, root []any case), so a decoded object with
// exactly one "items" key holding an array is restored to the bare array.
// A genuine {"items": [...]} object encodes identically, so the distinction
// is already lost in TOON; restoring the array is the contract choice here.
func unwrapAnonymousItems(v any) any {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return v
	}
	arr, ok := m["items"].([]any)
	if !ok {
		return v
	}
	return arr
}

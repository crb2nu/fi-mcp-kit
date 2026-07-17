package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
)

const maxEmbeddedJSONBytes = 1 << 20 // 1 MiB

// deepConvertEmbeddedJSON attempts to parse embedded JSON strings in common
// "wrapper" fields (stdout/stderr/etc) into structured values before TOON encoding.
//
// This reduces "JSON inside TOON" outputs while avoiding risky conversions for
// fields that are commonly user-authored text (content/body/description/etc).
func deepConvertEmbeddedJSON(v any) any {
	return deepConvertEmbeddedJSONWithKey(v, "")
}

func deepConvertEmbeddedJSONWithKey(v any, key string) any {
	switch vv := v.(type) {
	case orderedObject:
		for i := range vv.Entries {
			entryKey := vv.Entries[i].Key
			vv.Entries[i].Value = deepConvertEmbeddedJSONWithKey(vv.Entries[i].Value, entryKey)
		}
		return vv
	case []any:
		for i := range vv {
			vv[i] = deepConvertEmbeddedJSONWithKey(vv[i], key)
		}
		return vv
	case string:
		if !shouldParseEmbeddedJSONForKey(key) {
			return vv
		}
		parsed, ok := parseEmbeddedJSON(vv)
		if !ok {
			return vv
		}
		return deepConvertEmbeddedJSONWithKey(parsed, key)
	default:
		return v
	}
}

func shouldParseEmbeddedJSONForKey(key string) bool {
	k := strings.TrimSpace(strings.ToLower(key))
	if k == "" {
		return false
	}

	// Avoid converting common "free text" fields (could be user content or file contents).
	switch k {
	case "content", "body", "description", "message", "trace", "diff", "patch", "raw":
		return false
	}

	// Strong signals that the value is a wrapper around JSON.
	switch k {
	case "stdout", "stderr", "output", "result", "data", "payload", "response", "request", "json":
		return true
	}
	if strings.HasSuffix(k, "_json") || strings.HasSuffix(k, "json") {
		return true
	}
	return false
}

func parseEmbeddedJSON(s string) (any, bool) {
	if len(s) == 0 || len(s) > maxEmbeddedJSONBytes {
		return nil, false
	}
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 2 {
		return nil, false
	}
	// Only attempt full JSON documents (object/array). Avoid parsing JSON primitives.
	if (!strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}")) &&
		(!strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]")) {
		return nil, false
	}

	dec := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	dec.UseNumber()
	val, err := decodeOrderedJSON(dec)
	if err != nil {
		return nil, false
	}
	switch val.(type) {
	case orderedObject, []any:
		return val, true
	default:
		return nil, false
	}
}

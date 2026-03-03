package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// formatTOON encodes v into a TOON-like representation.
//
// This is an encoder-only implementation intended for token-efficient MCP tool outputs.
// If it cannot encode a value safely, it returns an error so callers can fall back to JSON.
func formatTOON(v any) (string, error) {
	// Normalize through encoding/json to:
	// - handle structs/tags consistently
	// - ensure stable map key order (encoding/json sorts map keys)
	// - collapse Go-specific types into JSON types
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()

	val, err := decodeOrderedJSON(dec)
	if err != nil {
		return "", err
	}
	val = deepConvertEmbeddedJSON(val)

	var lines []string
	if err := encodeTOONValue(&lines, 0, "", val, toonEncodeOptions{root: true}); err != nil {
		return "", err
	}

	out := strings.Join(lines, "\n")
	// No trailing newline, no trailing spaces on any line.
	return out, nil
}

type toonEncodeOptions struct {
	root bool
}

type orderedObject struct {
	Entries []orderedEntry
}

type orderedEntry struct {
	Key   string
	Value any
}

func decodeOrderedJSON(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			var obj orderedObject
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not string: %T", keyTok)
				}
				val, err := decodeOrderedJSON(dec)
				if err != nil {
					return nil, err
				}
				obj.Entries = append(obj.Entries, orderedEntry{Key: key, Value: val})
			}
			// consume '}'
			endTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if endDelim, ok := endTok.(json.Delim); !ok || endDelim != '}' {
				return nil, fmt.Errorf("expected object end, got %v", endTok)
			}
			return obj, nil
		case '[':
			var arr []any
			for dec.More() {
				val, err := decodeOrderedJSON(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			// consume ']'
			endTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if endDelim, ok := endTok.(json.Delim); !ok || endDelim != ']' {
				return nil, fmt.Errorf("expected array end, got %v", endTok)
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("unexpected delimiter: %q", string(v))
		}
	case json.Number:
		return v, nil
	case string:
		return v, nil
	case bool:
		return v, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected token type: %T", tok)
	}
}

func encodeTOONValue(lines *[]string, indent int, key string, v any, opts toonEncodeOptions) error {
	if opts.root && key != "" {
		return fmt.Errorf("root key must be empty")
	}

	indentStr := strings.Repeat("  ", indent)
	if opts.root {
		// Root may be a primitive, object, or array.
		switch vv := v.(type) {
		case orderedObject:
			for _, e := range vv.Entries {
				if err := encodeTOONValue(lines, indent, e.Key, e.Value, toonEncodeOptions{}); err != nil {
					return err
				}
			}
			return nil
		case []any:
			// Root arrays are represented as an anonymous array field for readability.
			// (Most MCP payloads are objects; this is rare.)
			return encodeArray(lines, indent, key, vv, true)
		default:
			token, err := encodePrimitiveToken(v)
			if err != nil {
				return err
			}
			*lines = append(*lines, token)
			return nil
		}
	}

	switch vv := v.(type) {
	case orderedObject:
		if len(vv.Entries) == 0 {
			*lines = append(*lines, fmt.Sprintf("%s%s:", indentStr, encodeKeyToken(key)))
			return nil
		}
		*lines = append(*lines, fmt.Sprintf("%s%s:", indentStr, encodeKeyToken(key)))
		for _, e := range vv.Entries {
			if err := encodeTOONValue(lines, indent+1, e.Key, e.Value, toonEncodeOptions{}); err != nil {
				return err
			}
		}
		return nil
	case []any:
		return encodeArray(lines, indent, key, vv, false)
	default:
		token, err := encodePrimitiveToken(v)
		if err != nil {
			return err
		}
		*lines = append(*lines, fmt.Sprintf("%s%s: %s", indentStr, encodeKeyToken(key), token))
		return nil
	}
}

func encodeArray(lines *[]string, indent int, key string, arr []any, anonymous bool) error {
	indentStr := strings.Repeat("  ", indent)
	name := encodeKeyToken(key)
	if anonymous {
		name = "items"
	}

	// Try tabular form: array of uniform objects with primitive fields.
	if headerKeys, rows, ok := tabularizePrimitiveSubset(arr); ok {
		header := fmt.Sprintf("%s%s[%d]{%s}:", indentStr, name, len(arr), strings.Join(headerKeys, ","))
		*lines = append(*lines, header)
		rowIndent := strings.Repeat("  ", indent+1)
		for _, row := range rows {
			*lines = append(*lines, rowIndent+strings.Join(row, ","))
		}
		return nil
	}
	if headerKeys, rows, ok := tabularize(arr); ok {
		header := fmt.Sprintf("%s%s[%d]{%s}:", indentStr, name, len(arr), strings.Join(headerKeys, ","))
		*lines = append(*lines, header)
		rowIndent := strings.Repeat("  ", indent+1)
		for _, row := range rows {
			*lines = append(*lines, rowIndent+strings.Join(row, ","))
		}
		return nil
	}

	// Try inline primitive array.
	allPrim := true
	encoded := make([]string, 0, len(arr))
	for _, it := range arr {
		tok, err := encodePrimitiveToken(it)
		if err != nil {
			allPrim = false
			break
		}
		encoded = append(encoded, tok)
	}
	if allPrim {
		*lines = append(*lines, fmt.Sprintf("%s%s[%d]: %s", indentStr, name, len(arr), strings.Join(encoded, ",")))
		return nil
	}

	// Fallback list form.
	*lines = append(*lines, fmt.Sprintf("%s%s[%d]:", indentStr, name, len(arr)))
	itemIndent := strings.Repeat("  ", indent+1)
	for _, it := range arr {
		switch vv := it.(type) {
		case orderedObject:
			*lines = append(*lines, itemIndent+"-")
			for _, e := range vv.Entries {
				if err := encodeTOONValue(lines, indent+2, e.Key, e.Value, toonEncodeOptions{}); err != nil {
					return err
				}
			}
		case []any:
			// Nested arrays are represented as list items with a nested anonymous array.
			*lines = append(*lines, itemIndent+"-")
			if err := encodeArray(lines, indent+2, "items", vv, true); err != nil {
				return err
			}
		default:
			tok, err := encodePrimitiveToken(it)
			if err != nil {
				return err
			}
			*lines = append(*lines, itemIndent+"- "+tok)
		}
	}
	return nil
}

func tabularize(arr []any) (headerKeys []string, rows [][]string, ok bool) {
	if len(arr) == 0 {
		return nil, nil, false
	}

	firstObj, ok := arr[0].(orderedObject)
	if !ok || len(firstObj.Entries) == 0 {
		return nil, nil, false
	}

	// Keys in encounter order.
	for _, e := range firstObj.Entries {
		headerKeys = append(headerKeys, e.Key)
	}

	// Enforce uniform shape and primitive-only values.
	for _, it := range arr {
		obj, ok := it.(orderedObject)
		if !ok || len(obj.Entries) != len(headerKeys) {
			return nil, nil, false
		}

		row := make([]string, 0, len(headerKeys))
		for i, wantKey := range headerKeys {
			if obj.Entries[i].Key != wantKey {
				return nil, nil, false
			}
			tok, err := encodePrimitiveToken(obj.Entries[i].Value)
			if err != nil {
				return nil, nil, false
			}
			row = append(row, tok)
		}
		rows = append(rows, row)
	}

	return headerKeys, rows, true
}

// tabularizePrimitiveSubset creates a tabular representation for arrays of objects when a
// strict table isn't possible (e.g., nested objects/arrays exist).
//
// It selects the subset of keys from the first element that:
// - exist in every element, and
// - are primitive in every element
//
// This produces a compact summary table while keeping complex nested fields out of the output.
func tabularizePrimitiveSubset(arr []any) (headerKeys []string, rows [][]string, ok bool) {
	if len(arr) == 0 {
		return nil, nil, false
	}

	firstObj, ok := arr[0].(orderedObject)
	if !ok || len(firstObj.Entries) == 0 {
		return nil, nil, false
	}

	// Only consider keys from the first object to keep ordering stable.
	candidates := make([]string, 0, len(firstObj.Entries))
	for _, e := range firstObj.Entries {
		candidates = append(candidates, e.Key)
	}

	// Build a primitive-safe key subset.
	for _, key := range candidates {
		primitiveForAll := true
		for _, it := range arr {
			obj, ok := it.(orderedObject)
			if !ok {
				return nil, nil, false
			}
			val, found := findOrderedObjectKey(obj, key)
			if !found {
				primitiveForAll = false
				break
			}
			if _, err := encodePrimitiveToken(val); err != nil {
				primitiveForAll = false
				break
			}
		}
		if primitiveForAll {
			headerKeys = append(headerKeys, key)
		}
	}

	// Avoid creating single-column tables unless it's the only useful summary.
	if len(headerKeys) == 0 {
		return nil, nil, false
	}

	// Build rows.
	for _, it := range arr {
		obj := it.(orderedObject)
		row := make([]string, 0, len(headerKeys))
		for _, key := range headerKeys {
			val, _ := findOrderedObjectKey(obj, key)
			tok, _ := encodePrimitiveToken(val)
			row = append(row, tok)
		}
		rows = append(rows, row)
	}

	return headerKeys, rows, true
}

func findOrderedObjectKey(obj orderedObject, key string) (any, bool) {
	for _, e := range obj.Entries {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

var numericStringRE = regexp.MustCompile(`^[+-]?(?:\d+|\d*\.\d+)(?:[eE][+-]?\d+)?$`)

func encodePrimitiveToken(v any) (string, error) {
	switch vv := v.(type) {
	case nil:
		return "null", nil
	case bool:
		if vv {
			return "true", nil
		}
		return "false", nil
	case json.Number:
		// Canonicalize to non-exponential decimals when possible; if not, keep as-is.
		return canonicalizeNumber(vv.String()), nil
	case float64:
		if math.IsNaN(vv) || math.IsInf(vv, 0) {
			return "null", nil
		}
		return canonicalizeNumber(strconv.FormatFloat(vv, 'g', -1, 64)), nil
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", vv), nil
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", vv), nil
	case string:
		return encodeStringToken(vv, ',')
	default:
		// Objects/arrays are not primitives for TOON tabular/inline forms.
		return "", fmt.Errorf("non-primitive type: %T", v)
	}
}

func canonicalizeNumber(s string) string {
	// Minimal normalization: avoid -0.
	if s == "-0" || s == "-0.0" {
		return "0"
	}
	return s
}

func encodeKeyToken(key string) string {
	// Keys follow the same quoting rules as strings in practice.
	// Use comma as document delimiter (most compact for our encoder).
	tok, err := encodeStringToken(key, ',')
	if err != nil {
		// Keys must always be representable; fall back to a best-effort quoted string.
		q, qerr := quoteTOONString(key)
		if qerr == nil {
			return q
		}
		return `""`
	}
	return tok
}

func encodeStringToken(s string, delimiter byte) (string, error) {
	if s == "" {
		return `""`, nil
	}
	if !needsQuotes(s, delimiter) {
		return s, nil
	}
	return quoteTOONString(s)
}

func needsQuotes(s string, delimiter byte) bool {
	// Avoid ambiguity with TOON primitives and numbers.
	lower := strings.ToLower(s)
	if lower == "true" || lower == "false" || lower == "null" {
		return true
	}
	if numericStringRE.MatchString(s) {
		return true
	}

	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
		switch r {
		case '\n', '\r', '\t':
			return true
		case ' ':
			return true
		case '"', '\\', ':', '{', '}', '[', ']':
			return true
		}
		if r == rune(delimiter) {
			return true
		}
	}
	return false
}

func quoteTOONString(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			// Support arbitrary control characters using JSON-style unicode escapes.
			// This prevents TOON formatting from failing and falling back to JSON.
			if r < 0x20 || r == 0x7f {
				b.WriteString(fmt.Sprintf(`\u%04x`, r))
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

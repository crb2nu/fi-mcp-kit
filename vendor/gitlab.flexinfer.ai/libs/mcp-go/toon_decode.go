package mcp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DecodeTOON parses a TOON-encoded string and returns the equivalent Go value.
//
// The returned value is one of:
//   - map[string]any (for objects)
//   - []any (for arrays)
//   - string, float64, bool, or nil (for primitives)
//
// All numbers are returned as float64 to match encoding/json conventions.
func DecodeTOON(input string) (any, error) {
	p := &toonParser{
		lines: splitLines(input),
	}
	return p.parse()
}

// DecodeTOONToJSON parses a TOON-encoded string and returns the equivalent JSON bytes.
func DecodeTOONToJSON(input string) ([]byte, error) {
	v, err := DecodeTOON(input)
	if err != nil {
		return nil, fmt.Errorf("decode TOON: %w", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal to JSON: %w", err)
	}
	return b, nil
}

// splitLines splits input into lines, preserving empty lines but trimming the
// trailing newline if present.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// The encoder does not produce a trailing newline, but be lenient.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type toonParser struct {
	lines []string
	pos   int
}

func (p *toonParser) parse() (any, error) {
	if len(p.lines) == 0 {
		return nil, fmt.Errorf("empty TOON input")
	}

	// Check if the first line is a bare primitive (root primitive value).
	firstLine := p.lines[0]
	if len(p.lines) == 1 && !strings.Contains(firstLine, ":") {
		return parsePrimitiveValue(strings.TrimSpace(firstLine))
	}

	// Check for a root-level single primitive: if the first line has no colon
	// at the top level (outside quotes), it might be a bare primitive.
	if len(p.lines) == 1 {
		trimmed := strings.TrimSpace(firstLine)
		colonIdx := findUnquotedColon(trimmed)
		if colonIdx < 0 {
			return parsePrimitiveValue(trimmed)
		}
	}

	// Root is an object: parse key-value pairs at indent level 0.
	return p.parseObject(0)
}

// parseObject parses key-value pairs at the given indentation level starting from p.pos.
func (p *toonParser) parseObject(indent int) (map[string]any, error) {
	obj := make(map[string]any)
	// Maintain insertion order for keys (Go maps don't, but JSON marshal will sort).
	// We use a plain map since the user expects map[string]any.

	for p.pos < len(p.lines) {
		line := p.lines[p.pos]

		// Skip completely empty lines.
		if strings.TrimSpace(line) == "" {
			p.pos++
			continue
		}

		lineIndent := measureIndent(line)

		// If this line is at a shallower indent than expected, we're done with this object.
		if lineIndent < indent {
			break
		}

		// If this line is deeper than expected, that's a structural error.
		if lineIndent > indent {
			return nil, fmt.Errorf("line %d: unexpected indentation (expected %d, got %d): %q", p.pos+1, indent, lineIndent, line)
		}

		trimmed := strings.TrimSpace(line)

		key, value, err := p.parseKeyValueLine(trimmed, indent)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", p.pos+1, err)
		}

		obj[key] = value
	}

	return obj, nil
}

// parseKeyValueLine parses a single key: value line and its potential children.
// It advances p.pos past any consumed lines.
func (p *toonParser) parseKeyValueLine(trimmed string, indent int) (string, any, error) {
	// Detect tabular array: key[N]{col1,col2,...}:
	if key, count, cols, ok := parseTabularHeader(trimmed); ok {
		p.pos++ // consume the header line
		arr, err := p.parseTabularRows(indent+1, cols, count)
		if err != nil {
			return "", nil, fmt.Errorf("tabular array %q: %w", key, err)
		}
		return key, arr, nil
	}

	// Detect inline primitive array: key[N]: val1,val2,...
	if key, _, ok := parseInlineArrayHeader(trimmed); ok {
		p.pos++ // consume the line
		return key, parseInlineArrayValues(trimmed), nil
	}

	// Detect list array header: key[N]:
	if key, count, ok := parseListArrayHeader(trimmed); ok {
		p.pos++ // consume the header line
		arr, err := p.parseListArray(indent+1, count)
		if err != nil {
			return "", nil, fmt.Errorf("list array %q: %w", key, err)
		}
		return key, arr, nil
	}

	// Regular key: value or key: (nested object)
	colonIdx := findKeyColon(trimmed)
	if colonIdx < 0 {
		return "", nil, fmt.Errorf("no key-value separator found: %q", trimmed)
	}

	rawKey := trimmed[:colonIdx]
	key := unquoteToken(rawKey)
	rest := trimmed[colonIdx+1:]

	if rest == "" || strings.TrimSpace(rest) == "" {
		// Nested object or empty object.
		p.pos++ // consume the header line

		// Check if there are child lines at indent+1.
		if p.pos < len(p.lines) {
			nextLine := p.lines[p.pos]
			if strings.TrimSpace(nextLine) != "" && measureIndent(nextLine) > indent {
				childObj, err := p.parseObject(indent + 1)
				if err != nil {
					return "", nil, fmt.Errorf("nested object %q: %w", key, err)
				}
				return key, childObj, nil
			}
		}

		// Empty object.
		return key, map[string]any{}, nil
	}

	// Simple key: value
	p.pos++
	valueStr := strings.TrimSpace(rest)
	val, err := parsePrimitiveValue(valueStr)
	if err != nil {
		return "", nil, fmt.Errorf("value for key %q: %w", key, err)
	}
	return key, val, nil
}

// parseTabularHeader detects lines like: key[N]{col1,col2,...}:
func parseTabularHeader(s string) (key string, count int, cols []string, ok bool) {
	// Find the pattern: something[digits]{...}:
	bracketOpen := strings.Index(s, "[")
	if bracketOpen < 0 {
		return "", 0, nil, false
	}

	bracketClose := strings.Index(s[bracketOpen:], "]")
	if bracketClose < 0 {
		return "", 0, nil, false
	}
	bracketClose += bracketOpen

	braceOpen := strings.Index(s[bracketClose:], "{")
	if braceOpen < 0 {
		return "", 0, nil, false
	}
	braceOpen += bracketClose

	// Brace must be immediately after bracket close.
	if braceOpen != bracketClose+1 {
		return "", 0, nil, false
	}

	braceClose := strings.Index(s[braceOpen:], "}")
	if braceClose < 0 {
		return "", 0, nil, false
	}
	braceClose += braceOpen

	// Must end with ":"
	if braceClose+1 >= len(s) || s[braceClose+1] != ':' {
		return "", 0, nil, false
	}

	// There must be nothing after the colon.
	remaining := strings.TrimSpace(s[braceClose+2:])
	if remaining != "" {
		return "", 0, nil, false
	}

	rawKey := s[:bracketOpen]
	key = unquoteToken(rawKey)

	countStr := s[bracketOpen+1 : bracketClose]
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return "", 0, nil, false
	}

	colStr := s[braceOpen+1 : braceClose]
	cols = strings.Split(colStr, ",")

	return key, n, cols, true
}

// parseInlineArrayHeader detects lines like: key[N]: val1,val2,...
func parseInlineArrayHeader(s string) (key string, count int, ok bool) {
	bracketOpen := strings.Index(s, "[")
	if bracketOpen < 0 {
		return "", 0, false
	}

	bracketClose := strings.Index(s[bracketOpen:], "]")
	if bracketClose < 0 {
		return "", 0, false
	}
	bracketClose += bracketOpen

	// Must not have a brace after bracket (that's tabular).
	if bracketClose+1 < len(s) && s[bracketClose+1] == '{' {
		return "", 0, false
	}

	// Must be followed by ": " with a value.
	if bracketClose+1 >= len(s) || s[bracketClose+1] != ':' {
		return "", 0, false
	}

	rest := s[bracketClose+2:]
	if strings.TrimSpace(rest) == "" {
		return "", 0, false // This is a list array header, not inline.
	}

	rawKey := s[:bracketOpen]
	key = unquoteToken(rawKey)

	countStr := s[bracketOpen+1 : bracketClose]
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return "", 0, false
	}

	return key, n, true
}

// parseInlineArrayValues parses the values from an inline array line.
func parseInlineArrayValues(s string) []any {
	// Find the ]: portion.
	bracketClose := strings.Index(s, "]")
	rest := strings.TrimSpace(s[bracketClose+2:])

	tokens := splitCSVValues(rest)
	result := make([]any, 0, len(tokens))
	for _, tok := range tokens {
		v, err := parsePrimitiveValue(strings.TrimSpace(tok))
		if err != nil {
			// Should not happen for well-formed TOON, but be lenient.
			result = append(result, strings.TrimSpace(tok))
			continue
		}
		result = append(result, v)
	}
	return result
}

// parseListArrayHeader detects lines like: key[N]:
func parseListArrayHeader(s string) (key string, count int, ok bool) {
	bracketOpen := strings.Index(s, "[")
	if bracketOpen < 0 {
		return "", 0, false
	}

	bracketClose := strings.Index(s[bracketOpen:], "]")
	if bracketClose < 0 {
		return "", 0, false
	}
	bracketClose += bracketOpen

	// Must not have a brace after bracket (that's tabular).
	if bracketClose+1 < len(s) && s[bracketClose+1] == '{' {
		return "", 0, false
	}

	// Must be followed by ":" and nothing else.
	if bracketClose+1 >= len(s) || s[bracketClose+1] != ':' {
		return "", 0, false
	}

	rest := strings.TrimSpace(s[bracketClose+2:])
	if rest != "" {
		return "", 0, false // Inline array, not list.
	}

	rawKey := s[:bracketOpen]
	key = unquoteToken(rawKey)

	countStr := s[bracketOpen+1 : bracketClose]
	n, err := strconv.Atoi(countStr)
	if err != nil {
		return "", 0, false
	}

	return key, n, true
}

// parseTabularRows parses the rows under a tabular array header.
func (p *toonParser) parseTabularRows(indent int, cols []string, _ int) ([]any, error) {
	var result []any

	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if strings.TrimSpace(line) == "" {
			p.pos++
			continue
		}

		lineIndent := measureIndent(line)
		if lineIndent < indent {
			break
		}

		trimmed := strings.TrimSpace(line)
		values := splitCSVValues(trimmed)

		if len(values) != len(cols) {
			return nil, fmt.Errorf("line %d: expected %d columns, got %d: %q", p.pos+1, len(cols), len(values), trimmed)
		}

		row := make(map[string]any, len(cols))
		for i, col := range cols {
			val, err := parsePrimitiveValue(strings.TrimSpace(values[i]))
			if err != nil {
				return nil, fmt.Errorf("line %d, column %q: %w", p.pos+1, col, err)
			}
			row[col] = val
		}

		result = append(result, row)
		p.pos++
	}

	if result == nil {
		result = []any{}
	}
	return result, nil
}

// parseListArray parses the items under a list array header (- item or - \n ...).
func (p *toonParser) parseListArray(indent int, _ int) ([]any, error) {
	var result []any

	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if strings.TrimSpace(line) == "" {
			p.pos++
			continue
		}

		lineIndent := measureIndent(line)
		if lineIndent < indent {
			break
		}

		trimmed := strings.TrimSpace(line)

		if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
			// Compatibility path: some producers emit object list items as
			// bare indented objects with no leading "-" marker (older mcp-go
			// encoders and cross-version hub payloads do this). The fields may
			// sit at the item indent, or one level deeper — the dash form's
			// field indent with the "-" line simply dropped (this is what the
			// live MCP hub emits). Parse the block at its actual indent,
			// splitting successive items on a repeated key, rather than
			// rejecting the whole document.
			obj, err := p.parseDashlessObjectItem(lineIndent)
			if err != nil {
				return nil, err
			}
			result = append(result, obj)
			continue
		}

		if trimmed == "-" {
			// Bare dash: the next indented block is the item value.
			p.pos++
			if p.pos < len(p.lines) {
				nextTrimmed := strings.TrimSpace(p.lines[p.pos])
				nextIndent := measureIndent(p.lines[p.pos])

				if nextIndent >= indent+1 && isArrayHeader(nextTrimmed) {
					// Nested array inside a list item.
					item, err := p.parseNestedArrayItem(indent + 1)
					if err != nil {
						return nil, err
					}
					result = append(result, item)
				} else if nextIndent >= indent+1 {
					// Nested object.
					childObj, err := p.parseObject(indent + 1)
					if err != nil {
						return nil, err
					}
					result = append(result, childObj)
				} else {
					// Bare dash with nothing following at the right indent.
					// Treat as empty object (unlikely but be safe).
					result = append(result, map[string]any{})
				}
			} else {
				result = append(result, map[string]any{})
			}
		} else {
			// "- value"
			p.pos++
			valueStr := strings.TrimSpace(trimmed[2:])
			val, err := parsePrimitiveValue(valueStr)
			if err != nil {
				return nil, fmt.Errorf("line %d: list item value: %w", p.pos, err)
			}
			result = append(result, val)
		}
	}

	if result == nil {
		result = []any{}
	}
	return result, nil
}

// parseDashlessObjectItem parses a single list item written as a bare indented
// object (no leading "-"). It consumes key/value lines at exactly indent (with
// their nested children) and stops when the block dedents, a dashed item
// begins, or a top-level key it already parsed reappears — the last case marks
// the start of the next item. Object keys are unique within an item, so a
// repeat unambiguously delimits the boundary for the common case of a
// homogeneous array of objects.
func (p *toonParser) parseDashlessObjectItem(indent int) (map[string]any, error) {
	obj := make(map[string]any)

	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if strings.TrimSpace(line) == "" {
			p.pos++
			continue
		}

		lineIndent := measureIndent(line)
		if lineIndent < indent {
			break // block ended
		}
		if lineIndent > indent {
			return nil, fmt.Errorf("line %d: unexpected indentation (expected %d, got %d): %q", p.pos+1, indent, lineIndent, line)
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			break // a dashed item begins; let the caller resume
		}

		// A repeated top-level key means the next item has started.
		if len(obj) > 0 {
			if key, ok := listItemKey(trimmed); ok {
				if _, seen := obj[key]; seen {
					break
				}
			}
		}

		key, value, err := p.parseKeyValueLine(trimmed, indent)
		if err != nil {
			return nil, err
		}
		obj[key] = value
	}

	return obj, nil
}

// listItemKey extracts the object key a TOON line introduces, for boundary
// detection in parseDashlessObjectItem. It mirrors parseKeyValueLine's key
// derivation across the simple key:value and array-header (key[N]...) forms.
func listItemKey(trimmed string) (string, bool) {
	if colon := findKeyColon(trimmed); colon >= 0 {
		return unquoteToken(trimmed[:colon]), true
	}
	// Array-header forms (key[N]:, key[N]{...}:, key[N]: vals): findKeyColon
	// returns -1 because it stops at '['. The key is the token before '['.
	if bracket := strings.Index(trimmed, "["); bracket > 0 {
		if strings.Contains(trimmed[bracket:], "]") {
			return unquoteToken(trimmed[:bracket]), true
		}
	}
	return "", false
}

// parseNestedArrayItem handles array items inside list items (e.g., nested arrays).
func (p *toonParser) parseNestedArrayItem(indent int) (any, error) {
	trimmed := strings.TrimSpace(p.lines[p.pos])

	// It could be a tabular array, inline array, or list array.
	if key, count, cols, ok := parseTabularHeader(trimmed); ok {
		p.pos++
		arr, err := p.parseTabularRows(indent+1, cols, count)
		if err != nil {
			return nil, fmt.Errorf("nested tabular array %q: %w", key, err)
		}
		// Wrap in a map since the line has a key.
		return map[string]any{key: arr}, nil
	}

	if key, _, ok := parseInlineArrayHeader(trimmed); ok {
		p.pos++
		values := parseInlineArrayValues(trimmed)
		return map[string]any{key: values}, nil
	}

	if key, count, ok := parseListArrayHeader(trimmed); ok {
		p.pos++
		arr, err := p.parseListArray(indent+1, count)
		if err != nil {
			return nil, fmt.Errorf("nested list array %q: %w", key, err)
		}
		return map[string]any{key: arr}, nil
	}

	// Fall through to parsing as an object.
	obj, err := p.parseObject(indent)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

// isArrayHeader checks if a trimmed line looks like an array header.
func isArrayHeader(s string) bool {
	bracketOpen := strings.Index(s, "[")
	if bracketOpen < 0 {
		return false
	}
	bracketClose := strings.Index(s[bracketOpen:], "]")
	return bracketClose >= 0
}

// splitCSVValues splits a comma-separated string, respecting quoted values.
func splitCSVValues(s string) []string {
	var result []string
	var current strings.Builder
	inQuotes := false
	i := 0

	for i < len(s) {
		ch := s[i]

		if ch == '"' && !inQuotes {
			inQuotes = true
			current.WriteByte(ch)
			i++
			continue
		}

		if inQuotes {
			if ch == '\\' && i+1 < len(s) {
				current.WriteByte(ch)
				i++
				current.WriteByte(s[i])
				i++
				continue
			}
			if ch == '"' {
				current.WriteByte(ch)
				inQuotes = false
				i++
				continue
			}
			current.WriteByte(ch)
			i++
			continue
		}

		if ch == ',' {
			result = append(result, current.String())
			current.Reset()
			i++
			continue
		}

		current.WriteByte(ch)
		i++
	}

	result = append(result, current.String())
	return result
}

// parsePrimitiveValue parses a TOON token into its Go equivalent.
func parsePrimitiveValue(s string) (any, error) {
	if s == "" {
		return "", nil
	}

	// Handle null.
	if s == "null" {
		return nil, nil
	}

	// Handle booleans.
	if s == "true" {
		return true, nil
	}
	if s == "false" {
		return false, nil
	}

	// Handle quoted strings.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return unquoteTOONString(s)
	}

	// Handle numbers.
	if numericStringRE.MatchString(s) {
		// Try integer first.
		if !strings.Contains(s, ".") && !strings.ContainsAny(s, "eE") {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return float64(n), nil
			}
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, nil
		}
	}

	// Unquoted string.
	return s, nil
}

// unquoteToken unquotes a key or value token: if quoted, parse as TOON string; otherwise return as-is.
func unquoteToken(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		unquoted, err := unquoteTOONString(s)
		if err != nil {
			return s
		}
		return unquoted
	}
	return s
}

// unquoteTOONString parses a TOON quoted string (with JSON-style escapes) and returns the Go string.
func unquoteTOONString(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("not a quoted string: %q", s)
	}

	inner := s[1 : len(s)-1]
	var b strings.Builder
	b.Grow(len(inner))

	i := 0
	for i < len(inner) {
		if inner[i] == '\\' {
			if i+1 >= len(inner) {
				return "", fmt.Errorf("trailing backslash in string: %q", s)
			}
			switch inner[i+1] {
			case '"':
				b.WriteByte('"')
				i += 2
			case '\\':
				b.WriteByte('\\')
				i += 2
			case 'n':
				b.WriteByte('\n')
				i += 2
			case 'r':
				b.WriteByte('\r')
				i += 2
			case 't':
				b.WriteByte('\t')
				i += 2
			case 'b':
				b.WriteByte('\b')
				i += 2
			case 'f':
				b.WriteByte('\f')
				i += 2
			case 'u':
				if i+5 >= len(inner) {
					return "", fmt.Errorf("incomplete unicode escape in string: %q", s)
				}
				hexStr := inner[i+2 : i+6]
				codepoint, err := strconv.ParseUint(hexStr, 16, 32)
				if err != nil {
					return "", fmt.Errorf("invalid unicode escape \\u%s in string: %q", hexStr, s)
				}
				r := rune(codepoint)

				// Handle UTF-16 surrogate pairs.
				if r >= 0xD800 && r <= 0xDBFF {
					// High surrogate, expect \uXXXX low surrogate.
					if i+11 >= len(inner) || inner[i+6] != '\\' || inner[i+7] != 'u' {
						return "", fmt.Errorf("missing low surrogate after high surrogate in string: %q", s)
					}
					lowHex := inner[i+8 : i+12]
					lowCP, err := strconv.ParseUint(lowHex, 16, 32)
					if err != nil {
						return "", fmt.Errorf("invalid low surrogate \\u%s in string: %q", lowHex, s)
					}
					low := rune(lowCP)
					if low < 0xDC00 || low > 0xDFFF {
						return "", fmt.Errorf("invalid low surrogate U+%04X in string: %q", low, s)
					}
					combined := 0x10000 + (r-0xD800)*0x400 + (low - 0xDC00)
					b.WriteRune(combined)
					i += 12
				} else {
					b.WriteRune(r)
					i += 6
				}
			default:
				// Unknown escape: keep as-is for leniency.
				b.WriteByte('\\')
				b.WriteByte(inner[i+1])
				i += 2
			}
		} else {
			r, size := utf8.DecodeRuneInString(inner[i:])
			b.WriteRune(r)
			i += size
		}
	}

	return b.String(), nil
}

// measureIndent returns the indentation level (number of 2-space units).
func measureIndent(line string) int {
	spaces := 0
	for _, ch := range line {
		if ch == ' ' {
			spaces++
		} else {
			break
		}
	}
	return spaces / 2
}

// findKeyColon finds the colon that separates a key from its value, respecting quoted keys.
// Returns the index of the colon, or -1 if not found.
func findKeyColon(s string) int {
	inQuotes := false
	i := 0

	for i < len(s) {
		ch := s[i]

		if ch == '"' && !inQuotes {
			inQuotes = true
			i++
			continue
		}

		if inQuotes {
			if ch == '\\' && i+1 < len(s) {
				i += 2 // skip escaped character
				continue
			}
			if ch == '"' {
				inQuotes = false
				i++
				continue
			}
			i++
			continue
		}

		if ch == ':' {
			return i
		}

		// If we hit '[', this might be an array header, not a simple key.
		if ch == '[' {
			return -1
		}

		i++
	}

	return -1
}

// findUnquotedColon finds any colon outside quotes. Used for root-level primitive detection.
func findUnquotedColon(s string) int {
	inQuotes := false
	i := 0

	for i < len(s) {
		ch := s[i]

		if ch == '"' && !inQuotes {
			inQuotes = true
			i++
			continue
		}

		if inQuotes {
			if ch == '\\' && i+1 < len(s) {
				i += 2
				continue
			}
			if ch == '"' {
				inQuotes = false
				i++
				continue
			}
			i++
			continue
		}

		if ch == ':' {
			return i
		}

		i++
	}

	return -1
}

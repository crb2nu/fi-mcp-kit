package gateway

import (
	"regexp"
)

// Redactor provides functionality to scrub sensitive information from strings/bytes.
type Redactor struct {
	patterns []*regexp.Regexp
}

// NewRedactor creates a new redactor with default patterns for common secrets.
func NewRedactor() *Redactor {
	return &Redactor{
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)sk-[a-zA-Z0-9]{20,}`),             // Anthropic/OpenAI style keys
			regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9\-\._~+/]+=*`), // Bearer tokens
			regexp.MustCompile(`(?i)ghp_[a-zA-Z0-9]{36}`),             // Github PATs
		},
	}
}

// AddPattern adds a custom regex pattern to be redacted.
func (r *Redactor) AddPattern(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	r.patterns = append(r.patterns, re)
	return nil
}

// Redact replaces all matched sensitive patterns with "[REDACTED]".
func (r *Redactor) Redact(input []byte) []byte {
	out := input
	for _, re := range r.patterns {
		out = re.ReplaceAll(out, []byte("[REDACTED]"))
	}
	return out
}

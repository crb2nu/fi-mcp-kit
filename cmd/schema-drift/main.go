// Command schema-drift compares vendored upstream config schemas in
// pkg/validator/schemas/ against their live upstream URLs, so a vendored
// copy can't silently lag the vendor (which produces false validation
// warnings on every generated config).
//
// Usage:
//
//	go run ./cmd/schema-drift [platform ...]
//
// With no arguments, all platforms from validator.UpstreamSchemas() are
// checked. Exits non-zero when any checked schema differs from upstream
// after JSON normalization (sorted keys, stable indentation).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/validator"
)

func main() {
	requested := map[string]bool{}
	for _, arg := range os.Args[1:] {
		requested[arg] = true
	}

	client := &http.Client{Timeout: 30 * time.Second}
	drifted := 0
	checked := 0

	for _, info := range validator.UpstreamSchemas() {
		if len(requested) > 0 && !requested[info.Platform] {
			continue
		}
		checked++
		if err := checkSchema(client, info); err != nil {
			fmt.Fprintf(os.Stderr, "DRIFT %s (%s): %v\n", info.Platform, info.Name, err)
			fmt.Fprintf(os.Stderr, "  refresh with: curl -sL %s | <normalize> > pkg/validator/schemas/%s\n", info.URL, info.Name)
			drifted++
			continue
		}
		fmt.Printf("OK %s (%s) matches %s\n", info.Platform, info.Name, info.URL)
	}

	if checked == 0 {
		fmt.Fprintf(os.Stderr, "no matching platforms for args %v\n", os.Args[1:])
		os.Exit(2)
	}
	if drifted > 0 {
		os.Exit(1)
	}
}

func checkSchema(client *http.Client, info validator.UpstreamSchemaInfo) error {
	vendored, ok := validator.GetEmbeddedSchema(info.Name)
	if !ok {
		return fmt.Errorf("no embedded schema named %s", info.Name)
	}

	resp, err := client.Get(info.URL)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", info.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %d", info.URL, resp.StatusCode)
	}
	live, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read %s: %w", info.URL, err)
	}

	vendoredNorm, err := normalizeJSON(vendored)
	if err != nil {
		return fmt.Errorf("normalize vendored: %w", err)
	}
	liveNorm, err := normalizeJSON(live)
	if err != nil {
		return fmt.Errorf("normalize live: %w", err)
	}

	if !bytes.Equal(vendoredNorm, liveNorm) {
		return fmt.Errorf("vendored schema differs from upstream")
	}
	return nil
}

// normalizeJSON re-marshals JSON with sorted object keys and stable
// indentation so byte comparison ignores formatting differences.
func normalizeJSON(data []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

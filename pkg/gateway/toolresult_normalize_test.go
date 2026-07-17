package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNormalizeToolResultText(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		// wantText is the expected content[0].text after normalization.
		wantText string
		// wantUnchanged asserts the raw bytes are returned untouched.
		wantUnchanged bool
	}{
		{
			name: "TOON object becomes JSON object text",
			raw: `{"content":[{"type":"text","text":"account_id: abc\nactive: true\ndisplay_name: Cody"}],` +
				`"isError":false,"structuredContent":{"kept":true}}`,
			wantText: `{"account_id":"abc","active":true,"display_name":"Cody"}`,
		},
		{
			name:     "TOON tabular array becomes JSON array text",
			raw:      `{"content":[{"type":"text","text":"items[2]{a,b}:\n  x,y\n  z,w"}],"isError":false}`,
			wantText: `[{"a":"x","b":"y"},{"a":"z","b":"w"}]`,
		},
		{
			name:          "already-JSON text passes through untouched",
			raw:           `{"content":[{"type":"text","text":"{\"hits\":[{\"key\":\"ICC-1\"}]}"}],"isError":false}`,
			wantUnchanged: true,
		},
		{
			name:          "plain prose passes through untouched",
			raw:           `{"content":[{"type":"text","text":"Transited issue ICC-1 to Done"}],"isError":false}`,
			wantUnchanged: true,
		},
		{
			name:          "isError result passes through untouched",
			raw:           `{"content":[{"type":"text","text":"code: 500\nreason: boom"}],"isError":true}`,
			wantUnchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeToolResultText(json.RawMessage(tt.raw))

			if tt.wantUnchanged {
				if string(got) != tt.raw {
					t.Fatalf("expected raw passthrough,\n got: %s\nwant: %s", got, tt.raw)
				}
				return
			}

			var result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError           bool           `json:"isError"`
				StructuredContent map[string]any `json:"structuredContent"`
			}
			if err := json.Unmarshal(got, &result); err != nil {
				t.Fatalf("normalized result is not JSON: %v\n%s", err, got)
			}
			if len(result.Content) != 1 {
				t.Fatalf("expected 1 content item, got %d", len(result.Content))
			}
			if result.Content[0].Text != tt.wantText {
				t.Errorf("content text = %s, want %s", result.Content[0].Text, tt.wantText)
			}
			if result.IsError {
				t.Error("isError flipped to true during normalization")
			}
			if strings.Contains(tt.raw, "structuredContent") && result.StructuredContent["kept"] != true {
				t.Errorf("unknown sibling field dropped during normalization: %s", got)
			}
		})
	}
}

func TestToonTextToJSON_MultipleContentItems(t *testing.T) {
	raw := `{"content":[` +
		`{"type":"text","text":"a: 1"},` +
		`{"type":"text","text":"plain prose"},` +
		`{"type":"image","data":"xxx","mimeType":"image/png"}` +
		`],"isError":false}`

	got := normalizeToolResultText(json.RawMessage(raw))

	var result struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatalf("normalized result is not JSON: %v", err)
	}
	if result.Content[0]["text"] != `{"a":1}` {
		t.Errorf("TOON item not normalized: %v", result.Content[0]["text"])
	}
	if result.Content[1]["text"] != "plain prose" {
		t.Errorf("prose item changed: %v", result.Content[1]["text"])
	}
	if result.Content[2]["data"] != "xxx" || result.Content[2]["mimeType"] != "image/png" {
		t.Errorf("non-text item changed: %v", result.Content[2])
	}
}

// TestToolCallHandler_NormalizesTOONTextToJSON exercises the REST path
// end-to-end: a backend emitting TOON text (the loom-hub default,
// LOOM_MCP_OUTPUT_FORMAT=toon) must be served to REST consumers as JSON
// text they can json-parse (reference consumer: ICC connectors.py).
func TestToolCallHandler_NormalizesTOONTextToJSON(t *testing.T) {
	fb := startFakeToolBackend(t, map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "account_id: abc\nactive: true"},
		},
		"isError": false,
	})

	hub := NewHub()
	hub.Registry = toolCallTestRegistry(fb.wsURL())
	hub.ToolCallTimeout = 5 * time.Second

	srv := newToolCallServer(t, hub)
	resp, body := postToolCall(t, srv.URL, "/api/v1/tools/atlassian/jira_search", `{"jql":"x"}`)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(result.Content))
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("content text is not json-parseable (still TOON?): %v\ntext: %s", err, result.Content[0].Text)
	}
	if payload["account_id"] != "abc" || payload["active"] != true {
		t.Errorf("unexpected normalized payload: %v", payload)
	}
}

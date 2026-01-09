package router

import (
	"testing"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

func TestRouter_ResolveServer(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{
				Name:       "fs-ssd",
				Categories: []string{"hub"},
				Common: &registry.TargetSpec{
					Tools: []registry.ToolSchema{
						{Name: "read_file"},
					},
				},
			},
			{
				Name:       "fs-hdd",
				Categories: []string{"hub"},
				Common: &registry.TargetSpec{
					Tools: []registry.ToolSchema{
						{Name: "read_file"},
					},
				},
			},
			{
				Name:       "git",
				Categories: []string{"hub"},
				Common: &registry.TargetSpec{
					Tools: []registry.ToolSchema{
						{Name: "status"},
					},
				},
			},
		},
		Routing: []*registry.RoutingRule{
			{
				ToolName: "read_file",
				Argument: "path",
				Cases: []registry.RoutingCase{
					{Match: "/ssd/*", Server: "fs-ssd"},
					{Match: "/hdd/*", Server: "fs-hdd"},
				},
				Default: "fs-hdd",
			},
		},
	}

	r := New(Config{Registry: reg})

	tests := []struct {
		name     string
		tool     string
		args     map[string]any
		want     string
		wantErr  bool
	}{
		{
			name: "Unique tool (smart routing)",
			tool: "status",
			want: "git",
		},
		{
			name: "Prefix routing",
			tool: "git__status",
			want: "git",
		},
		{
			name: "Argument routing: /ssd path",
			tool: "read_file",
			args: map[string]any{"path": "/ssd/data.txt"},
			want: "fs-ssd",
		},
		{
			name: "Argument routing: /hdd path",
			tool: "read_file",
			args: map[string]any{"path": "/hdd/logs.log"},
			want: "fs-hdd",
		},
		{
			name: "Argument routing: default fallback",
			tool: "read_file",
			args: map[string]any{"path": "/unknown/file"},
			want: "fs-hdd",
		},
		{
			name:    "Ambiguous tool without routing rule",
			tool:    "read_file",
			args:    nil, // Missing args for routing rule match (assuming arg check is strict)
			// Wait, if args missing, routing rule won't match cases. It hits default.
			// If no default was set, it would fall through to tool index check which would fail ambiguity.
			want:    "fs-hdd", 
		},
		{
			name:    "Unknown tool",
			tool:    "unknown",
			want:    "",
			wantErr: false, // Just returns empty string if not found
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.ResolveServer("common", tt.tool, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveServer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ResolveServer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRouter_Ambiguity(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{Name: "s1", Common: &registry.TargetSpec{Tools: []registry.ToolSchema{{Name: "ping"}}}},
			{Name: "s2", Common: &registry.TargetSpec{Tools: []registry.ToolSchema{{Name: "ping"}}}},
		},
		// No routing rules
	}

	r := New(Config{Registry: reg})

	_, err := r.ResolveServer("common", "ping", nil)
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
}

package registry

import (
	_ "embed"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

//go:embed tool_metadata.yaml
var embeddedMetadata []byte

type ToolMetadata struct {
	UsageHint    string   `yaml:"usageHint"`
	Example      string   `yaml:"example"`
	RelatedTools []string `yaml:"relatedTools"`
	Priority     int      `yaml:"priority"`
}

type ServerMetadata struct {
	Description string                  `yaml:"description"`
	Category    string                  `yaml:"category"`
	Tools       map[string]ToolMetadata `yaml:"tools"`
}

type CategoryMetadata struct {
	Description string   `yaml:"description"`
	Servers     []string `yaml:"servers"`
}

type Metadata struct {
	Version    int                         `yaml:"version"`
	Servers    map[string]ServerMetadata   `yaml:"servers"`
	Categories map[string]CategoryMetadata `yaml:"categories"`
}

func LoadMetadata(path string) (*Metadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var meta Metadata
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

func LoadMetadataFromDir(dir string) (*Metadata, error) {
	return LoadMetadata(filepath.Join(dir, "tool_metadata.yaml"))
}

func LoadEmbeddedMetadata() (*Metadata, error) {
	var meta Metadata
	if err := yaml.Unmarshal(embeddedMetadata, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (m *Metadata) GetToolMetadata(serverName, toolName string) *ToolMetadata {
	if server, ok := m.Servers[serverName]; ok {
		if tool, ok := server.Tools[toolName]; ok {
			return &tool
		}
	}
	return nil
}

func (m *Metadata) GetServerMetadata(serverName string) *ServerMetadata {
	if server, ok := m.Servers[serverName]; ok {
		return &server
	}
	return nil
}

func (m *Metadata) GetCategoryServers(category string) []string {
	if cat, ok := m.Categories[category]; ok {
		return cat.Servers
	}
	return nil
}

func (m *Metadata) EnhanceDescription(serverName, toolName, originalDesc string) string {
	meta := m.GetToolMetadata(serverName, toolName)
	if meta == nil || meta.UsageHint == "" {
		return originalDesc
	}

	if originalDesc != "" {
		return originalDesc + " | Hint: " + meta.UsageHint
	}
	return meta.UsageHint
}

module gitlab.flexinfer.ai/libs/fi-mcp-kit

go 1.24.0

require (
	gitlab.flexinfer.ai/libs/mcp-go v0.1.0
	golang.org/x/crypto v0.36.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3
)

replace gitlab.flexinfer.ai/libs/mcp-go => ../mcp-go

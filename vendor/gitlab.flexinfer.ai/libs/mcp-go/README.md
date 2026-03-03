![Banner](assets/banner.png)
# mcp-go

![Header](assets/header.svg)

[![pipeline status](https://gitlab.flexinfer.ai/libs/mcp-go/badges/main/pipeline.svg)](https://gitlab.flexinfer.ai/libs/mcp-go/-/commits/main)
[![coverage report](https://gitlab.flexinfer.ai/libs/mcp-go/badges/main/coverage.svg)](https://gitlab.flexinfer.ai/libs/mcp-go/-/commits/main)

A Go SDK for building [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) servers.

MCP is a protocol for connecting AI assistants to external tools and data sources. This library provides everything you need to build MCP servers in Go.

## Features

- **Complete MCP Protocol** - Full JSON-RPC 2.0 + MCP type definitions
- **Server Framework** - Simple API for registering tools and handling requests
- **Multiple Transports** - Stdio (newline-delimited + Content-Length) and WebSocket
- **Connection Pool** - Manage multiple MCP server connections with idle reaping
- **Production Ready** - Battle-tested with 26+ production MCP servers
- **Examples** - Echo server, file server, and more in `examples/`

## Installation

```bash
go get gitlab.flexinfer.ai/libs/mcp-go
```

## Quick Start

```go
package main

import (
    "context"
    "os"
    "os/signal"
    "syscall"

    "gitlab.flexinfer.ai/libs/mcp-go"
)

func main() {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Handle shutdown signals
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigCh
        cancel()
    }()

    // Create server
    server := mcp.NewServer("my-server", "1.0.0")
    server.SetInstructions("A simple MCP server example")

    // Register a tool
    server.AddTool(mcp.Tool{
        Name:        "hello",
        Description: "Says hello to the given name",
        InputSchema: mcp.InputSchema{
            Type: "object",
            Properties: map[string]any{
                "name": map[string]any{
                    "type":        "string",
                    "description": "Name to greet",
                },
            },
            Required: []string{"name"},
        },
    }, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
        name, _ := args["name"].(string)
        return mcp.TextResult("Hello, " + name + "!"), nil
    })

    // Run server (blocks until context is cancelled)
    if err := server.Run(ctx); err != nil {
        os.Exit(1)
    }
}
```

## API Reference

### Server

```go
// Create a new server
server := mcp.NewServer("name", "version")

// Set server instructions (shown to clients)
server.SetInstructions("Description of what this server does")

// Register a tool
server.AddTool(tool, handler)

// Run the server on stdio
server.Run(ctx)
```

### Tool Definition

```go
// Manual definition
mcp.Tool{
    Name:        "tool_name",
    Description: "What this tool does",
    InputSchema: mcp.InputSchema{
        Type: "object",
        Properties: map[string]any{
            "param1": map[string]any{
                "type":        "string",
                "description": "Description of param1",
            },
        },
        Required: []string{"param1"},
    },
}

// Fluent definition (Recommended)
tool := mcp.NewTool("tool_name", "What this tool does").
    WithString("param1", "Description of param1", true).
    WithNumber("param2", "Optional number", false).
    Build()
```

### Result Helpers

```go
// Simple text result
mcp.TextResult("Hello, world!")

// JSON result (pretty-printed)
mcp.JSONResult(map[string]any{"key": "value"})

// Error result
mcp.ErrorResult(err)
```

### Transports

#### Stdio Transport (Default)

The server uses stdio transport by default. It supports both:

- Newline-delimited JSON (MCP standard)
- Content-Length framing (LSP-style)

#### WebSocket Transport

For connecting to remote MCP servers:

```go
cfg := mcp.WebSocketConfig{
    URL:        "wss://mcp-hub.example.com/ws",
    ClientInfo: mcp.ClientInfo{Name: "my-client", Version: "1.0.0"},
}

transport, err := mcp.NewWebSocketTransport(ctx, cfg, "server-name")
if err != nil {
    log.Fatal(err)
}
defer transport.Close()

// Initialize the MCP connection
if err := transport.Initialize(ctx); err != nil {
    log.Fatal(err)
}

// Send/receive messages
transport.Send(ctx, msg)
msg, err := transport.Recv(ctx)
```

#### Pipe Transport (Testing)

For unit tests:

```go
client, server := mcp.NewPipeTransport()

// Messages sent on client are received on server and vice versa
client.Send(ctx, msg)
msg, _ := server.Recv(ctx)
```

## Protocol Types

The package exports all MCP protocol types:

- `Message` - JSON-RPC message
- `Error` - JSON-RPC error
- `Tool`, `InputSchema` - Tool definitions
- `CallToolParams`, `CallToolResult` - Tool call request/response
- `Resource`, `Prompt` - Resource and prompt definitions
- `Capabilities`, `ClientInfo`, `ServerInfo` - Initialization types

## License

MIT License - see [LICENSE](LICENSE)

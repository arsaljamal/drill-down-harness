// Command demoserver is a tiny MCP server over stdio for trying Keel.
// It exposes a read-only "add" tool and an unsafe "notify" tool that only
// pretends to send a message.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type addIn struct {
	A float64 `json:"a" jsonschema:"first number"`
	B float64 `json:"b" jsonschema:"second number"`
}

type notifyIn struct {
	To   string `json:"to" jsonschema:"recipient"`
	Text string `json:"text" jsonschema:"message text"`
}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "keel-demo", Version: "0.1.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "add",
		Description: "Add two numbers.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprint(in.A + in.B)}}}, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "notify",
		Description: "Send a notification (demo: does not really send anything).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in notifyIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("pretend-sent to %s: %s", in.To, in.Text)}}}, nil, nil
	})

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

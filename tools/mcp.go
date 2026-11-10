// Package tools connects the harness to tools. v0.1 supports MCP servers.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arsaljamal/drill-down-harness/agent"
)

// SideEffect classifies what a tool call can change. It drives retry and
// approval decisions (used from v0.2).
type SideEffect string

const (
	ReadOnly   SideEffect = "read_only"
	Idempotent SideEffect = "idempotent"
	Unsafe     SideEffect = "unsafe"
)

// Classify derives a side-effect class from MCP tool annotations. A tool
// without annotations is treated as unsafe: guessing wrong in that direction
// costs an approval prompt, not a duplicate side effect.
func Classify(t *mcp.Tool) SideEffect {
	a := t.Annotations
	switch {
	case a == nil:
		return Unsafe
	case a.ReadOnlyHint:
		return ReadOnly
	case a.IdempotentHint:
		return Idempotent
	default:
		return Unsafe
	}
}

// MCP is a toolbox backed by one MCP client session.
type MCP struct {
	session *mcp.ClientSession
	classes map[string]SideEffect
}

// ConnectMCP connects to an MCP server over the given transport, for example
// &mcp.CommandTransport{Command: exec.Command("my-server")}.
func ConnectMCP(ctx context.Context, t mcp.Transport) (*MCP, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "drill-down-harness", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server: %w", err)
	}
	return &MCP{session: session, classes: map[string]SideEffect{}}, nil
}

// Close ends the MCP session.
func (m *MCP) Close() error { return m.session.Close() }

// Class returns the side-effect class of a listed tool.
func (m *MCP) Class(name string) SideEffect {
	if c, ok := m.classes[name]; ok {
		return c
	}
	return Unsafe
}

// Tools implements agent.Toolbox.
func (m *MCP) Tools(ctx context.Context) ([]agent.ToolSpec, error) {
	var specs []agent.ToolSpec
	for t, err := range m.session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		schema, err := schemaMap(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %q: %w", t.Name, err)
		}
		m.classes[t.Name] = Classify(t)
		specs = append(specs, agent.ToolSpec{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return specs, nil
}

// Call implements agent.Toolbox.
func (m *MCP) Call(ctx context.Context, name string, input json.RawMessage) (agent.ToolResult, error) {
	var args any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return agent.ToolResult{}, fmt.Errorf("decode tool input: %w", err)
		}
	}
	res, err := m.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return agent.ToolResult{}, err
	}
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		parts = append(parts, string(b))
	}
	return agent.ToolResult{Text: strings.Join(parts, "\n"), IsError: res.IsError}, nil
}

// schemaMap turns whatever the SDK holds as a schema into a plain map.
func schemaMap(s any) (map[string]any, error) {
	if s == nil {
		return map[string]any{"type": "object"}, nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

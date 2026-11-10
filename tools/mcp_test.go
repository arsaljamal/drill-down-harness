package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type addIn struct {
	A float64 `json:"a"`
	B float64 `json:"b"`
}

func TestMCPListAndCall(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "add", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, any, error) {
			b, _ := json.Marshal(in.A + in.B)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "send"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "sent"}}}, nil, nil
		})

	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	box, err := ConnectMCP(ctx, clientT)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()

	specs, err := box.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("want 2 tools, got %d", len(specs))
	}
	if box.Class("add") != ReadOnly || box.Class("send") != Unsafe {
		t.Fatalf("classes: add=%s send=%s", box.Class("add"), box.Class("send"))
	}

	out, err := box.Call(ctx, "add", json.RawMessage(`{"a":2,"b":3}`))
	if err != nil || out.IsError || out.Text != "5" {
		t.Fatalf("add: %+v %v", out, err)
	}
}

package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/arsaljamal/keel/agent"
)

func TestRetryable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&anthropic.Error{StatusCode: 429}, true},
		{&anthropic.Error{StatusCode: 529}, true},
		{&anthropic.Error{StatusCode: 500}, true},
		{&anthropic.Error{StatusCode: 400}, false},
		{&anthropic.Error{StatusCode: 401}, false},
		{fmt.Errorf("wrapped: %w", &anthropic.Error{StatusCode: 503}), true},
		{errors.New("some other failure"), false},
	}
	for _, c := range cases {
		if got := Retryable(c.err); got != c.want {
			t.Errorf("Retryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestToParamsKeepsToolRoundTrip(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Blocks: []agent.Block{{Kind: agent.BlockText, Text: "add 2 and 3"}}},
		{Role: agent.RoleAssistant, Blocks: []agent.Block{{Kind: agent.BlockToolUse, ToolUseID: "tu_1", ToolName: "add", Input: json.RawMessage(`{"a":2,"b":3}`)}}},
		{Role: agent.RoleUser, Blocks: []agent.Block{{Kind: agent.BlockToolResult, ToolUseID: "tu_1", Text: "5"}}},
	}
	b, err := json.Marshal(toParams(msgs))
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got[1].Role != "assistant" || got[1].Content[0].Type != "tool_use" || got[1].Content[0].ID != "tu_1" || got[1].Content[0].Name != "add" {
		t.Fatalf("assistant tool_use not encoded: %s", b)
	}
	if got[2].Content[0].Type != "tool_result" || got[2].Content[0].ToolUseID != "tu_1" {
		t.Fatalf("tool_result not encoded: %s", b)
	}
}

func TestToToolsCopiesSchema(t *testing.T) {
	tools := toTools([]agent.ToolSpec{{
		Name:        "add",
		Description: "Add two numbers.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"a": map[string]any{"type": "number"}},
			"required":   []any{"a"},
		},
	}})
	b, _ := json.Marshal(tools)
	var got []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			Type       string         `json:"type"`
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		} `json:"input_schema"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	s := got[0].InputSchema
	if got[0].Name != "add" || s.Type != "object" || s.Properties["a"] == nil || len(s.Required) != 1 {
		t.Fatalf("schema not copied: %s", b)
	}
}

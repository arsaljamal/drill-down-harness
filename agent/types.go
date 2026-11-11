// Package agent contains the Drill Down Harness's provider-neutral types and the agent loop.
package agent

import (
	"context"
	"encoding/json"
)

// Role is who wrote a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockKind is the type of a content block.
type BlockKind string

const (
	BlockText       BlockKind = "text"
	BlockToolUse    BlockKind = "tool_use"
	BlockToolResult BlockKind = "tool_result"
)

// Block is one piece of a message: text, a tool call, or a tool result.
type Block struct {
	Kind BlockKind `json:"kind"`
	Text string    `json:"text,omitempty"`

	// Tool call and tool result fields.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`

	// Meta is opaque provider data attached to a tool call (for example
	// Gemini's thought signature). Adapters must send it back unchanged.
	Meta json.RawMessage `json:"meta,omitempty"`
}

// Message is one turn in the conversation.
type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
}

// ToolSpec describes a tool to the model.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// Usage counts tokens for one or more model calls.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Total returns input plus output tokens.
func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens }

func (u *Usage) add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
}

// ModelRequest is what the runner sends to a model.
type ModelRequest struct {
	System    string
	Messages  []Message
	Tools     []ToolSpec
	MaxTokens int64
}

// ModelReply is what a model returns for one call.
type ModelReply struct {
	Blocks     []Block
	StopReason string
	Usage      Usage
}

// Model generates the next assistant turn.
type Model interface {
	Generate(ctx context.Context, req ModelRequest) (ModelReply, error)
}

// ToolResult is the outcome of one tool call.
type ToolResult struct {
	Text    string
	IsError bool
}

// Toolbox lists and executes tools.
type Toolbox interface {
	Tools(ctx context.Context) ([]ToolSpec, error)
	Call(ctx context.Context, name string, input json.RawMessage) (ToolResult, error)
}

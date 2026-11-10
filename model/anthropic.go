// Package model adapts model APIs to Keel's agent.Model interface.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/arsaljamal/keel/agent"
)

// Claude calls the Claude Messages API.
type Claude struct {
	client anthropic.Client
	model  anthropic.Model
}

// NewClaude returns a Claude model. The API key is read from ANTHROPIC_API_KEY
// unless passed in opts. The SDK's own retries are turned off: Keel decides
// when to retry, so retries are counted and traced in one place.
func NewClaude(model string, opts ...option.RequestOption) *Claude {
	opts = append([]option.RequestOption{option.WithMaxRetries(0)}, opts...)
	return &Claude{client: anthropic.NewClient(opts...), model: anthropic.Model(model)}
}

// Generate implements agent.Model.
func (c *Claude) Generate(ctx context.Context, req agent.ModelRequest) (agent.ModelReply, error) {
	params := anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: req.MaxTokens,
		Messages:  toParams(req.Messages),
		Tools:     toTools(req.Tools),
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	msg, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return agent.ModelReply{}, err
	}
	return fromMessage(msg), nil
}

// Retryable reports whether a Claude API error is worth retrying:
// rate limits, overload, server errors and network failures.
func Retryable(err error) bool {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == 408, apiErr.StatusCode == 409, apiErr.StatusCode == 429:
			return true
		case apiErr.StatusCode >= 500:
			return true
		}
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF)
}

func toParams(msgs []agent.Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Blocks))
		for _, b := range m.Blocks {
			switch b.Kind {
			case agent.BlockText:
				blocks = append(blocks, anthropic.NewTextBlock(b.Text))
			case agent.BlockToolUse:
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ToolUseID, json.RawMessage(b.Input), b.ToolName))
			case agent.BlockToolResult:
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolUseID, b.Text, b.IsError))
			}
		}
		if m.Role == agent.RoleAssistant {
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		} else {
			out = append(out, anthropic.NewUserMessage(blocks...))
		}
	}
	return out
}

func toTools(specs []agent.ToolSpec) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(specs))
	for _, s := range specs {
		schema := anthropic.ToolInputSchemaParam{Properties: s.InputSchema["properties"]}
		if req, ok := s.InputSchema["required"].([]any); ok {
			for _, r := range req {
				if name, ok := r.(string); ok {
					schema.Required = append(schema.Required, name)
				}
			}
		}
		t := anthropic.ToolParam{Name: s.Name, InputSchema: schema}
		if s.Description != "" {
			t.Description = anthropic.String(s.Description)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &t})
	}
	return out
}

func fromMessage(msg *anthropic.Message) agent.ModelReply {
	reply := agent.ModelReply{
		StopReason: string(msg.StopReason),
		Usage:      agent.Usage{InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens},
	}
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			reply.Blocks = append(reply.Blocks, agent.Block{Kind: agent.BlockText, Text: b.Text})
		case anthropic.ToolUseBlock:
			reply.Blocks = append(reply.Blocks, agent.Block{Kind: agent.BlockToolUse, ToolUseID: b.ID, ToolName: b.Name, Input: b.Input})
		}
	}
	return reply
}

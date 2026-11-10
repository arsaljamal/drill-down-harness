package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/arsaljamal/drill-down-harness/agent"
)

// OpenAICompatible talks to any server that implements the OpenAI Chat
// Completions API with tool calling: OpenAI, Ollama, LM Studio, vLLM, Groq,
// OpenRouter, and Gemini's OpenAI-compatible endpoint.
type OpenAICompatible struct {
	BaseURL string // e.g. http://localhost:11434/v1 for Ollama
	Model   string
	APIKey  string // empty for local servers such as Ollama
	HTTP    *http.Client
}

// HTTPError is a non-2xx answer from the server.
type HTTPError struct {
	Status     int
	Body       string
	RetryAfter time.Duration // from the Retry-After header, if any
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("model server returned %d: %s", e.Status, strings.TrimSpace(e.Body))
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model     string      `json:"model"`
	Messages  []oaMessage `json:"messages"`
	Tools     []oaTool    `json:"tools,omitempty"`
	MaxTokens int64       `json:"max_tokens,omitempty"`
}

type oaResponse struct {
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// Generate implements agent.Model.
func (o *OpenAICompatible) Generate(ctx context.Context, req agent.ModelRequest) (agent.ModelReply, error) {
	body, err := json.Marshal(oaRequest{
		Model:     o.Model,
		Messages:  toOAMessages(req.System, req.Messages),
		Tools:     toOATools(req.Tools),
		MaxTokens: req.MaxTokens,
	})
	if err != nil {
		return agent.ModelReply{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return agent.ModelReply{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	client := o.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return agent.ModelReply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return agent.ModelReply{}, err
	}
	if resp.StatusCode/100 != 2 {
		return agent.ModelReply{}, &HTTPError{Status: resp.StatusCode, Body: string(raw), RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}
	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return agent.ModelReply{}, fmt.Errorf("decode model response: %w", err)
	}
	if len(out.Choices) == 0 {
		return agent.ModelReply{}, errors.New("model response has no choices")
	}
	return fromOA(out), nil
}

// retryAfter parses a Retry-After header given in seconds.
func retryAfter(h string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// WaitHint returns the server's requested wait for rate-limit errors.
func WaitHint(err error) (time.Duration, bool) {
	var h *HTTPError
	if errors.As(err, &h) && h.Status == 429 {
		if h.RetryAfter > 0 {
			return h.RetryAfter, true
		}
		return 30 * time.Second, true // rate limited with no hint: wait out a typical per-minute window
	}
	return 0, false
}

// RetryableOpenAI reports whether an OpenAI-compatible error is worth retrying.
func RetryableOpenAI(err error) bool {
	var h *HTTPError
	if errors.As(err, &h) {
		return h.Status == 408 || h.Status == 409 || h.Status == 429 || h.Status >= 500
	}
	return Retryable(err) // network errors
}

func strPtr(s string) *string { return &s }

func toOAMessages(system string, msgs []agent.Message) []oaMessage {
	var out []oaMessage
	if system != "" {
		out = append(out, oaMessage{Role: "system", Content: strPtr(system)})
	}
	for _, m := range msgs {
		if m.Role == agent.RoleAssistant {
			am := oaMessage{Role: "assistant"}
			var text []string
			for _, b := range m.Blocks {
				switch b.Kind {
				case agent.BlockText:
					text = append(text, b.Text)
				case agent.BlockToolUse:
					tc := oaToolCall{ID: b.ToolUseID, Type: "function"}
					tc.Function.Name = b.ToolName
					tc.Function.Arguments = string(b.Input)
					if tc.Function.Arguments == "" {
						tc.Function.Arguments = "{}"
					}
					am.ToolCalls = append(am.ToolCalls, tc)
				}
			}
			if len(text) > 0 {
				am.Content = strPtr(strings.Join(text, "\n"))
			}
			out = append(out, am)
			continue
		}
		// User turns: text becomes a user message; each tool result becomes
		// its own "tool" message, as the Chat Completions API expects.
		var text []string
		for _, b := range m.Blocks {
			switch b.Kind {
			case agent.BlockText:
				text = append(text, b.Text)
			case agent.BlockToolResult:
				content := b.Text
				if b.IsError {
					content = "ERROR: " + content
				}
				out = append(out, oaMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: strPtr(content)})
			}
		}
		if len(text) > 0 {
			out = append(out, oaMessage{Role: "user", Content: strPtr(strings.Join(text, "\n"))})
		}
	}
	return out
}

func toOATools(specs []agent.ToolSpec) []oaTool {
	out := make([]oaTool, 0, len(specs))
	for _, s := range specs {
		var t oaTool
		t.Type = "function"
		t.Function.Name = s.Name
		t.Function.Description = s.Description
		t.Function.Parameters = s.InputSchema
		out = append(out, t)
	}
	return out
}

func fromOA(r oaResponse) agent.ModelReply {
	c := r.Choices[0]
	reply := agent.ModelReply{
		StopReason: c.FinishReason,
		Usage:      agent.Usage{InputTokens: r.Usage.PromptTokens, OutputTokens: r.Usage.CompletionTokens},
	}
	if c.Message.Content != nil && *c.Message.Content != "" {
		reply.Blocks = append(reply.Blocks, agent.Block{Kind: agent.BlockText, Text: *c.Message.Content})
	}
	for _, tc := range c.Message.ToolCalls {
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		reply.Blocks = append(reply.Blocks, agent.Block{Kind: agent.BlockToolUse, ToolUseID: tc.ID, ToolName: tc.Function.Name, Input: json.RawMessage(args)})
	}
	return reply
}

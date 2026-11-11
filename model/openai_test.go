package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arsaljamal/drill-down-harness/agent"
)

// fakeOpenAI answers like an OpenAI-compatible server (e.g. Ollama): first a
// tool call, then a final answer once the tool result comes back.
func fakeOpenAI(t *testing.T, seen *[]oaRequest, auth *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		*auth = r.Header.Get("Authorization")
		var req oaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		*seen = append(*seen, req)
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool" {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"The answer is 42."},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":6}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","extra_content":{"google":{"thought_signature":"sig-123"}},"function":{"name":"add","arguments":"{\"a\":21.5,\"b\":20.5}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":9}}`))
	}))
}

type addBox struct{}

func (addBox) Tools(context.Context) ([]agent.ToolSpec, error) {
	return []agent.ToolSpec{{Name: "add", Description: "Add two numbers.", InputSchema: map[string]any{"type": "object"}}}, nil
}
func (addBox) Call(_ context.Context, _ string, in json.RawMessage) (agent.ToolResult, error) {
	var v struct{ A, B float64 }
	json.Unmarshal(in, &v)
	b, _ := json.Marshal(v.A + v.B)
	return agent.ToolResult{Text: string(b)}, nil
}

func TestOpenAICompatibleRunsAgentLoopWithoutKey(t *testing.T) {
	var seen []oaRequest
	var auth string
	srv := fakeOpenAI(t, &seen, &auth)
	defer srv.Close()

	m := &OpenAICompatible{BaseURL: srv.URL + "/v1", Model: "qwen3"}
	res := (&agent.Runner{Model: m, Tools: addBox{}, System: "Be brief."}).Run(context.Background(), "What is 21.5 + 20.5?")

	if res.Stop != agent.StopDone || res.FinalText != "The answer is 42." {
		t.Fatalf("stop=%s text=%q err=%v", res.Stop, res.FinalText, res.Err)
	}
	if auth != "" {
		t.Fatalf("no key set, but Authorization = %q", auth)
	}
	if res.Usage.Total() != 65 {
		t.Fatalf("usage = %d, want 65", res.Usage.Total())
	}

	// Second request must carry: system, user, assistant tool_call, tool result.
	msgs := seen[1].Messages
	if len(msgs) != 4 || msgs[0].Role != "system" || msgs[2].Role != "assistant" || msgs[3].Role != "tool" {
		t.Fatalf("unexpected message shape: %+v", msgs)
	}
	if msgs[2].ToolCalls[0].ID != "call_1" || msgs[3].ToolCallID != "call_1" || *msgs[3].Content != "42" {
		t.Fatalf("tool round trip broken: %+v / %+v", msgs[2], msgs[3])
	}
	if string(msgs[2].ToolCalls[0].ExtraContent) != `{"google":{"thought_signature":"sig-123"}}` {
		t.Fatalf("provider data not sent back: %s", msgs[2].ToolCalls[0].ExtraContent)
	}
	if seen[0].Tools[0].Function.Name != "add" || seen[0].Model != "qwen3" {
		t.Fatalf("tools/model not sent: %+v", seen[0])
	}
}

func TestOpenAICompatibleSendsKeyWhenSet(t *testing.T) {
	var seen []oaRequest
	var auth string
	srv := fakeOpenAI(t, &seen, &auth)
	defer srv.Close()
	m := &OpenAICompatible{BaseURL: srv.URL + "/v1", Model: "x", APIKey: "sk-test"}
	if _, err := m.Generate(context.Background(), agent.ModelRequest{Messages: []agent.Message{{Role: agent.RoleUser, Blocks: []agent.Block{{Kind: agent.BlockText, Text: "hi"}}}}}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestRetryableOpenAI(t *testing.T) {
	for status, want := range map[int]bool{429: true, 503: true, 500: true, 400: false, 401: false, 404: false} {
		if got := RetryableOpenAI(&HTTPError{Status: status}); got != want {
			t.Errorf("status %d: got %v want %v", status, got, want)
		}
	}
}

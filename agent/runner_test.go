package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/arsaljamal/keel/retry"
)

// scriptModel replays a fixed list of replies (or errors), one per call.
type scriptModel struct {
	steps []step
	calls int
	seen  []ModelRequest
}

type step struct {
	reply ModelReply
	err   error
}

func (m *scriptModel) Generate(ctx context.Context, req ModelRequest) (ModelReply, error) {
	m.seen = append(m.seen, req)
	if m.calls >= len(m.steps) {
		<-ctx.Done() // nothing scripted: behave like a hung API
		return ModelReply{}, ctx.Err()
	}
	s := m.steps[m.calls]
	m.calls++
	return s.reply, s.err
}

type mapTools map[string]func(json.RawMessage) ToolResult

func (t mapTools) Tools(context.Context) ([]ToolSpec, error) {
	var out []ToolSpec
	for name := range t {
		out = append(out, ToolSpec{Name: name, InputSchema: map[string]any{"type": "object"}})
	}
	return out, nil
}

func (t mapTools) Call(_ context.Context, name string, in json.RawMessage) (ToolResult, error) {
	fn, ok := t[name]
	if !ok {
		return ToolResult{}, errors.New("no such tool")
	}
	return fn(in), nil
}

func text(s string) ModelReply {
	return ModelReply{Blocks: []Block{{Kind: BlockText, Text: s}}, Usage: Usage{InputTokens: 10, OutputTokens: 5}}
}

func use(id, name, input string) ModelReply {
	return ModelReply{Blocks: []Block{{Kind: BlockToolUse, ToolUseID: id, ToolName: name, Input: json.RawMessage(input)}}, Usage: Usage{InputTokens: 10, OutputTokens: 5}}
}

var echo = mapTools{"echo": func(in json.RawMessage) ToolResult { return ToolResult{Text: string(in)} }}

func TestDoneAfterToolRoundTrip(t *testing.T) {
	m := &scriptModel{steps: []step{{reply: use("t1", "echo", `{"x":1}`)}, {reply: text("all good")}}}
	res := (&Runner{Model: m, Tools: echo}).Run(context.Background(), "do it")

	if res.Stop != StopDone || res.FinalText != "all good" || res.Turns != 2 {
		t.Fatalf("got stop=%s text=%q turns=%d", res.Stop, res.FinalText, res.Turns)
	}
	// The second model call must see the tool result for t1.
	last := m.seen[1].Messages[len(m.seen[1].Messages)-1]
	if last.Role != RoleUser || last.Blocks[0].Kind != BlockToolResult || last.Blocks[0].ToolUseID != "t1" || last.Blocks[0].Text != `{"x":1}` {
		t.Fatalf("tool result not passed back: %+v", last)
	}
	if res.Usage.Total() != 30 {
		t.Fatalf("usage = %d, want 30", res.Usage.Total())
	}
}

func TestStopsAtMaxTurns(t *testing.T) {
	m := &scriptModel{steps: []step{{reply: use("1", "echo", `{"n":1}`)}, {reply: use("2", "echo", `{"n":2}`)}, {reply: use("3", "echo", `{"n":3}`)}}}
	res := (&Runner{Model: m, Tools: echo, Limits: Limits{MaxTurns: 2}}).Run(context.Background(), "loop")
	if res.Stop != StopMaxTurns || res.Turns != 2 {
		t.Fatalf("got stop=%s turns=%d", res.Stop, res.Turns)
	}
}

func TestStopsAtTokenBudget(t *testing.T) {
	m := &scriptModel{steps: []step{{reply: use("1", "echo", `{"n":1}`)}, {reply: use("2", "echo", `{"n":2}`)}}}
	res := (&Runner{Model: m, Tools: echo, Limits: Limits{MaxTokens: 15}}).Run(context.Background(), "spend")
	if res.Stop != StopTokenBudget || res.Turns != 1 {
		t.Fatalf("got stop=%s turns=%d", res.Stop, res.Turns)
	}
}

func TestDetectsStuckLoop(t *testing.T) {
	same := use("x", "echo", `{"same":true}`)
	m := &scriptModel{steps: []step{{reply: same}, {reply: same}, {reply: same}, {reply: text("never")}}}
	res := (&Runner{Model: m, Tools: echo, Limits: Limits{MaxRepeats: 3}}).Run(context.Background(), "repeat")
	if res.Stop != StopStuck || res.Err == nil {
		t.Fatalf("got stop=%s err=%v", res.Stop, res.Err)
	}
}

func TestRetriesTransientModelErrors(t *testing.T) {
	transient := errors.New("overloaded")
	m := &scriptModel{steps: []step{{err: transient}, {err: transient}, {reply: text("ok")}}}
	var events []Event
	r := &Runner{
		Model:      m,
		Tools:      echo,
		ModelRetry: retry.Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
		Observe:    func(e Event) { events = append(events, e) },
	}
	res := r.Run(context.Background(), "flaky")
	if res.Stop != StopDone || res.FinalText != "ok" {
		t.Fatalf("got stop=%s text=%q err=%v", res.Stop, res.FinalText, res.Err)
	}
	retries := 0
	for _, e := range events {
		if e.Kind == "retry" {
			retries++
		}
	}
	if retries != 2 {
		t.Fatalf("retry events = %d, want 2", retries)
	}
}

func TestModelErrorAfterRetriesRunOut(t *testing.T) {
	m := &scriptModel{steps: []step{{err: errors.New("down")}, {err: errors.New("down")}}}
	r := &Runner{Model: m, Tools: echo, ModelRetry: retry.Policy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}}
	res := r.Run(context.Background(), "x")
	if res.Stop != StopModelError || res.Err == nil {
		t.Fatalf("got stop=%s err=%v", res.Stop, res.Err)
	}
}

func TestTimeout(t *testing.T) {
	m := &scriptModel{} // hangs until the context ends
	res := (&Runner{Model: m, Tools: echo, Limits: Limits{MaxDuration: 20 * time.Millisecond}}).Run(context.Background(), "slow")
	if res.Stop != StopTimeout {
		t.Fatalf("got stop=%s err=%v", res.Stop, res.Err)
	}
}

func TestToolErrorGoesBackToModel(t *testing.T) {
	m := &scriptModel{steps: []step{{reply: use("t1", "missing", `{}`)}, {reply: text("recovered")}}}
	res := (&Runner{Model: m, Tools: echo}).Run(context.Background(), "x")
	if res.Stop != StopDone {
		t.Fatalf("got stop=%s", res.Stop)
	}
	got := m.seen[1].Messages[len(m.seen[1].Messages)-1].Blocks[0]
	if !got.IsError {
		t.Fatalf("want error tool result, got %+v", got)
	}
}

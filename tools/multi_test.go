package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/arsaljamal/drill-down-harness/agent"
)

// fakeSource is a tool server with one read-only "read" and one unsafe "send".
type fakeSource struct{ calls []string }

func (f *fakeSource) Tools(context.Context) ([]agent.ToolSpec, error) {
	return []agent.ToolSpec{{Name: "read"}, {Name: "send", Description: "Send it."}}, nil
}
func (f *fakeSource) Call(_ context.Context, name string, _ json.RawMessage) (agent.ToolResult, error) {
	f.calls = append(f.calls, name)
	return agent.ToolResult{Text: "ok:" + name}, nil
}
func (f *fakeSource) Class(name string) SideEffect {
	if name == "read" {
		return ReadOnly
	}
	return Unsafe
}

func TestMultiPrefixesAndRoutes(t *testing.T) {
	a, b := &fakeSource{}, &fakeSource{}
	m := &Multi{Sources: map[string]Source{"grafana": a, "sentry": b}, Policy: PolicyAllow}
	specs, err := m.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 4 || specs[0].Name != "grafana__read" || specs[3].Name != "sentry__send" {
		t.Fatalf("names: %+v", specs)
	}
	if _, err := m.Call(context.Background(), "sentry__read", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 1 || b.calls[0] != "read" || len(a.calls) != 0 {
		t.Fatalf("routed wrong: a=%v b=%v", a.calls, b.calls)
	}
}

func TestSingleServerKeepsNames(t *testing.T) {
	m := &Multi{Sources: map[string]Source{"demo": &fakeSource{}}, Policy: PolicyAllow}
	specs, _ := m.Tools(context.Background())
	if specs[0].Name != "read" {
		t.Fatalf("got %s", specs[0].Name)
	}
}

func TestUnsafePolicy(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		policy  Policy
		approve Approver
		ran     bool
	}{
		{PolicyDeny, nil, false},
		{PolicyAsk, nil, false}, // nobody to ask: refuse
		{PolicyAsk, func(context.Context, string, json.RawMessage) (bool, error) { return false, nil }, false},
		{PolicyAsk, func(context.Context, string, json.RawMessage) (bool, error) { return true, nil }, true},
		{PolicyAllow, nil, true},
	}
	for i, c := range cases {
		src := &fakeSource{}
		m := &Multi{Sources: map[string]Source{"demo": src}, Policy: c.policy, Approve: c.approve}
		specs, _ := m.Tools(ctx)
		res, err := m.Call(ctx, "send", nil)
		if err != nil {
			t.Fatal(err)
		}
		if ran := len(src.calls) == 1; ran != c.ran {
			t.Errorf("case %d: ran=%v want %v (result %+v)", i, ran, c.ran, res)
		}
		if !c.ran && !res.IsError {
			t.Errorf("case %d: refusal must be an error result for the model", i)
		}
		if c.policy != PolicyAllow && specs[1].Description == "Send it." {
			t.Errorf("case %d: unsafe tool not marked in its description", i)
		}
		// Read-only tools never need approval.
		if _, err := m.Call(ctx, "read", nil); err != nil || src.calls[len(src.calls)-1] != "read" {
			t.Errorf("case %d: read-only call blocked", i)
		}
	}
}

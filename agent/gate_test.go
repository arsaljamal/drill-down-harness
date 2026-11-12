package agent

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func call(name, input string) Message {
	return Message{Role: RoleAssistant, Blocks: []Block{{Kind: BlockToolUse, ToolName: name, Input: json.RawMessage(input)}}}
}

func TestReportGate(t *testing.T) {
	g := ReportGate{
		MinToolCalls: 2,
		Require:      []Requirement{{Name: "check change events", Pattern: regexp.MustCompile(`rollout`)}},
		Headings:     []string{"## Root cause"},
	}
	if n := g.Check(nil, "  "); !strings.Contains(n, "empty") {
		t.Fatalf("empty reply should be nudged: %q", n)
	}
	hist := []Message{call("query_prometheus", `{"expr":"up"}`)}
	n := g.Check(hist, "done")
	for _, want := range []string{"at least 2", "change events", "## Root cause"} {
		if !strings.Contains(n, want) {
			t.Fatalf("nudge %q should mention %q", n, want)
		}
	}
	hist = append(hist, call("query_loki_logs", `{"logql":"{a=\"b\"} |~ \"rollout\""}`))
	if n := g.Check(hist, "## Root cause\nx"); n != "" {
		t.Fatalf("should pass, got %q", n)
	}
}

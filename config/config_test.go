package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "drill.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExampleLoads(t *testing.T) {
	cfg, err := Load(write(t, Example))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "ollama/qwen3" || cfg.Limits.MaxTurns != 40 || time.Duration(cfg.Limits.Timeout) != 15*time.Minute {
		t.Fatalf("unexpected: %+v", cfg)
	}
	if s := cfg.MCPServers["demo"]; s.Command != "go" || len(s.Args) != 2 {
		t.Fatalf("demo server: %+v", s)
	}
}

func TestDefaultsFillGaps(t *testing.T) {
	cfg, err := Load(write(t, "version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model == "" || cfg.Approvals.Unsafe != "ask" || cfg.Limits.MaxTurns != 20 {
		t.Fatalf("defaults missing: %+v", cfg)
	}
}

func TestRejectsMistakes(t *testing.T) {
	cases := map[string]string{
		"typo in key":         "version: 1\nmodle: x\n",
		"bad duration":        "version: 1\nlimits: { timeout: soon }\n",
		"bad approval":        "version: 1\napprovals: { unsafe: maybe }\n",
		"server needs one":    "version: 1\nmcp_servers: { x: {} }\n",
		"server has both":     "version: 1\nmcp_servers: { x: { command: a, url: http://b } }\n",
		"unsupported version": "version: 2\n",
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestExpandReadsEnvironment(t *testing.T) {
	t.Setenv("DRILL_TEST_TOKEN", "secret")
	got := Expand(map[string]string{"TOKEN": "${DRILL_TEST_TOKEN}", "URL": "http://x"})
	if got["TOKEN"] != "secret" || got["URL"] != "http://x" {
		t.Fatalf("got %v", got)
	}
}

func TestServerNamesSorted(t *testing.T) {
	c := Config{MCPServers: map[string]MCPServer{"sentry": {}, "grafana": {}, "github": {}}}
	if got := strings.Join(c.ServerNames(), ","); got != "github,grafana,sentry" {
		t.Fatalf("got %s", got)
	}
}

func TestReportGate(t *testing.T) {
	cfg, err := Load(write(t, "version: 1\nreport_gate:\n  min_tool_calls: 4\n  require:\n    - { name: logs, pattern: \"rollout|config\" }\n  headings: [\"## Root cause\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if g := cfg.ReportGate; g == nil || g.MinToolCalls != 4 || g.Require[0].Name != "logs" {
		t.Fatalf("got %+v", cfg.ReportGate)
	}
	if _, err := Load(write(t, "version: 1\nreport_gate:\n  require:\n    - { name: x, pattern: \"(\" }\n")); err == nil {
		t.Fatal("bad pattern accepted")
	}
}

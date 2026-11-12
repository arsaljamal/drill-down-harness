// Package config loads drill.yaml, the file that describes which model to
// use, which MCP servers to connect to, run limits and approval rules.
package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the contents of drill.yaml.
type Config struct {
	Version    int                  `yaml:"version"`
	Model      string               `yaml:"model"`
	BaseURL    string               `yaml:"base_url"`
	APIKeyEnv  string               `yaml:"api_key_env"`
	System     string               `yaml:"system"`
	Limits     Limits               `yaml:"limits"`
	MCPServers map[string]MCPServer `yaml:"mcp_servers"`
	Approvals  Approvals            `yaml:"approvals"`
	Playbooks  string               `yaml:"playbooks"`
	ReportGate *ReportGate          `yaml:"report_gate"`
}

// ReportGate holds back a final answer until the run has gathered evidence.
type ReportGate struct {
	MinToolCalls int           `yaml:"min_tool_calls"`
	Require      []Requirement `yaml:"require"`
	Headings     []string      `yaml:"headings"`
	MaxNudges    int           `yaml:"max_nudges"`
}

// Requirement: some tool call (name + input) must match Pattern, a regular expression.
type Requirement struct {
	Name    string `yaml:"name"`
	Pattern string `yaml:"pattern"`
}

// Limits bound a run.
type Limits struct {
	MaxTurns  int      `yaml:"max_turns"`
	MaxTokens int64    `yaml:"max_tokens"`
	Timeout   Duration `yaml:"timeout"`
}

// MCPServer is one MCP server: a command started over stdio, or a URL for
// the streamable HTTP transport. Values in Env and Headers may use ${VAR}
// so secrets stay in the environment, not in the file.
type MCPServer struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
}

// Approvals decides what happens before an unsafe tool call.
type Approvals struct {
	Unsafe string `yaml:"unsafe"` // ask | deny | allow
}

// Duration reads values such as "15m" or "90s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %q is not a duration like 15m or 90s", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

// Default returns the settings used when a field is left out.
func Default() Config {
	return Config{
		Version:   1,
		Model:     "anthropic/claude-sonnet-5-5",
		Limits:    Limits{MaxTurns: 20, MaxTokens: 200_000, Timeout: Duration(5 * time.Minute)},
		Approvals: Approvals{Unsafe: "ask"},
	}
}

// Load reads a config file on top of the defaults and checks it.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // a typo in a key is an error, not a silent no-op
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Validate reports the first problem in the config.
func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("version must be 1, got %d", c.Version)
	}
	switch c.Approvals.Unsafe {
	case "ask", "deny", "allow":
	default:
		return fmt.Errorf("approvals.unsafe must be ask, deny or allow, got %q", c.Approvals.Unsafe)
	}
	if g := c.ReportGate; g != nil {
		for i, r := range g.Require {
			if r.Name == "" {
				return fmt.Errorf("report_gate.require[%d]: name is required", i)
			}
			if _, err := regexp.Compile(r.Pattern); err != nil || r.Pattern == "" {
				return fmt.Errorf("report_gate.require[%d] (%s): pattern must be a valid regular expression", i, r.Name)
			}
		}
	}
	for _, name := range c.ServerNames() {
		s := c.MCPServers[name]
		if (s.Command == "") == (s.URL == "") {
			return fmt.Errorf("mcp_servers.%s: set exactly one of command or url", name)
		}
	}
	return nil
}

// ServerNames returns MCP server names in a stable order.
func (c Config) ServerNames() []string {
	names := make([]string, 0, len(c.MCPServers))
	for n := range c.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Expand replaces ${VAR} in a map's values with environment variables.
func Expand(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = os.ExpandEnv(v)
	}
	return out
}

// Example is written by `drill init`.
const Example = `# drill.yaml: settings for the Drill Down Harness.
version: 1

# provider/model. Local and free: ollama/qwen3. Hosted: anthropic/claude-sonnet-5-5,
# openai/<model>, gemini/<model>, openrouter/<provider/model>, groq/<model>.
model: ollama/qwen3
# api_key_env: ANTHROPIC_API_KEY   # name of the variable holding the key; never the key itself

limits:
  max_turns: 40
  max_tokens: 400000
  timeout: 15m

# MCP servers, written in any language. Use command for stdio, url for HTTP.
# ${VAR} is read from your environment, so tokens stay out of this file.
mcp_servers:
  demo:                 # the tiny demo server; works when run from the repo folder
    command: go
    args: [run, ./examples/demoserver]
  # grafana:
  #   command: mcp-grafana
  #   env: { GRAFANA_URL: "http://localhost:3000", GRAFANA_SERVICE_ACCOUNT_TOKEN: "${GRAFANA_TOKEN}" }

approvals:
  unsafe: ask   # ask: confirm each unsafe tool call; deny: never run them; allow: run them (not recommended)

# Optional: send a final answer back until the run has gathered evidence.
# report_gate:
#   min_tool_calls: 5
#   require:
#     - { name: "check recent changes in the logs", pattern: "rollout|deploy|config" }
#   headings: ["## Root cause", "## Evidence"]
`

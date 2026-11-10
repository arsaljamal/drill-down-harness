package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/arsaljamal/drill-down-harness/agent"
)

// Source is one connected tool server.
type Source interface {
	agent.Toolbox
	Class(name string) SideEffect
}

// Approver decides whether an unsafe tool call may run.
type Approver func(ctx context.Context, tool string, input json.RawMessage) (bool, error)

// Policy says what happens to unsafe tool calls.
type Policy string

const (
	PolicyAsk   Policy = "ask"
	PolicyDeny  Policy = "deny"
	PolicyAllow Policy = "allow"
)

// Multi combines several tool servers into one toolbox and applies the
// approval policy to unsafe tools. With more than one server, tool names are
// prefixed "<server>__" so two servers can expose a tool with the same name.
type Multi struct {
	Sources map[string]Source
	Policy  Policy
	Approve Approver

	routes map[string]route
}

type route struct {
	server, tool string
}

// Tools implements agent.Toolbox.
func (m *Multi) Tools(ctx context.Context) ([]agent.ToolSpec, error) {
	m.routes = map[string]route{}
	prefix := len(m.Sources) > 1
	var out []agent.ToolSpec
	for _, name := range sortedKeys(m.Sources) {
		specs, err := m.Sources[name].Tools(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, s := range specs {
			original := s.Name
			if prefix {
				s.Name = name + "__" + original
			}
			m.routes[s.Name] = route{server: name, tool: original}
			if m.Sources[name].Class(original) == Unsafe && m.Policy != PolicyAllow {
				s.Description = strings.TrimSpace(s.Description + " (Unsafe: needs human approval.)")
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// Call implements agent.Toolbox. Unsafe calls are approved, denied or
// allowed according to the policy; a refusal goes back to the model as an
// error result so it can choose another path.
func (m *Multi) Call(ctx context.Context, name string, input json.RawMessage) (agent.ToolResult, error) {
	r, ok := m.routes[name]
	if !ok {
		return agent.ToolResult{}, fmt.Errorf("unknown tool %q", name)
	}
	src := m.Sources[r.server]
	if src.Class(r.tool) == Unsafe {
		switch m.Policy {
		case PolicyAllow:
		case PolicyDeny:
			return agent.ToolResult{Text: fmt.Sprintf("refused: %q is unsafe and the policy denies unsafe tools", name), IsError: true}, nil
		default:
			if m.Approve == nil {
				return agent.ToolResult{Text: fmt.Sprintf("refused: %q is unsafe and nobody is available to approve it", name), IsError: true}, nil
			}
			yes, err := m.Approve(ctx, name, input)
			if err != nil {
				return agent.ToolResult{}, err
			}
			if !yes {
				return agent.ToolResult{Text: fmt.Sprintf("refused: a human declined %q", name), IsError: true}, nil
			}
		}
	}
	return src.Call(ctx, r.tool, input)
}

func sortedKeys(m map[string]Source) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

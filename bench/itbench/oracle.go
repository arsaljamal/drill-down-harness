package itbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/arsaljamal/drill-down-harness/agent"
)

// oracle is a scripted model that writes the correct answer from the answer
// key and submits it. It checks the download, sandbox, tools and grader end
// to end without paying for a model: a correct setup scores 1.0.
type oracle struct {
	answer string
	step   int
}

func newOracle(data string) (*oracle, error) {
	b, err := os.ReadFile(filepath.Join(data, "ground_truth.yaml"))
	if err != nil {
		return nil, err
	}
	// Answer keys come flat, or wrapped Kubernetes-style under "spec".
	var wrapped struct {
		Spec yaml.Node `yaml:"spec"`
	}
	if err := yaml.Unmarshal(b, &wrapped); err == nil && wrapped.Spec.Kind != 0 {
		if b, err = yaml.Marshal(&wrapped.Spec); err != nil {
			return nil, err
		}
	}
	var gt struct {
		Groups []struct {
			Kind      string   `yaml:"kind"`
			Name      string   `yaml:"name"`
			Filter    []string `yaml:"filter"`
			Namespace string   `yaml:"namespace"`
			RootCause bool     `yaml:"root_cause"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(b, &gt); err != nil {
		return nil, err
	}
	clean := regexp.MustCompile(`(\\b|-\.\*|-\.\+|\.\*|\^|\$)`)
	var factors []map[string]string
	for _, g := range gt.Groups {
		if !g.RootCause || (len(g.Filter) == 0 && g.Name == "") {
			continue
		}
		ns := g.Namespace
		if ns == "" {
			ns = "cluster"
		}
		name := g.Name
		if name == "" {
			name = clean.ReplaceAllString(g.Filter[0], "")
		}
		factors = append(factors, map[string]string{"name": fmt.Sprintf("%s/%s/%s", ns, g.Kind, name), "reasoning": "answer key", "evidence": "answer key"})
	}
	if len(factors) == 0 {
		return nil, fmt.Errorf("no root-cause groups in answer key")
	}
	out, _ := json.Marshal(map[string]any{"contributing_factors": factors})
	return &oracle{answer: string(out)}, nil
}

func (o *oracle) Generate(context.Context, agent.ModelRequest) (agent.ModelReply, error) {
	o.step++
	switch o.step {
	case 1:
		cmd := "cat > /home/user/agent_output.json <<'JSON'\n" + o.answer + "\nJSON\nls /home/user"
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return agent.ModelReply{Blocks: []agent.Block{{Kind: agent.BlockToolUse, ToolUseID: "o1", ToolName: "run_shell", Input: in}}}, nil
	case 2:
		return agent.ModelReply{Blocks: []agent.Block{{Kind: agent.BlockToolUse, ToolUseID: "o2", ToolName: "finish", Input: json.RawMessage(`{"path":"/home/user/agent_output.json"}`)}}}, nil
	}
	return agent.ModelReply{Blocks: []agent.Block{{Kind: agent.BlockText, Text: "submitted: " + strings.TrimSpace(o.answer)}}}, nil
}

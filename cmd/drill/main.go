// Command drill runs the Drill Down Harness.
//
//	drill init                 write an example drill.yaml
//	drill run "<task>"         run a task with the settings in drill.yaml
//	drill version
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/arsaljamal/drill-down-harness/agent"
	"github.com/arsaljamal/drill-down-harness/config"
	"github.com/arsaljamal/drill-down-harness/model"
	"github.com/arsaljamal/drill-down-harness/retry"
	"github.com/arsaljamal/drill-down-harness/tools"
)

// version is set at build time by the release pipeline.
var version = "dev"

const usage = `usage:
  drill init                       write an example drill.yaml in this folder
  drill run [flags] "<task>"       run a task (settings from drill.yaml; flags override)
  drill version`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(run(os.Args[2:]))
	case "init":
		os.Exit(initConfig())
	case "version", "--version", "-v":
		fmt.Println("drill", version)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func initConfig() int {
	if _, err := os.Stat("drill.yaml"); err == nil {
		fmt.Fprintln(os.Stderr, "drill.yaml already exists; not overwriting it")
		return 1
	}
	if err := os.WriteFile("drill.yaml", []byte(config.Example), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("wrote drill.yaml")
	return 0
}

func run(args []string) int {
	fset := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fset.String("config", "drill.yaml", "settings file (optional)")
	modelFlag := fset.String("model", "", `provider/model, e.g. "ollama/qwen3"; overrides the config`)
	baseURL := fset.String("base-url", "", "OpenAI-compatible server URL; overrides the config")
	keyEnv := fset.String("api-key-env", "", "environment variable holding the API key; overrides the config")
	server := fset.String("mcp", "", "command for one extra MCP server over stdio, named \"cli\"")
	unsafe := fset.String("unsafe", "", "ask | deny | allow; overrides approvals.unsafe")
	traces := fset.Bool("trace", false, "print OpenTelemetry spans to stderr")
	fset.Parse(args)

	task := strings.Join(fset.Args(), " ")
	if task == "" {
		fmt.Fprintln(os.Stderr, `drill run: give a task, e.g. drill run "Is the payments service healthy?"`)
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	switch {
	case errors.Is(err, fs.ErrNotExist) && *cfgPath == "drill.yaml":
		cfg = config.Default() // no file is fine: run on defaults and flags
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *modelFlag != "" {
		cfg.Model = *modelFlag
	}
	if *baseURL != "" {
		cfg.BaseURL = *baseURL
	}
	if *keyEnv != "" {
		cfg.APIKeyEnv = *keyEnv
	}
	if *unsafe != "" {
		cfg.Approvals.Unsafe = *unsafe
	}
	if *server != "" {
		parts := strings.Fields(*server)
		if cfg.MCPServers == nil {
			cfg.MCPServers = map[string]config.MCPServer{}
		}
		cfg.MCPServers["cli"] = config.MCPServer{Command: parts[0], Args: parts[1:]}
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if len(cfg.MCPServers) == 0 {
		fmt.Fprintln(os.Stderr, "no MCP servers: add mcp_servers to drill.yaml (see drill init) or pass --mcp")
		return 2
	}

	m, retryable, err := pickModel(cfg.Model, cfg.BaseURL, cfg.APIKeyEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *traces {
		exp, err := stdouttrace.New(stdouttrace.WithWriter(os.Stderr), stdouttrace.WithPrettyPrint())
		if err != nil {
			fmt.Fprintln(os.Stderr, "trace exporter:", err)
			return 1
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
		defer tp.Shutdown(context.Background())
		otel.SetTracerProvider(tp)
	}

	box := &tools.Multi{Sources: map[string]tools.Source{}, Policy: tools.Policy(cfg.Approvals.Unsafe), Approve: askOnTerminal}
	for _, name := range cfg.ServerNames() {
		src, err := connect(ctx, cfg.MCPServers[name])
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp server %q: %v\n", name, err)
			return 1
		}
		defer src.Close()
		box.Sources[name] = src
	}

	r := &agent.Runner{
		Model:      m,
		Tools:      box,
		System:     cfg.System,
		Limits:     agent.Limits{MaxTurns: cfg.Limits.MaxTurns, MaxTokens: cfg.Limits.MaxTokens, MaxDuration: time.Duration(cfg.Limits.Timeout)},
		ModelRetry: retry.Default(retryable),
		Observe: func(e agent.Event) {
			fmt.Fprintf(os.Stderr, "[turn %d] %-11s %s\n", e.Turn, e.Kind, oneLine(e.Message, 160))
		},
	}
	res := r.Run(ctx, task)

	fmt.Fprintf(os.Stderr, "stop=%s turns=%d tokens=%d\n", res.Stop, res.Turns, res.Usage.Total())
	if res.FinalText != "" {
		fmt.Println(res.FinalText)
	}
	if res.Stop != agent.StopDone {
		if res.Err != nil {
			fmt.Fprintln(os.Stderr, "error:", res.Err)
		}
		return 1
	}
	return 0
}

// connect starts or dials one MCP server from the config.
func connect(ctx context.Context, s config.MCPServer) (*tools.MCP, error) {
	if s.URL != "" {
		hc := &http.Client{Transport: headerTransport{headers: config.Expand(s.Headers), base: http.DefaultTransport}}
		return tools.ConnectMCP(ctx, &mcp.StreamableClientTransport{Endpoint: os.ExpandEnv(s.URL), HTTPClient: hc})
	}
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = os.Environ()
	for k, v := range config.Expand(s.Env) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr
	return tools.ConnectMCP(ctx, &mcp.CommandTransport{Command: cmd})
}

type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

// askOnTerminal asks the person at the keyboard to approve an unsafe call.
// Without a terminal (CI, cron) the answer is no.
func askOnTerminal(_ context.Context, tool string, input json.RawMessage) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, nil
	}
	defer tty.Close()
	fmt.Fprintf(tty, "\nApprove unsafe tool call?\n  %s %s\n[y/N] ", tool, oneLine(string(input), 300))
	line, _ := bufio.NewReader(tty).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}

// provider holds the defaults for a "provider/model" prefix.
type provider struct {
	baseURL string
	keyEnv  string // empty: no key needed
}

var providers = map[string]provider{
	"openai":     {baseURL: "https://api.openai.com/v1", keyEnv: "OPENAI_API_KEY"},
	"ollama":     {baseURL: "http://localhost:11434/v1"},
	"lmstudio":   {baseURL: "http://localhost:1234/v1"},
	"openrouter": {baseURL: "https://openrouter.ai/api/v1", keyEnv: "OPENROUTER_API_KEY"},
	"gemini":     {baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", keyEnv: "GEMINI_API_KEY"},
	"groq":       {baseURL: "https://api.groq.com/openai/v1", keyEnv: "GROQ_API_KEY"},
	"compatible": {}, // any other OpenAI-compatible server: set base_url
}

// pickModel turns "provider/model" into a model client and its retry rule.
func pickModel(spec, baseURL, keyEnv string) (agent.Model, func(error) bool, error) {
	name, id, ok := strings.Cut(spec, "/")
	if !ok || id == "" {
		return nil, nil, fmt.Errorf("model must look like provider/model, got %q", spec)
	}
	if name == "anthropic" {
		env := keyEnv
		if env == "" {
			env = "ANTHROPIC_API_KEY"
		}
		if os.Getenv(env) == "" {
			return nil, nil, fmt.Errorf("anthropic models need an API key in $%s (or use model: ollama/qwen3 to run locally with no key)", env)
		}
		return model.NewClaude(id, option.WithAPIKey(os.Getenv(env))), model.Retryable, nil
	}
	p, known := providers[name]
	if !known {
		return nil, nil, fmt.Errorf("unknown provider %q (known: anthropic, openai, ollama, lmstudio, openrouter, gemini, groq, compatible)", name)
	}
	if baseURL == "" {
		baseURL = p.baseURL
	}
	if baseURL == "" {
		return nil, nil, fmt.Errorf("provider %q needs base_url", name)
	}
	if keyEnv == "" {
		keyEnv = p.keyEnv
	}
	key := ""
	if keyEnv != "" {
		if key = os.Getenv(keyEnv); key == "" {
			return nil, nil, fmt.Errorf("%s models need an API key in $%s", name, keyEnv)
		}
	}
	return &model.OpenAICompatible{BaseURL: baseURL, Model: id, APIKey: key}, model.RetryableOpenAI, nil
}

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

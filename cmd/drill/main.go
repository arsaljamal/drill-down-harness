// Command drill runs the Drill Down Harness.
//
//	drill init                 write an example drill.yaml
//	drill run "<task>"         run a task with the settings in drill.yaml
//	drill bench itbench [flags]  score the harness on the public ITBench-AA SRE tasks
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
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/arsaljamal/drill-down-harness/agent"
	"github.com/arsaljamal/drill-down-harness/bench/itbench"
	"github.com/arsaljamal/drill-down-harness/config"
	"github.com/arsaljamal/drill-down-harness/lens"
	"github.com/arsaljamal/drill-down-harness/model"
	"github.com/arsaljamal/drill-down-harness/retry"
	"github.com/arsaljamal/drill-down-harness/tools"
)

// version is set at build time by the release pipeline.
var version = "dev"

const usage = `usage:
  drill init                       write an example drill.yaml in this folder
  drill run [flags] "<task>"       run a task (settings from drill.yaml; flags override)
                                   --ui 127.0.0.1:7072 to watch it live, --record run.jsonl to keep it
  drill replay [flags] run.jsonl   play a recorded run back in the viewer
  drill bench itbench [flags]      score the harness on the public ITBench-AA SRE tasks
  drill version`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(run(os.Args[2:]))
	case "replay":
		os.Exit(replay(os.Args[2:]))
	case "init":
		os.Exit(initConfig())
	case "bench":
		os.Exit(bench(os.Args[2:]))
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
	events := fset.String("events", "text", "progress on stderr: text (one short line per step) or json (one full JSON object per line, for UIs)")
	uiAddr := fset.String("ui", "", `watch the investigation in a browser, e.g. --ui 127.0.0.1:7072`)
	record := fset.String("record", "", "save every event to this file (JSON lines) for drill replay")
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

	out, err := newSink(*events == "json", *record, *uiAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer out.close()
	out.emit(map[string]any{"kind": "meta", "task": task, "model": cfg.Model, "components": cfg.Viewer.Components})

	r := &agent.Runner{
		Model:      m,
		Tools:      box,
		System:     cfg.System,
		Limits:     agent.Limits{MaxTurns: cfg.Limits.MaxTurns, MaxTokens: cfg.Limits.MaxTokens, MaxDuration: time.Duration(cfg.Limits.Timeout)},
		ModelRetry: retry.Default(retryable),
		Observe: func(e agent.Event) {
			msg := e.Message
			if len(msg) > 20000 {
				msg = msg[:20000] + " …"
			}
			out.emit(map[string]any{"turn": e.Turn, "kind": e.Kind, "message": msg})
			if !out.json {
				fmt.Fprintf(os.Stderr, "[turn %d] %-11s %s\n", e.Turn, e.Kind, oneLine(e.Message, 160))
			}
		},
	}
	if g := cfg.ReportGate; g != nil {
		gate := agent.ReportGate{MinToolCalls: g.MinToolCalls, Headings: g.Headings}
		for _, req := range g.Require {
			gate.Require = append(gate.Require, agent.Requirement{Name: req.Name, Pattern: regexp.MustCompile("(?i)" + req.Pattern)})
		}
		r.Gate, r.MaxNudges = gate.Check, g.MaxNudges
	}
	if len(cfg.Lenses) > 0 {
		use := map[string]bool{}
		for _, l := range cfg.Lenses {
			use[l] = true
		}
		r.Shape = func(tool string, input []byte, text string) (string, bool) {
			if use["series"] {
				if s, ok := lens.SeriesFor(queryOf(tool, input), text, time.Now()); ok {
					return s, true
				}
			}
			if use["logs"] {
				return lens.Logs(text, time.Now())
			}
			return text, false
		}
	}
	res := r.Run(ctx, task)

	out.emit(map[string]any{"kind": "summary", "stop": res.Stop, "turns": res.Turns,
		"input_tokens": res.Usage.InputTokens, "output_tokens": res.Usage.OutputTokens})
	if res.FinalText != "" {
		out.emit(map[string]any{"kind": "final", "message": res.FinalText})
	}
	if !out.json {
		fmt.Fprintf(os.Stderr, "stop=%s turns=%d tokens=%d\n", res.Stop, res.Turns, res.Usage.Total())
	}
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

func bench(args []string) int {
	if len(args) == 0 || args[0] != "itbench" {
		fmt.Fprintln(os.Stderr, "usage: drill bench itbench [flags]")
		return 2
	}
	fset := flag.NewFlagSet("bench itbench", flag.ExitOnError)
	modelFlag := fset.String("model", "anthropic/claude-sonnet-5-5", "provider/model")
	baseURL := fset.String("base-url", "", "OpenAI-compatible server URL")
	keyEnv := fset.String("api-key-env", "", "environment variable holding the API key")
	scenarios := fset.String("scenarios", "", "comma-separated scenario IDs, e.g. Scenario-1,Scenario-3 (default: all 40 public ones)")
	limit := fset.Int("limit", 0, "stop after this many tasks (0: no limit)")
	out := fset.String("out", "", "results folder (default bench-results/itbench/<time>)")
	work := fset.String("work", "", "where snapshots are downloaded; must be inside your home folder (default: your cache folder)")
	keep := fset.Bool("keep", false, "keep snapshot data after each task")
	stopBelow := fset.Float64("stop-below", 0, "stop early if the running mean reward falls below this (0: never)")
	minTasks := fset.Int("min-tasks", 5, "tasks to run before the stop-below check applies")
	maxTurns := fset.Int("max-turns", 100, "model calls per task (the leaderboard uses 100)")
	maxTokens := fset.Int64("max-tokens", 3_000_000, "token budget per task")
	timeout := fset.Duration("timeout", 45*time.Minute, "time limit per task")
	priceIn := fset.Float64("price-in", 0, "USD per million input tokens, for cost estimates")
	priceOut := fset.Float64("price-out", 0, "USD per million output tokens, for cost estimates")
	rpm := fset.Float64("rpm", 0, "max model requests per minute, e.g. 5 for a free tier (0: no pacing)")
	effort := fset.String("reasoning-effort", "", "low | medium | high, for OpenAI-compatible models that support it")
	keyFile := fset.String("api-key-file", "", "read the API key from this file (keeps it out of your shell history)")
	check := fset.Bool("check", false, "setup check: submit the answer key instead of calling a model (should score 1.0)")
	fset.Parse(args[1:])

	var m agent.Model
	var retryable func(error) bool
	var err error
	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "api key file:", err)
			return 2
		}
		env := *keyEnv
		if env == "" {
			name, _, _ := strings.Cut(*modelFlag, "/")
			env = providers[name].keyEnv
			if name == "anthropic" {
				env = "ANTHROPIC_API_KEY"
			}
		}
		if env == "" {
			fmt.Fprintln(os.Stderr, "--api-key-file: this provider takes no key; set --api-key-env")
			return 2
		}
		os.Setenv(env, strings.TrimSpace(string(b)))
	}
	if *check {
		*modelFlag = "answer-key-check"
	} else if m, retryable, err = pickModel(*modelFlag, *baseURL, *keyEnv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if oa, ok := m.(*model.OpenAICompatible); ok && *effort != "" {
		oa.ReasoningEffort = *effort
	}
	if *rpm > 0 && m != nil {
		m = &model.Paced{Model: m, Every: time.Duration(float64(time.Minute) / *rpm)}
	}
	if *out == "" {
		*out = filepath.Join("bench-results", "itbench", time.Now().UTC().Format("20060102-150405"))
	}
	if *work == "" {
		// Under the home folder, which Docker runtimes share by default.
		cache, err := os.UserCacheDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		*work = filepath.Join(cache, "drill", "itbench")
	}
	var ids []string
	for _, id := range strings.Split(*scenarios, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	results, err := itbench.Run(ctx, itbench.Options{
		Model: m, ModelName: *modelFlag,
		Retry: retry.Policy{MaxAttempts: 8, BaseDelay: 2 * time.Second, MaxDelay: time.Minute,
			Retryable: retryable, WaitHint: model.WaitHint, MaxHint: 5 * time.Minute},
		Scenarios: ids, Limit: *limit, OutDir: *out, WorkDir: *work, Keep: *keep,
		StopBelow: *stopBelow, MinTasks: *minTasks,
		MaxTurns: *maxTurns, MaxTokens: *maxTokens, Timeout: *timeout,
		PriceIn: *priceIn, PriceOut: *priceOut, Oracle: *check,
		Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) },
	})
	if len(results) > 0 {
		sum, cost := 0.0, 0.0
		for _, r := range results {
			sum += r.Reward
			cost += r.CostUSD
		}
		fmt.Printf("\nITBench-AA (public tasks) · %s · %d task(s) · mean reward %.3f · est. cost $%.2f\nresults: %s\n", *modelFlag, len(results), sum/float64(len(results)), cost, *out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// queryOf names a tool call for lens summaries: its query expression if it
// has one, else the tool name.
func queryOf(tool string, input []byte) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	for _, k := range []string{"expr", "query", "logql", "metric"} {
		if q, ok := in[k].(string); ok && q != "" {
			return tool + ": " + q
		}
	}
	return tool
}

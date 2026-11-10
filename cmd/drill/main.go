// Command drill runs an agent task against MCP tools.
//
//	drill run --model ollama/qwen3 --mcp "go run ./examples/demoserver" "What is 21.5 + 20.5?"
package main

import (
	"context"
	"flag"
	"fmt"
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
	"github.com/arsaljamal/drill-down-harness/model"
	"github.com/arsaljamal/drill-down-harness/retry"
	"github.com/arsaljamal/drill-down-harness/tools"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, `usage: drill run --mcp "<server command>" [flags] "<task>"`)
		os.Exit(2)
	}
	os.Exit(run(os.Args[2:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	server := fs.String("mcp", "", "command that starts an MCP server over stdio")
	modelFlag := fs.String("model", "anthropic/claude-sonnet-5-5", `provider/model, e.g. "anthropic/claude-sonnet-5-5", "ollama/qwen3", "openai/<model>"`)
	baseURL := fs.String("base-url", "", "OpenAI-compatible server URL (default depends on provider)")
	keyEnv := fs.String("api-key-env", "", "environment variable holding the API key (default depends on provider; none for ollama)")
	system := fs.String("system", "", "system prompt")
	maxTurns := fs.Int("max-turns", 20, "maximum model calls")
	maxTokens := fs.Int64("max-tokens", 200_000, "maximum input+output tokens for the run")
	timeout := fs.Duration("timeout", 5*time.Minute, "maximum wall-clock time")
	traces := fs.Bool("trace", false, "print OpenTelemetry spans to stderr")
	fs.Parse(args)

	task := strings.Join(fs.Args(), " ")
	if *server == "" || task == "" {
		fmt.Fprintln(os.Stderr, "drill run: --mcp and a task are required")
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

	parts := strings.Fields(*server)
	box, err := tools.ConnectMCP(ctx, &mcp.CommandTransport{Command: exec.Command(parts[0], parts[1:]...)})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer box.Close()

	m, retryable, err := pickModel(*modelFlag, *baseURL, *keyEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	r := &agent.Runner{
		Model:      m,
		Tools:      box,
		System:     *system,
		Limits:     agent.Limits{MaxTurns: *maxTurns, MaxTokens: *maxTokens, MaxDuration: *timeout},
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
	"compatible": {}, // any other OpenAI-compatible server: pass --base-url
}

// pickModel turns "provider/model" into a model client and its retry rule.
func pickModel(spec, baseURL, keyEnv string) (agent.Model, func(error) bool, error) {
	name, id, ok := strings.Cut(spec, "/")
	if !ok || id == "" {
		return nil, nil, fmt.Errorf("--model must look like provider/model, got %q", spec)
	}
	if name == "anthropic" {
		env := keyEnv
		if env == "" {
			env = "ANTHROPIC_API_KEY"
		}
		if os.Getenv(env) == "" {
			return nil, nil, fmt.Errorf("anthropic models need an API key in $%s (or try --model ollama/qwen3 to run locally with no key)", env)
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
		return nil, nil, fmt.Errorf("provider %q needs --base-url", name)
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

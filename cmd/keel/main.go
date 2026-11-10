// Command keel runs an agent task against MCP tools.
//
//	keel run --mcp "go run ./examples/demoserver" "What is 21.5 + 20.5?"
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/arsaljamal/keel/agent"
	"github.com/arsaljamal/keel/model"
	"github.com/arsaljamal/keel/retry"
	"github.com/arsaljamal/keel/tools"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, `usage: keel run --mcp "<server command>" [flags] "<task>"`)
		os.Exit(2)
	}
	os.Exit(run(os.Args[2:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	server := fs.String("mcp", "", "command that starts an MCP server over stdio")
	modelName := fs.String("model", "claude-sonnet-5-5", "Claude model ID")
	system := fs.String("system", "", "system prompt")
	maxTurns := fs.Int("max-turns", 20, "maximum model calls")
	maxTokens := fs.Int64("max-tokens", 200_000, "maximum input+output tokens for the run")
	timeout := fs.Duration("timeout", 5*time.Minute, "maximum wall-clock time")
	traces := fs.Bool("trace", false, "print OpenTelemetry spans to stderr")
	fs.Parse(args)

	task := strings.Join(fs.Args(), " ")
	if *server == "" || task == "" {
		fmt.Fprintln(os.Stderr, "keel run: --mcp and a task are required")
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

	r := &agent.Runner{
		Model:      model.NewClaude(*modelName),
		Tools:      box,
		System:     *system,
		Limits:     agent.Limits{MaxTurns: *maxTurns, MaxTokens: *maxTokens, MaxDuration: *timeout},
		ModelRetry: retry.Default(model.Retryable),
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

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

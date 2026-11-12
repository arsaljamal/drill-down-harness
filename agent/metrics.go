package agent

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics go to the global OpenTelemetry meter; with no exporter set up
// they cost nothing. Names start with drill. like the spans.
type instruments struct {
	runs        metric.Int64Counter
	runSeconds  metric.Float64Histogram
	turns       metric.Int64Histogram
	tokens      metric.Int64Counter
	toolCalls   metric.Int64Counter
	toolSeconds metric.Float64Histogram
	modelSecs   metric.Float64Histogram
	gate        metric.Int64Counter
}

var (
	instOnce sync.Once
	inst     instruments
)

func meters() *instruments {
	instOnce.Do(func() {
		m := otel.Meter("github.com/arsaljamal/drill-down-harness/agent")
		secs := metric.WithExplicitBucketBoundaries(.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600)
		inst.runs, _ = m.Int64Counter("drill.runs", metric.WithDescription("Finished runs, by stop reason"))
		inst.runSeconds, _ = m.Float64Histogram("drill.run.duration", metric.WithUnit("s"), secs)
		inst.turns, _ = m.Int64Histogram("drill.run.turns", metric.WithExplicitBucketBoundaries(1, 2, 3, 5, 8, 13, 20, 30, 50))
		inst.tokens, _ = m.Int64Counter("drill.tokens", metric.WithDescription("Model tokens, by kind"))
		inst.toolCalls, _ = m.Int64Counter("drill.tool.calls", metric.WithDescription("Tool calls, by tool and outcome"))
		inst.toolSeconds, _ = m.Float64Histogram("drill.tool.duration", metric.WithUnit("s"), secs)
		inst.modelSecs, _ = m.Float64Histogram("drill.model.duration", metric.WithUnit("s"), secs)
		inst.gate, _ = m.Int64Counter("drill.gate.sendbacks", metric.WithDescription("Final answers the report gate sent back"))
	})
	return &inst
}

func recordRun(ctx context.Context, res Result, took time.Duration) {
	m := meters()
	stop := metric.WithAttributes(attribute.String("stop", string(res.Stop)))
	m.runs.Add(ctx, 1, stop)
	m.runSeconds.Record(ctx, took.Seconds(), stop)
	m.turns.Record(ctx, int64(res.Turns))
	m.tokens.Add(ctx, res.Usage.InputTokens, metric.WithAttributes(attribute.String("kind", "input")))
	m.tokens.Add(ctx, res.Usage.OutputTokens, metric.WithAttributes(attribute.String("kind", "output")))
}

func recordTool(ctx context.Context, tool string, isErr bool, took time.Duration) {
	m := meters()
	outcome := "ok"
	if isErr {
		outcome = "error"
	}
	m.toolCalls.Add(ctx, 1, metric.WithAttributes(attribute.String("tool", tool), attribute.String("outcome", outcome)))
	m.toolSeconds.Record(ctx, took.Seconds(), metric.WithAttributes(attribute.String("tool", tool)))
}

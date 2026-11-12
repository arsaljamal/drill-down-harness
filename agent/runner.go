package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/arsaljamal/drill-down-harness/retry"
)

// StopReason says why a run ended. Every run ends with exactly one.
type StopReason string

const (
	StopDone        StopReason = "done"         // the model finished without asking for tools
	StopMaxTurns    StopReason = "max_turns"    // turn limit reached
	StopTokenBudget StopReason = "token_budget" // token limit reached
	StopTimeout     StopReason = "timeout"      // wall-clock limit reached
	StopStuck       StopReason = "stuck"        // the same tool call kept repeating
	StopModelError  StopReason = "model_error"  // the model call failed after retries
	StopToolsError  StopReason = "tools_error"  // tools could not be listed
	StopCancelled   StopReason = "cancelled"    // the caller cancelled the run
)

// Limits bound a run. Zero means "no limit" except where noted.
type Limits struct {
	MaxTurns    int           // model calls per run (default 20)
	MaxTokens   int64         // input+output tokens across the run
	MaxDuration time.Duration // wall-clock time for the run
	MaxRepeats  int           // identical consecutive tool calls before "stuck" (default 3)
}

// Event is reported to the observer as the run progresses.
type Event struct {
	Turn    int
	Kind    string // model_reply, tool_call, tool_result, lens, gate, retry, stop
	Message string
}

// Runner drives the agent loop.
type Runner struct {
	Model     Model
	Tools     Toolbox
	System    string
	Limits    Limits
	MaxOutput int64 // max tokens per model reply (default 4096)

	// ModelRetry decides how failed model calls are retried.
	ModelRetry retry.Policy

	// Observe, if set, receives progress events.
	Observe func(Event)

	// Gate, if set, can send a final answer back for more work (at most
	// MaxNudges times, default 3).
	Gate      Gate
	MaxNudges int

	// Shape, if set, rewrites a tool result before the model sees it (for
	// example summarising long number series). Observers get both versions.
	Shape func(tool, text string) (string, bool)

	// Compact, if set, shapes what the model sees each turn (for example
	// shortening old tool results). The full history stays in Result.Messages.
	Compact func([]Message) []Message
}

// Result is the outcome of a run.
type Result struct {
	Stop      StopReason
	Turns     int
	Usage     Usage
	FinalText string
	Messages  []Message
	Err       error
}

var tracer = otel.Tracer("github.com/arsaljamal/drill-down-harness/agent")

// Run executes task until a stop condition is met.
func (r *Runner) Run(ctx context.Context, task string) (res Result) {
	lim := r.Limits
	if lim.MaxTurns <= 0 {
		lim.MaxTurns = 20
	}
	if lim.MaxRepeats <= 0 {
		lim.MaxRepeats = 3
	}
	maxOut := r.MaxOutput
	if maxOut <= 0 {
		maxOut = 4096
	}
	if lim.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lim.MaxDuration)
		defer cancel()
	}

	ctx, span := tracer.Start(ctx, "drill.run")
	defer func() {
		span.SetAttributes(
			attribute.String("drill.stop_reason", string(res.Stop)),
			attribute.Int("drill.turns", res.Turns),
			attribute.Int64("drill.tokens.input", res.Usage.InputTokens),
			attribute.Int64("drill.tokens.output", res.Usage.OutputTokens),
		)
		if res.Err != nil {
			span.RecordError(res.Err)
			span.SetStatus(codes.Error, string(res.Stop))
		}
		span.End()
		r.emit(Event{Turn: res.Turns, Kind: "stop", Message: string(res.Stop)})
	}()

	res.Messages = []Message{{Role: RoleUser, Blocks: []Block{{Kind: BlockText, Text: task}}}}

	specs, err := r.Tools.Tools(ctx)
	if err != nil {
		res.Stop, res.Err = StopToolsError, err
		return res
	}

	var lastCall []byte
	repeats, nudges := 0, 0
	maxNudges := r.MaxNudges
	if maxNudges <= 0 {
		maxNudges = 3
	}

	for {
		if stop, ok := r.ctxStop(ctx); ok {
			res.Stop, res.Err = stop, ctx.Err()
			return res
		}
		if res.Turns >= lim.MaxTurns {
			res.Stop = StopMaxTurns
			return res
		}
		if lim.MaxTokens > 0 && res.Usage.Total() >= lim.MaxTokens {
			res.Stop = StopTokenBudget
			return res
		}

		res.Turns++
		turnCtx, turnSpan := tracer.Start(ctx, "drill.turn", trace.WithAttributes(attribute.Int("drill.turn", res.Turns)))

		view := res.Messages
		if r.Compact != nil {
			view = r.Compact(view)
		}
		reply, err := r.generate(turnCtx, ModelRequest{System: r.System, Messages: view, Tools: specs, MaxTokens: maxOut})
		if err != nil {
			turnSpan.RecordError(err)
			turnSpan.End()
			if stop, ok := r.ctxStop(ctx); ok {
				res.Stop, res.Err = stop, err
			} else {
				res.Stop, res.Err = StopModelError, err
			}
			return res
		}
		res.Usage.add(reply.Usage)
		res.Messages = append(res.Messages, Message{Role: RoleAssistant, Blocks: reply.Blocks})
		r.emit(Event{Turn: res.Turns, Kind: "model_reply", Message: summarize(reply.Blocks)})

		calls := toolCalls(reply.Blocks)
		if len(calls) == 0 && r.Gate != nil && nudges < maxNudges {
			if nudge := r.Gate(res.Messages, textOf(reply.Blocks)); nudge != "" {
				nudges++
				r.emit(Event{Turn: res.Turns, Kind: "gate", Message: nudge})
				res.Messages = append(res.Messages, Message{Role: RoleUser, Blocks: []Block{{Kind: BlockText, Text: nudge}}})
				turnSpan.End()
				continue
			}
		}
		if len(calls) == 0 {
			res.FinalText = textOf(reply.Blocks)
			res.Stop = StopDone
			turnSpan.End()
			return res
		}

		results := make([]Block, 0, len(calls))
		for _, c := range calls {
			key := append([]byte(c.ToolName+"\x00"), c.Input...)
			if bytes.Equal(key, lastCall) {
				repeats++
			} else {
				lastCall, repeats = key, 1
			}
			if repeats >= lim.MaxRepeats {
				turnSpan.End()
				res.Stop = StopStuck
				res.Err = fmt.Errorf("tool %q called %d times in a row with the same input", c.ToolName, repeats)
				return res
			}
			results = append(results, r.callTool(turnCtx, res.Turns, c))
		}
		res.Messages = append(res.Messages, Message{Role: RoleUser, Blocks: results})
		turnSpan.End()
	}
}

func (r *Runner) generate(ctx context.Context, req ModelRequest) (ModelReply, error) {
	ctx, span := tracer.Start(ctx, "drill.model_call")
	defer span.End()

	policy := r.ModelRetry
	userHook := policy.OnRetry
	retries := 0
	policy.OnRetry = func(attempt int, err error, wait time.Duration) {
		retries++
		r.emit(Event{Kind: "retry", Message: fmt.Sprintf("model call attempt %d failed (%v); retrying in %s", attempt, err, wait.Round(time.Millisecond))})
		if userHook != nil {
			userHook(attempt, err, wait)
		}
	}

	var reply ModelReply
	err := retry.Do(ctx, policy, func(ctx context.Context) error {
		var err error
		reply, err = r.Model.Generate(ctx, req)
		return err
	})
	span.SetAttributes(attribute.Int("drill.retries", retries))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "model call failed")
		return ModelReply{}, err
	}
	span.SetAttributes(
		attribute.String("drill.model.stop_reason", reply.StopReason),
		attribute.Int64("drill.tokens.input", reply.Usage.InputTokens),
		attribute.Int64("drill.tokens.output", reply.Usage.OutputTokens),
	)
	return reply, nil
}

// callTool runs one tool call. Tool failures are returned to the model as
// error results so it can recover; they do not end the run.
func (r *Runner) callTool(ctx context.Context, turn int, c Block) Block {
	ctx, span := tracer.Start(ctx, "drill.tool_call", trace.WithAttributes(attribute.String("drill.tool", c.ToolName)))
	defer span.End()

	r.emit(Event{Turn: turn, Kind: "tool_call", Message: fmt.Sprintf("%s %s", c.ToolName, c.Input)})
	out, err := r.Tools.Call(ctx, c.ToolName, c.Input)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool call failed")
		out = ToolResult{Text: "tool call failed: " + err.Error(), IsError: true}
	}
	span.SetAttributes(attribute.Bool("drill.tool.is_error", out.IsError))
	r.emit(Event{Turn: turn, Kind: "tool_result", Message: out.Text})
	if r.Shape != nil && !out.IsError {
		if shaped, ok := r.Shape(c.ToolName, out.Text); ok {
			out.Text = shaped
			r.emit(Event{Turn: turn, Kind: "lens", Message: shaped})
		}
	}
	return Block{Kind: BlockToolResult, ToolUseID: c.ToolUseID, Text: out.Text, IsError: out.IsError}
}

// ctxStop maps a finished context to a stop reason.
func (r *Runner) ctxStop(ctx context.Context) (StopReason, bool) {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return StopTimeout, true
	case ctx.Err() != nil:
		return StopCancelled, true
	}
	return "", false
}

func (r *Runner) emit(e Event) {
	if r.Observe != nil {
		r.Observe(e)
	}
}

func toolCalls(blocks []Block) []Block {
	var out []Block
	for _, b := range blocks {
		if b.Kind == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

func textOf(blocks []Block) string {
	var buf bytes.Buffer
	for _, b := range blocks {
		if b.Kind == BlockText {
			if buf.Len() > 0 {
				buf.WriteString("\n")
			}
			buf.WriteString(b.Text)
		}
	}
	return buf.String()
}

func summarize(blocks []Block) string {
	if t := textOf(blocks); t != "" {
		return t
	}
	return fmt.Sprintf("%d tool call(s)", len(toolCalls(blocks)))
}

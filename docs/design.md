# Keel: design

Status: draft v0.1 (2026-11-10). Author: Arsal Jamal.

Keel is an agent harness written in Go: the engine around a language model that turns it into an agent that can use tools, survive failures and be measured. The name comes from a ship's keel, the part that keeps it steady.

## 1. Why

Most agent demos work on the happy path. In production, the hard parts are the same ones distributed systems have always had:

- a tool call times out: retry it, or did it already run?
- the process crashes halfway through a task: start over, or resume?
- a step keeps failing: loop forever, or park it somewhere safe?
- a change to the prompt or model: did the agent get better or worse?

Keel treats an agent run like a message pipeline: every step is recorded, every side effect is classified, failures go to a dead-letter area instead of disappearing, and quality is measured with evals.

## 2. Goals and non-goals

**Goals**
- A small, readable agent loop with clear stop conditions.
- Tools through MCP (Model Context Protocol), so any MCP server plugs in.
- Reliability as a first-class feature: retries, idempotency, dead-lettering, crash recovery.
- Every step traced with OpenTelemetry.
- An evals mode that scores the agent on a task suite and catches regressions in CI.

**Non-goals (for now)**
- Multi-agent orchestration.
- A UI. Keel is a CLI plus a Go library.
- Training or fine-tuning models.

## 3. Architecture

```
            +-------------------------------------------------+
  task ---> |                  Runner (agent loop)            |
            |  plan -> call model -> run tools -> observe     |
            +--+-----------+-----------+-----------+----------+
               |           |           |           |
          Model client  Tool layer  Policy gate  Context manager
          (Claude API)  (MCP client) (approvals)  (compaction)
               |           |           |
               +-----+-----+-----+-----+
                     |           |
                  Journal    Dead-letter store
                 (SQLite)       (SQLite)
                     |
                Telemetry (OpenTelemetry traces + metrics)
```

| Component | Responsibility |
|---|---|
| Runner | Drives the loop, enforces limits, decides when to stop. |
| Model client | Calls the model. Interface-based; first implementation uses the Claude API (`anthropic-sdk-go`). |
| Tool layer | Connects to MCP servers with the official Go SDK (`modelcontextprotocol/go-sdk`), lists tools, executes calls. |
| Policy gate | Classifies each tool call and decides: run, ask a human, or refuse. |
| Journal | Append-only record of every step. Source of truth for resume and replay. |
| Dead-letter store | Steps that failed permanently or are unsafe to retry, with the reason. |
| Context manager | Keeps the conversation inside the model's context window. |
| Telemetry | A span per run, turn and tool call; metrics for latency, tokens, retries, failures. |

## 4. The agent loop

```
run(task):
    journal.start(run)
    while true:
        check limits (turns, tokens, cost, wall time)      -> stop: limit_reached
        context = context_manager.build(journal)
        reply   = model.call(context, tools)                -> retried on transient errors
        journal.append(model_turn)
        if reply has no tool calls:                         -> stop: done
        for call in reply.tool_calls:
            decision = policy.check(call)                   -> run | ask | refuse
            result   = tools.execute(call, idempotency_key) -> retried by side-effect class
            journal.append(tool_result)
        if no progress for N turns:                         -> stop: stuck
```

**Stop conditions:** the model finishes; a limit is hit (turns, tokens, cost, wall time); no progress is detected (same tool call with same arguments repeated, or the same error repeating); or a human rejects a required approval. Every stop records a reason, so a run never just "ends".

## 5. Reliability

### 5.1 Side-effect classes

Every tool is classified, by configuration or by MCP tool annotations (`readOnlyHint`, `idempotentHint`, `destructiveHint`):

| Class | Example | Retry policy | Approval |
|---|---|---|---|
| Read-only | search, read file, list queue | Retry with backoff | No |
| Idempotent write | set a value, upsert by key | Retry with the same idempotency key | Configurable |
| Unsafe write | send a message, charge a card, replay a message | Never retried automatically | Required by default |

### 5.2 Retries
- Model calls: exponential backoff with jitter on rate limits, overload and network errors; a retry budget per run.
- Tool calls: according to the side-effect class. Each call carries an idempotency key `(run_id, step_no, call_index)` so a cooperating tool can ignore duplicates.

### 5.3 Dead-lettering
A step goes to the dead-letter store when it fails after its retry budget, when an unsafe call's outcome is unknown (for example a timeout after the request was sent), or when a human rejects it. Each record keeps the full call, the error, the attempt count and a classification (transient, permanent, unknown-outcome, rejected). Dead-lettered steps can be inspected and replayed, dry-run by default.

### 5.4 Crash recovery
The journal is written before and after every step (`step_started`, `step_completed`). On restart, `keel resume <run_id>`:
- replays completed steps from the journal without re-executing them;
- re-runs an interrupted read-only or idempotent step;
- sends an interrupted unsafe step to the dead-letter store as unknown-outcome, instead of guessing, so nothing is sent twice.

## 6. Context management
Strategies, chosen per run:
1. Keep everything (short tasks).
2. Trim old tool results to a summary line, keeping the full result in the journal.
3. Summarise older turns with the model once usage passes a threshold (for example 70% of the window).
The journal always keeps the full history; only what is sent to the model changes.

## 7. Observability
- Traces: `run` -> `turn` -> `model_call` / `tool_call` spans, with attributes for model, tokens, side-effect class, retry count and stop reason.
- Metrics: run duration, turns per run, tokens and cost per run, tool latency, retries, dead-lettered steps by class.
- A Grafana dashboard ships with the repo.

## 8. Evals
- A task suite is a YAML file: task prompt, MCP servers to load, limits, and graders.
- Graders: exact match, a script that checks the end state, or a model-as-judge with a rubric.
- Each task runs N times; the report shows pass rate, cost, turns and stop reasons.
- `keel eval --baseline main` compares against a previous run and fails CI on a regression.

## 9. Tech choices
- Go (1.25+). Single binary, good concurrency, strongest language of the author.
- `github.com/modelcontextprotocol/go-sdk` for MCP.
- `github.com/anthropics/anthropic-sdk-go` for the Claude API. Default model configurable.
- SQLite for the journal and dead-letter store (pure-Go driver), so it runs anywhere with no setup.
- OpenTelemetry for traces and metrics.

## 10. Milestones

| Version | Scope | Target |
|---|---|---|
| v0.1 | Loop, Claude client, MCP tools over stdio, limits and stop reasons, model-call retries, OpenTelemetry traces | 2026-11-24 |
| v0.2 | Journal, side-effect classes, tool retries, idempotency keys, dead-letter store | 2026-12-01 |
| v0.3 | Crash recovery (`resume`), approval gate | 2026-12-08 |
| v0.4 | Context management strategies | 2026-12-15 |
| v0.5 | Evals mode and CI regression check; DLQ Doctor demo | 2026-12-22 |
| later | Rust command sandbox | 2027 |

## 11. Open questions
- Should the journal format be stable enough for other tools to read?
- How should approvals work in non-interactive runs (CI, cron)? Deny by default, or a policy file?
- Cost limits need token prices per model: config file, or fetched?

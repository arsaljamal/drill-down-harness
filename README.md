# Keel

An agent harness in Go, built for reliability: the engine around a language model that lets it use tools through MCP, recover from failures, and be measured with evals.

> Status: **v0.1 in progress.** The agent loop, Claude client, MCP tools, limits, model-call retries and tracing work today. See [docs/design.md](docs/design.md) for the full design and roadmap.

## Try it

```sh
export ANTHROPIC_API_KEY=...
go run ./cmd/keel run --mcp "go run ./examples/demoserver" "What is 21.5 + 20.5?"
```

Add `--trace` to print OpenTelemetry spans for the run, each turn, each model call and each tool call.

## Layout

| Package | What it does |
|---|---|
| `agent` | Provider-neutral types and the agent loop, with stop reasons: done, max_turns, token_budget, timeout, stuck, model_error, cancelled |
| `model` | Claude adapter; SDK retries are off so Keel owns and traces every retry |
| `tools` | MCP toolbox on the official Go SDK; classifies tools as read-only, idempotent or unsafe from MCP annotations |
| `retry` | Exponential backoff with full jitter |
| `examples/demoserver` | A tiny MCP server to try Keel against |

## What it does (planned)

- **Agent loop** with clear stop conditions: done, limit reached, stuck, or rejected.
- **Tools through MCP**, using the official Go SDK.
- **Reliability built in:** retries with backoff, idempotency keys, side-effect classes, a dead-letter store for failed steps, and resume after a crash without sending anything twice.
- **Tracing** of every step with OpenTelemetry.
- **Evals** that score the agent on a task suite and catch regressions in CI.

## Why

Agents fail the same way distributed systems do: timeouts, retries, partial failures, silent loss. Keel applies the patterns that keep message pipelines reliable to agent runs.

## License

MIT

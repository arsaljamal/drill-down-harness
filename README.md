# Keel

An agent harness in Go, built for reliability: the engine around a language model that lets it use tools through MCP, recover from failures, and be measured with evals.

> Status: design stage. See [docs/design.md](docs/design.md).

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

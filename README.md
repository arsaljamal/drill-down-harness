# Keel

An agent harness in Go, built for reliability: the engine around a language model that lets it use tools through MCP, recover from failures, and be measured with evals.

> Status: **v0.1 in progress.** The agent loop, Claude client, MCP tools, limits, model-call retries and tracing work today. See [docs/design.md](docs/design.md) for the full design and roadmap.

## Try it

Runs on any model. With [Ollama](https://ollama.com) it runs on your own machine and needs no API key:

```sh
ollama pull qwen3
go run ./cmd/keel run --model ollama/qwen3 --mcp "go run ./examples/demoserver" "What is 21.5 + 20.5?"
```

Or use a hosted model (the key is read from the environment, never from a file):

```sh
export ANTHROPIC_API_KEY=...
go run ./cmd/keel run --model anthropic/claude-sonnet-5-5 --mcp "go run ./examples/demoserver" "What is 21.5 + 20.5?"
```

| `--model` prefix | Talks to | Key |
|---|---|---|
| `anthropic/` | Claude Messages API | `ANTHROPIC_API_KEY` |
| `openai/` | OpenAI | `OPENAI_API_KEY` |
| `gemini/` | Gemini's OpenAI-compatible endpoint | `GEMINI_API_KEY` |
| `openrouter/` | OpenRouter | `OPENROUTER_API_KEY` |
| `groq/` | Groq | `GROQ_API_KEY` |
| `ollama/`, `lmstudio/` | a local server | none |
| `compatible/` | any OpenAI-compatible server, with `--base-url` | `--api-key-env` if needed |

Add `--trace` to print OpenTelemetry spans for the run, each turn, each model call and each tool call.

## Layout

| Package | What it does |
|---|---|
| `agent` | Provider-neutral types and the agent loop, with stop reasons: done, max_turns, token_budget, timeout, stuck, model_error, cancelled |
| `model` | Two adapters: Claude, and any OpenAI-compatible server. Retries are owned and traced by Keel |
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

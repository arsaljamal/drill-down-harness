# Drill Down Harness

An on-call investigator. When a page fires at 3 a.m. ("traffic on /refresh doubled"), tell it to find out what is happening. It knows how your product fits together, checks the right systems, proposes how to contain the damage, drills down to the source with evidence, and writes the report.

Underneath is a reliability-first agent harness in Go: any model, tools through MCP, safe actions, retries and full tracing. The command is `drill`.

> Status: **v0.1** (harness core) works today. The investigator is being built on top. See [docs/design.md](docs/design.md) for the full design and roadmap.

## How an investigation runs

| Phase | What happens |
|---|---|
| **Map** | Learns the product before anything breaks: entry points, services, endpoints, dashboards, alerts, deploys. |
| **Triage** | How big is it, are real users hurt, act now or wait? |
| **Contain** | Proposes rate limits, flag changes or blocks, with blast radius. Nothing runs without your yes. |
| **Drill down** | Lists every way traffic reaches the endpoint, splits the spike, rules suspects out with evidence. |
| **Report** | Reproduction steps and an incident report where every number links to its query. |

## Try the harness

Runs on any model. With [Ollama](https://ollama.com) it runs on your own machine and needs no API key:

```sh
ollama pull qwen3
go run ./cmd/drill run --model ollama/qwen3 --mcp "go run ./examples/demoserver" "What is 21.5 + 20.5?"
```

Or use a hosted model (the key is read from the environment, never from a file):

```sh
export ANTHROPIC_API_KEY=...
go run ./cmd/drill run --model anthropic/claude-sonnet-5-5 --mcp "go run ./examples/demoserver" "What is 21.5 + 20.5?"
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
| `agent` | Provider-neutral types and the agent loop, with stop reasons: done, max_turns, token_budget, timeout, stuck, model_error, tools_error, cancelled |
| `model` | Two adapters: Claude, and any OpenAI-compatible server. Retries are owned and traced by the harness |
| `tools` | MCP toolbox on the official Go SDK; classifies tools as read-only, idempotent or unsafe from MCP annotations |
| `retry` | Exponential backoff with full jitter |
| `examples/demoserver` | A tiny MCP server to try the harness against |

## Principles

- **Evidence or it didn't happen:** every finding cites the tool call behind it.
- **Read freely, act carefully:** investigation is read-only; anything that changes production waits for approval.
- **Say what you can't see:** missing access is reported as a gap, never guessed.

## License

MIT

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

## Domains

- **Reliability** (now): traffic spikes, errors, latency, failed dependencies.
- **Security** (planned, v0.7): leaked secrets, vulnerable dependencies, cloud misconfiguration, risky identities, attack investigation. Defensive and read-only by default; secrets are never shown in full.

## Install

Release downloads for macOS, Linux and Windows, and a Docker image (`ghcr.io/arsaljamal/drill`), are published from the first tagged release. Until then:

```sh
go install github.com/arsaljamal/drill-down-harness/cmd/drill@latest
```

## Configure

```sh
drill init      # writes an example drill.yaml
```

`drill.yaml` holds the model, the MCP servers (any language; stdio `command` or HTTP `url`), run limits and the approval rule for unsafe tools. `${VAR}` values are read from your environment, so tokens never live in the file. A typo in a setting is an error, not silently ignored. Command-line flags override the file.

```yaml
version: 1
model: ollama/qwen3
limits: { max_turns: 40, max_tokens: 400000, timeout: 15m }
mcp_servers:
  grafana:
    command: mcp-grafana
    env: { GRAFANA_URL: "http://localhost:3000", GRAFANA_SERVICE_ACCOUNT_TOKEN: "${GRAFANA_TOKEN}" }
  github:
    url: https://api.githubcopilot.com/mcp/
    headers: { Authorization: "Bearer ${GITHUB_TOKEN}" }
approvals:
  unsafe: ask     # ask | deny | allow
```

With more than one server, tools are named `<server>__<tool>`. Unsafe tools (no read-only or idempotent annotation) ask for a yes on the terminal; with no terminal (CI, cron) the answer is no.

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

## Benchmark: ITBench-AA

`drill bench itbench` scores the harness on the 40 public ITBench-AA SRE tasks (offline Kubernetes incident snapshots), using the official task prompt and grader, so results are comparable with the [ITBench-AA leaderboard](https://artificialanalysis.ai/evaluations/itbench-aa).

```sh
drill bench itbench --check --scenarios Scenario-1        # free setup check: submits the answer key, must score 1.0
drill bench itbench --model anthropic/claude-sonnet-5-5 \
  --limit 5 --stop-below 0.3 --min-tasks 5 --price-in 3 --price-out 15
```

Tasks run one at a time: download the snapshot, investigate inside a Docker sandbox (data read-only, no network, the answer key never inside), grade, delete the data. `--stop-below` stops early if the running score falls under a threshold. Results go to `bench-results/itbench/<time>/results.jsonl`.

## Layout

| Package | What it does |
|---|---|
| `agent` | Provider-neutral types and the agent loop, with stop reasons: done, max_turns, token_budget, timeout, stuck, model_error, tools_error, cancelled |
| `model` | Two adapters: Claude, and any OpenAI-compatible server. Retries are owned and traced by the harness |
| `tools` | MCP toolbox on the official Go SDK; classifies tools as read-only, idempotent or unsafe; combines several servers and applies the approval rule |
| `config` | Loads and checks `drill.yaml` |
| `bench/itbench` | ITBench-AA runner: download, sandbox, official grader, early stop |
| `retry` | Exponential backoff with full jitter |
| `examples/demoserver` | A tiny MCP server to try the harness against |

## Principles

- **Go core, language-free edges:** you never need Go to run it, connect systems (MCP in any language), describe your product (YAML and Markdown playbooks) or trigger it (HTTP and webhooks). Release binaries and a Docker image are on the roadmap.

- **Evidence or it didn't happen:** every finding cites the tool call behind it.
- **Read freely, act carefully:** investigation is read-only; anything that changes production waits for approval.
- **Say what you can't see:** missing access is reported as a gap, never guessed.

## License

MIT

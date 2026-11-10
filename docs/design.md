# Drill Down Harness: design

Status: draft v0.3 (2026-11-10). Author: Arsal Jamal.

The Drill Down Harness is an on-call investigator. When a page fires in the middle of the night ("traffic on /refresh doubled"), you tell it to find out what is happening. It already knows how your product fits together, so it checks the right systems, contains the damage once you approve, drills down to the source with evidence, and writes the report.

Underneath is a general agent harness written in Go: the engine around a language model that runs the loop, connects tools through MCP, keeps actions safe, survives failures and records every step. The command is `drill`.

## 1. The problem

A page at 03:00 says an endpoint is getting far more traffic than usual. The on-call engineer has to:

1. work out if real users are hurt, while half awake;
2. stop the damage before knowing the cause;
3. find the source among many possible entry points: mobile apps, web, partners, cron jobs, queues, bots, a push campaign, a bad release;
4. reproduce it, then make sure it can't happen again.

Each step means jumping between Grafana, Loki, Sentry, the CDN, the gateway, the code and the deploy history. Most of the time goes on finding where to look, not on thinking.

## 2. What it does

| Phase | What happens | Target time |
|---|---|---|
| **0. Map** | Before anything breaks, it builds a product map: entry points (CDN and gateway routes, apps, web, partners, cron, queues), which service owns which endpoint, call paths from traces, dashboards, alerts, error projects, repos, and recent deploys and flag changes. Refreshed on a schedule. | nightly |
| **1. Triage** | The page triggers a run. It answers: how big is it, are real users affected (errors, latency, foreground vs background traffic), and does this need action now? | < 2 min |
| **2. Contain** | It proposes ways to stop the damage, each with its blast radius: rate-limit at the edge, turn a flag off, block one app version or network, scale up. Nothing runs without a human yes. | minutes |
| **3. Drill down** | From the map, it lists every way traffic can reach the endpoint, splits the spike by platform, app version, country, network and client, lines it up with change events, and rules suspects out one by one. A cause is accepted only when it fits every test. | 5-15 min |
| **4. Reproduce and report** | It writes the steps or a script to reproduce the issue (staging only, with approval) and an incident report where every number links to the query that produced it, plus a runbook entry for next time. | after |

## 3. Principles

- **Evidence or it didn't happen.** Every claim in a finding cites the tool call that produced it. The journal is the audit trail.
- **Read freely, act carefully.** Investigation tools are read-only and run without asking. Anything that changes production (rate limits, flags, scaling, restarts) is unsafe and waits for approval.
- **Say what you can't see.** Missing access (for example a push provider console that needs owner rights) is reported as an open gap, never filled with a guess.
- **Any model.** The harness talks to models through one interface: Claude natively, and anything OpenAI-compatible (OpenAI, Gemini, OpenRouter, Groq, or a local model through Ollama with no API key).
- **Plug in, don't rewrite.** Systems are reached through MCP servers, preferring the official ones (Grafana, Sentry, Cloudflare, GitHub, Kubernetes).
- **Go core, language-free edges.** The engine is Go. Nobody who runs it, connects a system to it, describes their product to it or triggers it needs Go: those edges are a downloaded binary, MCP (any language), YAML, Markdown, HTTP and JSON. Only people changing the engine itself write Go.

## 4. Architecture

```
 page / alert webhook / CLI
            |
   +--------v---------------------------------------------------+
   |                    Investigation runner                     |
   |  phases: triage -> contain -> drill down -> report          |
   |  hypothesis board: suspects, tests, evidence, verdicts      |
   +--+-------------+-------------+-------------+----------------+
      |             |             |             |
   Product map   Agent loop    Policy gate   Report writer
   (graph)       (harness)     (approvals)   (markdown/HTML)
      |             |
      |     +-------+--------+------------------+
      |     |                |                  |
      |  Model client    Tool layer (MCP)    Journal + dead-letter store
      |  Claude / OpenAI-  Grafana, Loki,     (SQLite)
      |  compatible        Sentry, CDN,
      |                    gateway, GitHub,
      +------------------> k8s, flags ...
                               |
                       Telemetry (OpenTelemetry)
```

### 4.1 Harness core (built, v0.1)
- **Agent loop** with eight stop reasons: done, max_turns, token_budget, timeout, stuck, model_error, tools_error, cancelled.
- **Model adapters:** Claude, and OpenAI-compatible servers. The SDK's own retries are off; the harness retries with exponential backoff and full jitter, so every retry is counted and traced.
- **MCP tool layer** on the official Go SDK. Each tool is classified from its annotations: read-only, idempotent or unsafe (unsafe by default).
- **Tracing:** a span per run, turn, model call and tool call.

### 4.2 Product map
A graph stored in SQLite:
- nodes: entry point, route, service, endpoint, job, queue, dashboard, alert, error project, repo, client app;
- edges: routes-to, calls, owns, alerts-on, deployed-from, publishes-to;
- change events: deploys, flag changes, config pushes, campaigns, with timestamps.

Built by read-only discovery runs: gateway and CDN routes, trace service graphs, Prometheus label values, Loki streams, Sentry projects, Kubernetes services, repo manifests. Each fact keeps its source so it can be refreshed or questioned.

### 4.3 Hypothesis board
For each investigation: a list of suspects, each with status (open, ruled out, fits), the tests run against it, and the evidence (tool call IDs from the journal). The model proposes suspects and tests; the harness enforces that a verdict always has evidence attached.

### 4.4 Reliability (from the original harness design)
- **Side-effect classes** drive retries and approvals: read-only retried freely; idempotent retried with an idempotency key; unsafe never retried automatically.
- **Journal** written before and after each step; `resume` replays completed steps and parks an interrupted unsafe step as unknown-outcome instead of guessing.
- **Dead-letter store** for steps that fail permanently, with the reason and a dry-run replay.
- **Context management:** long log and metric results are summarised before they reach the model; the journal keeps the full data.

### 4.5 Language-free edges

| Who | What they touch | Format |
|---|---|---|
| On-call engineer | `drill` binary (macOS, Linux, Windows), Homebrew, Docker image | built by GoReleaser on every tag; no Go toolchain needed |
| Team connecting a system | MCP servers over stdio or HTTP | any language; official servers preferred; our demo connectors include Python ones |
| Team describing its product | `drill.yaml` and `playbooks/*.md` | YAML and Markdown |
| Alerting and chat tools | HTTP API and alert webhook | JSON in, JSON + report out |
| Other tools reading results | product map, hypothesis board, report | versioned JSON schemas |

**`drill.yaml`** replaces long command-line flags:

```yaml
version: 1
model: anthropic/claude-sonnet-5-5      # or ollama/qwen3, openai/<model>, ...
api_key_env: ANTHROPIC_API_KEY          # the key itself is never in the file
limits: { max_turns: 40, max_tokens: 400000, timeout: 15m }
mcp_servers:
  grafana: { command: "mcp-grafana", env: { GRAFANA_URL: "http://localhost:3000" } }
  github:  { url: "https://api.githubcopilot.com/mcp/" }
approvals:
  unsafe: ask          # ask | deny
  notify: cli          # cli first; chat and phone later
playbooks: ./playbooks
```

**Playbooks** are Markdown files the investigator reads as hints, matched by alert name or endpoint:

```markdown
---
match: { alert: "TrafficSpike", endpoint: "/auth/refresh" }
---
Check first: background vs foreground traffic, platform split, recent push campaigns.
Known benign cause: nightly token refresh from the partner API at 02:00.
```

**HTTP trigger:** `drill serve` exposes `POST /v1/investigations` (start one, from a webhook or by hand), `GET /v1/investigations/{id}` (status, board, report) and `POST /v1/approvals/{id}` (approve or deny a proposed action). Alertmanager and Grafana alert webhooks are accepted directly.

**Versioned outputs:** `map.v1.json`, `board.v1.json` and `report.v1.json` are published as JSON Schemas in `schemas/`, so other tools can read results in any language.

## 5. Landscape: what others do, and the gaps we target

Checked 2026-11-10.

| Group | Examples | What they do well | Limit |
|---|---|---|---|
| Closed AI SRE agents | Resolve AI, Traversal, Cleric, Neubird | parallel hypotheses, causal search, operational memory | data goes to the vendor; accuracy claims hard to verify |
| Observability incumbents | Datadog Bits AI SRE, Dynatrace, New Relic, AWS DevOps Agent | low friction on their own data, audit trails | reasoning stops at their own data |
| Open-source harnesses | HolmesGPT (CNCF sandbox, Python), K8sGPT, OpenSRE (Python, Claude Agent SDK), Aurora (Apache 2.0) | open, self-hosted, MCP and runbooks, some human-gated remediation | mostly Kubernetes and error/latency faults; few reproducible accuracy numbers |

What research says agents get wrong (Kim et al., 2026, "Why do AI agents systematically fail at cloud root cause analysis?"; ORCA-bench, 2026):
- **Hallucinated interpretation** of data (about 71% of failed runs) and **symptom taken as cause** (about 40%).
- **Incomplete exploration**: whole components or signal types skipped (about 64%); metrics used, logs and traces ignored.
- **Bad queries**: 26-40% of telemetry queries return nothing or error.
- **Timestamp and timezone mistakes** (about 23%).
- **Concurrent faults**: agents stop at the first cause.
- **Source code under-used** even though it helps.
- Best models reach only 10-25% accuracy on harder on-call tasks; prompting alone does not fix this, the framework has to.

How the Drill Down Harness answers each one, in the framework rather than the prompt:

| Gap | Answer |
|---|---|
| Hallucination, symptom-as-cause | Hypothesis board: a cause is accepted only if it fits every test, and each test result is a cited tool call. A separate verify step re-reads the evidence before the report. |
| Incomplete exploration | The product map gives a coverage checklist: every entry point and every signal type (metrics, logs, traces, errors, changes) for the affected endpoint must be checked or marked unavailable. |
| Bad queries | Query helpers built from discovered label values and log fields; empty results are flagged, retried with a corrected query, and counted. |
| Timestamps | All tool inputs and outputs normalised to UTC by the harness, never by the model. |
| Concurrent faults | The board allows more than one "fits" verdict and asks whether the fits explain the whole spike. |
| Code under-used | The map links endpoints to repos and handlers, so reading code is a planned step. |
| "Why is traffic up?" | Most tools focus on errors, latency and Kubernetes faults. We also target traffic-origin questions: which client, version, network, partner, job or campaign is sending it. |
| Unverifiable claims | Every score is reproducible from the public eval suite. |

## 6. Testing

Five layers, cheapest first:

1. **Unit tests** (today: 20): loop, stop reasons, retries, adapters, MCP toolbox. Run on every push.
2. **Replay tests:** recorded tool responses plus a scripted model make full investigations deterministic and free. They check harness rules: no verdict without evidence, coverage checklist enforced, UTC normalisation, approvals required for unsafe tools.
3. **Scenario evals on the OpenTelemetry Demo** (the "Astronomy Shop": many services in many languages, an Envoy front proxy, a Locust load generator, and built-in fault flags through flagd). We reuse it instead of building our own demo company, and add our own traffic-origin scenarios on top through the load generator: background-wake storm, retry storm from one client version, bot flood from one network, cron misconfiguration, campaign spike. Each scenario has a known root cause.
4. **Public benchmarks:** run a subset of ORCA-bench (built on the same OpenTelemetry Demo) and RCAEval, and publish the numbers, good or bad.
5. **Dogfooding:** run it against personal and demo systems only; never against employer systems from this repo.

What we measure per run: root cause correct, all causes found (for concurrent faults), every claim cited, hallucinated claims, coverage of the checklist, empty-query rate, time to triage, time to cause, tokens and cost, model used. `drill eval` prints a table and fails CI if accuracy or citation rate drops against the last baseline. Expensive eval runs are nightly, not on every push; a local model through Ollama keeps a cheap smoke run on every push.

## 7. Roadmap

| Version | Scope | Target |
|---|---|---|
| v0.1 | Harness core: loop, Claude and OpenAI-compatible models, MCP tools, limits, retries, tracing | done 2026-11-10 |
| v0.2 | Language-free edges: `drill.yaml`, release binaries and Docker image, versioned JSON schemas; replay-test framework | 2026-11-24 |
| v0.3 | Eval rig: OpenTelemetry Demo with Grafana stack, two flagd faults and two traffic-origin scenarios, `drill eval` | 2026-12-01 |
| v0.4 | Product map (`drill map`) and coverage checklist | 2026-12-08 |
| v0.5 | Investigate: triage, hypothesis board, evidence rules, verify step, playbooks | 2026-12-15 |
| v0.6 | Contain and serve: approvals, journal, resume, `drill serve` with webhook trigger; first published eval numbers | 2026-12-22 |
| later | Report polish, ORCA-bench subset, chat/phone approvals, Rust sandbox for reproduction scripts | 2027 |

## 8. Open questions
- How is the map kept fresh without expensive full rediscovery? Incremental refresh per source?
- What is the minimum read access a team must grant for useful results, and how is it described to them?
- Containment approvals at 03:00: phone notification, chat message, or CLI only at first?
- How much of the investigation can run on a local model, and where is a hosted model needed?

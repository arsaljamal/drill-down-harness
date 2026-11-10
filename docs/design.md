# Drill Down Harness: design

Status: draft v0.2 (2026-11-10). Author: Arsal Jamal.

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

## 5. Evals: a demo company we break on purpose

The public version runs against a small company we build ourselves, in Docker Compose:
- an edge gateway with rate limiting (stands in for a CDN);
- an auth service with `/login` and `/refresh`, plus two or three other services;
- simulated clients: Android, iOS, web, a partner, a cron job;
- Prometheus, Loki, Tempo and Grafana.

Fault scenarios, each with a known root cause:
1. background-wake storm: a push makes idle Android clients wake and refresh at once;
2. retry storm from one bad app version;
3. bot flood from one network;
4. misconfigured cron job running every minute;
5. bad deploy raising latency;
6. cache expiry causing a thundering herd.

`drill eval` runs each scenario, scores whether the right cause was found, whether every claim had evidence, time to answer and cost, and fails CI on a regression. No real company data is used anywhere.

## 6. Roadmap

| Version | Scope | Target |
|---|---|---|
| v0.1 | Harness core: loop, Claude and OpenAI-compatible models, MCP tools, limits, retries, tracing | done 2026-11-10 |
| v0.2 | Demo company in Docker Compose with Grafana stack; first two fault scenarios | 2026-11-24 |
| v0.3 | Product map: discovery runs, graph store, `drill map` | 2026-12-01 |
| v0.4 | Triage and drill down: hypothesis board, evidence rules, `drill investigate` | 2026-12-08 |
| v0.5 | Contain: policy gate, approvals, journal, resume, dead-letter store | 2026-12-15 |
| v0.6 | Report and evals: incident report, runbook entry, `drill eval` in CI over all six scenarios | 2026-12-22 |
| later | Alert webhook trigger, Rust sandbox for reproduction scripts, more connectors | 2027 |

## 7. Open questions
- How is the map kept fresh without expensive full rediscovery? Incremental refresh per source?
- What is the minimum read access a team must grant for useful results, and how is it described to them?
- Containment approvals at 03:00: phone notification, chat message, or CLI only at first?
- How much of the investigation can run on a local model, and where is a hosted model needed?

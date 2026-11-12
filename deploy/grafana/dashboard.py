"""Generates drill.json, the Drill Down Harness dashboard (python3 deploy/grafana/dashboard.py)."""
import json, os

P = {"type": "prometheus", "uid": "prometheus"}
T = {"type": "tempo", "uid": "tempo"}
panels, y = [], 0
G, A, R = "green", "orange", "red"


def stat(title, expr, x, unit, steps, desc, w=4):
    return {"type": "stat", "title": title, "description": desc, "datasource": P,
            "gridPos": {"x": x, "y": y, "w": w, "h": 5},
            "fieldConfig": {"defaults": {"unit": unit, "decimals": 1, "thresholds": {"mode": "absolute", "steps": steps}}},
            "options": {"colorMode": "background", "graphMode": "area", "reduceOptions": {"calcs": ["lastNotNull"]}},
            "targets": [{"datasource": P, "expr": expr, "refId": "A"}]}


def ts(title, exprs, x, desc, unit="short", w=12, kind="timeseries", stack=False):
    return {"type": kind, "title": title, "description": desc, "datasource": P,
            "gridPos": {"x": x, "y": y, "w": w, "h": 8},
            "fieldConfig": {"defaults": {"unit": unit, "custom": {"lineWidth": 2, "fillOpacity": 15,
                                                                  "stacking": {"mode": "normal" if stack else "none"}}}},
            "options": {"legend": {"displayMode": "list", "placement": "bottom"}, "tooltip": {"mode": "multi"}},
            "targets": [{"datasource": P, "expr": e, "legendFormat": l, "refId": chr(65 + i)} for i, (e, l) in enumerate(exprs)]}


W = "[$__range]"
panels.append({"type": "text", "title": "", "gridPos": {"x": 0, "y": y, "w": 24, "h": 3},
               "options": {"mode": "markdown", "content":
                           "### How the investigator is doing\n"
                           "Each **run** is one investigation. Green numbers are good. "
                           "**Sent back** counts reports the harness rejected as too early. "
                           "Click a run in *Recent runs* (bottom) to see its full timeline."}})
y += 3
panels += [
    stat("Runs", f"sum(increase(drill_runs_total{W}))", 0, "none", [{"color": "blue", "value": None}],
         "Investigations finished in the selected time range."),
    stat("Finished normally", f'sum(increase(drill_runs_total{{stop="done"}}{W})) / sum(increase(drill_runs_total{W}))', 4, "percentunit",
         [{"color": R, "value": None}, {"color": A, "value": .7}, {"color": G, "value": .9}],
         "Share of runs that ended with a report, not a limit (time, turns, tokens) or an error."),
    stat("Typical run time", f"histogram_quantile(0.5, sum by (le) (increase(drill_run_duration_seconds_bucket{W})))", 8, "s",
         [{"color": G, "value": None}, {"color": A, "value": 180}, {"color": R, "value": 600}], "Median minutes from page to report."),
    stat("Checks per run", f"sum(increase(drill_tool_calls_total{W})) / sum(increase(drill_runs_total{W}))", 12, "none",
         [{"color": "blue", "value": None}], "Average number of tool calls (queries) per investigation."),
    stat("Sent back", f"sum(increase(drill_gate_sendbacks_total{W})) or vector(0)", 16, "none",
         [{"color": G, "value": None}, {"color": A, "value": 1}], "Reports the harness refused because the evidence was not there yet."),
    stat("Tokens per run", f"sum(increase(drill_tokens_total{W})) / sum(increase(drill_runs_total{W}))", 20, "short",
         [{"color": "blue", "value": None}], "Model input + output tokens per investigation (cost driver for hosted models)."),
]
y += 5
panels += [
    ts("How runs ended", [("sum by (stop) (increase(drill_runs_total[5m]))", "{{stop}}")], 0,
       "done = wrote a report. Anything else means it hit a limit or failed.", kind="timeseries", stack=True),
    ts("Run time (p50 / p95)", [("histogram_quantile(0.5, sum by (le) (rate(drill_run_duration_seconds_bucket[10m])))", "p50"),
                               ("histogram_quantile(0.95, sum by (le) (rate(drill_run_duration_seconds_bucket[10m])))", "p95")], 12,
       "How long an investigation takes, page to report.", unit="s"),
]
y += 8
panels += [
    ts("Which tools it uses", [("sum by (tool) (increase(drill_tool_calls_total[5m]))", "{{tool}}")], 0,
       "Tool calls per 5 minutes, by tool.", stack=True),
    ts("Tool errors", [("sum by (tool) (increase(drill_tool_calls_total{outcome=\"error\"}[5m]))", "{{tool}}")], 12,
       "Failed tool calls (bad queries, timeouts). The model sees the error and can retry."),
]
y += 8
panels += [
    ts("Slowest parts (p95)", [("histogram_quantile(0.95, sum by (le) (rate(drill_model_duration_seconds_bucket[10m])))", "model thinking"),
                              ("histogram_quantile(0.95, sum by (le, tool) (rate(drill_tool_duration_seconds_bucket[10m])))", "tool: {{tool}}")], 0,
       "Where time goes: waiting for the model, or for tools.", unit="s"),
    ts("Tokens", [("sum by (kind) (increase(drill_tokens_total[5m]))", "{{kind}}")], 12, "Model tokens per 5 minutes.", stack=True),
]
y += 8
panels.append({"type": "table", "title": "Recent runs (click one for its timeline)", "datasource": T,
               "gridPos": {"x": 0, "y": y, "w": 24, "h": 9},
               "targets": [{"datasource": T, "queryType": "traceql", "query": '{resource.service.name="drill-down-harness" && name="drill.run"}',
                            "limit": 30, "refId": "A"}]})

out = os.path.join(os.path.dirname(__file__), "drill.json")
json.dump({"uid": "drill", "title": "Drill Down Harness", "tags": ["drill"], "refresh": "10s",
           "time": {"from": "now-6h", "to": "now"}, "schemaVersion": 39, "panels": panels}, open(out, "w"), indent=1)
print("wrote", out)

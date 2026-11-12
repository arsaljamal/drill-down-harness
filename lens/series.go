// Package lens rewrites tool results so a model can read them reliably.
//
// Series turns Prometheus-style time series ({"metric":{...},"values":[[ts,"v"],...]})
// into one plain line per series: where it started, where it is now, and when
// it changed most. Small models misread long lists of numbers; they do not
// misread "was 0.02, now 2.4, jumped 3 min ago".
package lens

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type series struct {
	labels string
	ts     []float64
	vals   []float64
}

// Series returns the rewritten text and true if text held at least one
// multi-point series; otherwise it returns text unchanged and false.
func Series(text string, now time.Time) (string, bool) {
	return SeriesFor("", text, now)
}

// SeriesFor is Series with the query that produced the result named in the
// summary, so the model cannot mix up which series came from which query.
func SeriesFor(query, text string, now time.Time) (string, bool) {
	var root any
	if json.Unmarshal([]byte(text), &root) != nil {
		return text, false
	}
	var found []series
	walk(root, &found)
	if len(found) == 0 {
		return text, false
	}
	var b strings.Builder
	if query != "" {
		fmt.Fprintf(&b, "Result of %s\n", query)
	}
	fmt.Fprintf(&b, "%d time series (summarised by the harness; times are relative to now):\n", len(found))
	for _, s := range found {
		b.WriteString("- " + describe(s, now) + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), true
}

func walk(v any, out *[]series) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			walk(e, out)
		}
	case map[string]any:
		if vals, ok := x["values"].([]any); ok && len(vals) > 1 {
			if s, ok := parse(x["metric"], vals); ok {
				*out = append(*out, s)
				return
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walk(x[k], out)
		}
	}
}

func parse(metric any, vals []any) (series, bool) {
	s := series{labels: labels(metric)}
	for _, p := range vals {
		pair, ok := p.([]any)
		if !ok || len(pair) != 2 {
			return s, false
		}
		ts, ok1 := pair[0].(float64)
		str, ok2 := pair[1].(string)
		if !ok1 || !ok2 {
			return s, false
		}
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return s, false
		}
		s.ts, s.vals = append(s.ts, ts), append(s.vals, v)
	}
	return s, true
}

func labels(m any) string {
	mm, _ := m.(map[string]any)
	var parts []string
	for k, v := range mm {
		if k == "__name__" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "(all)"
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func ago(ts float64, now time.Time) string {
	m := now.Sub(time.Unix(int64(ts), 0)).Minutes()
	if m < 0.5 {
		return "just now"
	}
	return strconv.FormatFloat(math.Round(m*2)/2, 'f', -1, 64) + " min ago"
}

// describe reports the latest change of level: the series is at its current
// level since some point; before that it was elsewhere. Older spikes that
// have already passed are not the story.
func describe(s series, now time.Time) string {
	var real []int
	for i, v := range s.vals {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			real = append(real, i)
		}
	}
	if len(real) == 0 {
		return s.labels + ": no data in this window"
	}
	lo, hi := s.vals[real[0]], s.vals[real[0]]
	for _, i := range real {
		lo, hi = math.Min(lo, s.vals[i]), math.Max(hi, s.vals[i])
	}
	last := real[len(real)-1]
	cur := s.vals[last]
	out := fmt.Sprintf("%s: %s now (window min %s, max %s)", s.labels, num(cur), num(lo), num(hi))
	if hi-lo <= 0.3*math.Max(math.Abs(hi), math.Abs(lo)) {
		return out + "; steady"
	}
	// Walk back while values stay near the current level.
	tol := 0.25 * (hi - lo)
	k := len(real) - 1
	for k > 0 && math.Abs(s.vals[real[k-1]]-cur) <= tol {
		k--
	}
	if k == 0 {
		return out + "; at this level for the whole window"
	}
	before := s.vals[real[k-1]]
	dir := "rose"
	if cur < before {
		dir = "fell"
	}
	return out + fmt.Sprintf("; %s from %s to this level %s", dir, num(before), ago(s.ts[real[k]], now))
}

func num(v float64) string {
	switch a := math.Abs(v); {
	case a == 0:
		return "0"
	case a >= 100:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case a >= 1:
		return strconv.FormatFloat(v, 'f', 2, 64)
	default:
		return strconv.FormatFloat(v, 'g', 2, 64)
	}
}

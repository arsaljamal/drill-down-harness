package lens

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Logs turns Loki-style results ({"timestamp":"<unix ns>","line":"..."}) into
// one short line per entry with its age ("2 min ago: config reloaded ..."),
// newest first, without the metadata noise. Small models cannot do arithmetic
// on nanosecond timestamps; they can match "2 min ago" to "rose 2 min ago".
func Logs(text string, now time.Time) (string, bool) {
	var root any
	if json.Unmarshal([]byte(text), &root) != nil {
		return text, false
	}
	type entry struct {
		at   time.Time
		line string
	}
	var found []entry
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			line, ok := x["line"].(string)
			if ok {
				if at, ok := stamp(x["timestamp"]); ok {
					found = append(found, entry{at, line})
					return
				}
			}
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(root)
	if len(found) == 0 {
		return text, false
	}
	sort.Slice(found, func(i, j int) bool { return found[i].at.After(found[j].at) })
	const max = 30
	var b strings.Builder
	fmt.Fprintf(&b, "%d log line(s), newest first (ages computed by the harness):\n", len(found))
	for i, e := range found {
		if i == max {
			fmt.Fprintf(&b, "- ... %d older line(s) not shown\n", len(found)-max)
			break
		}
		fmt.Fprintf(&b, "- %s: %s\n", agoTime(e.at, now), oneLine(e.line, 200))
	}
	return strings.TrimRight(b.String(), "\n"), true
}

func stamp(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	s = strings.Trim(s, `"`)
	ns, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t, true
		}
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}

func agoTime(t, now time.Time) string {
	return ago(float64(t.Unix()), now)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

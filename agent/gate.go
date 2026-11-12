package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Gate decides whether a final answer may end the run. If it returns a
// nudge, the nudge goes back to the model as a user message and the run
// continues. It sees the whole history and the proposed final text.
type Gate func(history []Message, final string) (nudge string)

// Requirement is evidence the run must have gathered before reporting: at
// least one tool call whose name and input match Pattern.
type Requirement struct {
	Name    string
	Pattern *regexp.Regexp
}

// ReportGate is the standard gate for investigations: a reply must not be
// empty, must rest on enough tool calls, must include the required checks
// and must use the required headings.
type ReportGate struct {
	MinToolCalls int
	Require      []Requirement
	Headings     []string
}

func (g ReportGate) Check(history []Message, final string) string {
	if strings.TrimSpace(final) == "" {
		return "Your reply was empty. Continue the investigation with the tools, then write the report."
	}
	var calls []string
	for _, m := range history {
		for _, b := range m.Blocks {
			if b.Kind == BlockToolUse {
				calls = append(calls, b.ToolName+" "+string(b.Input))
			}
		}
	}
	var missing []string
	if len(calls) < g.MinToolCalls {
		missing = append(missing, fmt.Sprintf("you ran %d queries; run at least %d before concluding", len(calls), g.MinToolCalls))
	}
	for _, r := range g.Require {
		found := false
		for _, c := range calls {
			if r.Pattern.MatchString(c) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, "you have not yet: "+r.Name)
		}
	}
	for _, h := range g.Headings {
		if !strings.Contains(strings.ToLower(final), strings.ToLower(h)) {
			missing = append(missing, "the report is missing the heading "+h)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "Not ready to report yet:\n- " + strings.Join(missing, "\n- ") +
		"\nDo these with the tools now. Do not guess; only conclude from query results."
}

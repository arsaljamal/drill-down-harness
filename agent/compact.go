package agent

import "fmt"

// TrimOldToolResults keeps the newest keep tool results whole and cuts older
// ones to max characters, so a long investigation fits in the model's
// context window. It returns a new slice and never changes msgs.
func TrimOldToolResults(keep, max int) func([]Message) []Message {
	return func(msgs []Message) []Message {
		seen := 0
		out := make([]Message, len(msgs))
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			blocks := make([]Block, len(m.Blocks))
			copy(blocks, m.Blocks)
			for j := len(blocks) - 1; j >= 0; j-- {
				if blocks[j].Kind != BlockToolResult {
					continue
				}
				seen++
				if seen > keep && len(blocks[j].Text) > max {
					cut := len(blocks[j].Text) - max
					blocks[j].Text = blocks[j].Text[:max] + fmt.Sprintf("\n[older output shortened: %d characters removed]", cut)
				}
			}
			out[i] = Message{Role: m.Role, Blocks: blocks}
		}
		return out
	}
}

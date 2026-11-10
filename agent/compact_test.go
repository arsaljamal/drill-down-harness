package agent

import (
	"strings"
	"testing"
)

func TestTrimOldToolResults(t *testing.T) {
	long := strings.Repeat("x", 100)
	var msgs []Message
	for i := 0; i < 4; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Blocks: []Block{{Kind: BlockToolResult, Text: long}}})
	}
	got := TrimOldToolResults(2, 10)(msgs)
	for i, m := range got {
		trimmed := strings.Contains(m.Blocks[0].Text, "shortened")
		if want := i < 2; trimmed != want {
			t.Errorf("message %d: trimmed=%v want %v", i, trimmed, want)
		}
	}
	if msgs[0].Blocks[0].Text != long {
		t.Fatal("input was modified")
	}
}

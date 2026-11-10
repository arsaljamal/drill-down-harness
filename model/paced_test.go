package model

import (
	"context"
	"testing"
	"time"

	"github.com/arsaljamal/drill-down-harness/agent"
)

type countModel struct{ at []time.Time }

func (c *countModel) Generate(context.Context, agent.ModelRequest) (agent.ModelReply, error) {
	c.at = append(c.at, time.Now())
	return agent.ModelReply{}, nil
}

func TestPacedSpacesCalls(t *testing.T) {
	inner := &countModel{}
	p := &Paced{Model: inner, Every: 40 * time.Millisecond}
	for i := 0; i < 3; i++ {
		p.Generate(context.Background(), agent.ModelRequest{})
	}
	for i := 1; i < len(inner.at); i++ {
		if gap := inner.at[i].Sub(inner.at[i-1]); gap < 35*time.Millisecond {
			t.Fatalf("calls %d and %d only %v apart", i-1, i, gap)
		}
	}
}

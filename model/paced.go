package model

import (
	"context"
	"sync"
	"time"

	"github.com/arsaljamal/drill-down-harness/agent"
)

// Paced spaces out calls to a model so they stay under a requests-per-minute
// limit, instead of hitting the limit and waiting for a 429 to clear.
type Paced struct {
	Model agent.Model
	Every time.Duration // minimum time between the starts of two calls

	mu   sync.Mutex
	last time.Time
}

// Generate implements agent.Model.
func (p *Paced) Generate(ctx context.Context, req agent.ModelRequest) (agent.ModelReply, error) {
	p.mu.Lock()
	wait := time.Until(p.last.Add(p.Every))
	if wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			p.mu.Unlock()
			return agent.ModelReply{}, ctx.Err()
		case <-t.C:
		}
	}
	p.last = time.Now()
	p.mu.Unlock()
	return p.Model.Generate(ctx, req)
}

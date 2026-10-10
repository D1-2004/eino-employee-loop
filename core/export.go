package core

import (
	"context"
	"time"
)

// Outbox returns a copy of delivered user-facing events (ack / reply / dispatch_reply / result_reply / ask / dedup_notice).
func (r *Runtime) Outbox() []OutEv {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]OutEv, len(r.out))
	copy(out, r.out)
	return out
}

// Turns returns a copy of completed decision turns.
func (r *Runtime) Turns() []*Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Turn, len(r.turns))
	copy(out, r.turns)
	return out
}

// Counters returns Host-side counters (dispatch_deduplicated, tasks_created, protocol_repairs, ...).
func (r *Runtime) Counters() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for k, v := range r.counters {
		out[k] = v
	}
	return out
}

// ListTasks returns the Host task registry for this scene.
func (r *Runtime) ListTasks(ctx context.Context) ([]*Task, error) {
	return r.ts.Find(ctx, r.cfg.Tenant, r.cfg.Scene, "", 200)
}

// ListMemory returns active memory facts for a subject+audience.
func (r *Runtime) ListMemory(ctx context.Context, subject, audience string) ([]*Fact, error) {
	return r.ms.Recent(ctx, r.cfg.Tenant, r.cfg.Scene, subject, audience, 100)
}

// StoreStats returns per-op storage latency summary.
func (r *Runtime) StoreStats() map[string]map[string]float64 { return r.stats.Summary() }

// DefaultConfig builds a treat-mode Config (registry+memory+ack) for the demo.
func DefaultConfig(tenant, scene, key, baseURL string, caseData CaseData) Config {
	return Config{
		Tenant: tenant, Scene: scene, Case: caseData,
		JobDelay: 4 * time.Second, Key: key, BaseURL: baseURL,
		Registry: true, Ack: true, DedupWindow: 90 * time.Second,
		ConvTokens: 1200, MemTokens: 500, RegTokens: 450,
	}
}

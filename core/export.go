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
func DefaultConfig(tenant, scene string, models ModelProvider, caseData CaseData) Config {
	return Config{
		Tenant: tenant, Scene: scene, Case: caseData,
		JobDelay: 4 * time.Second, Models: models,
		Registry: true, Ack: true, DedupWindow: 90 * time.Second,
		ConvTokens: 2200, MemTokens: 700, RegTokens: 450,
	}
}

// SeedFromHistory appends prior channel messages into the local conversation log when the
// log is empty or thin, so the first decision turn already sees nearby speakers/order.
// Skips messages already present by SourceRef (msg id). Does not trigger the loop.
func (r *Runtime) SeedFromHistory(ctx context.Context, items []Inbound, selfNames, selfIDs []string) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	existing, _ := r.conv.Recent(ctx, r.cfg.Tenant, r.cfg.Scene, 0, 200)
	have := map[string]bool{}
	for _, m := range existing {
		if m.SourceRef != "" {
			have[m.SourceRef] = true
		}
	}
	selfN := map[string]bool{}
	for _, n := range selfNames {
		if n != "" {
			selfN[n] = true
		}
	}
	_ = selfIDs // reserved: match by openDingTalkId when history carries it
	n := 0
	for _, in := range items {
		if in.MsgID != "" && have[in.MsgID] {
			continue
		}
		role := "user"
		speaker := in.Speaker
		kind := "history"
		if selfN[in.Speaker] {
			role = "assistant"
			speaker = "employee"
			kind = "history_self"
		}
		_, err := r.conv.Append(ctx, r.cfg.Tenant, r.cfg.Scene, Msg{
			Role: role, Speaker: speaker, Text: in.Text, Kind: kind,
			SourceRef: in.MsgID, AtMs: in.AtMs,
		})
		if err != nil {
			return n, err
		}
		if in.MsgID != "" {
			have[in.MsgID] = true
		}
		n++
	}
	return n, nil
}

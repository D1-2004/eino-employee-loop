package core

import "context"

// Process event kinds for task / wake lifecycle. Host surfaces these to the
// user (or UI) — the model must not spam progress text on its own.
const (
	EvAccepted = "accepted" // Host accepted work (dispatch receipt)
	EvProgress = "progress" // meaningful mid-flight update
	EvBlocked  = "blocked"  // waiting on human / external
	EvDone     = "done"     // finished with result
	EvFailed   = "failed"   // failed with reason
	EvQuiet    = "quiet"    // wake ended with no user-facing message
	EvBudget   = "budget_fallback" // Host auto-acted after per-turn model-round budget
)

// ProcessEvent is one Host-owned lifecycle signal.
type ProcessEvent struct {
	Kind      string // Ev*
	FieldID   string
	TaskID    string
	Receipt   string
	Text      string // optional user-facing summary; empty = Host decides silence
	AtMs      int64
	Meta      map[string]string
}

// EventSink receives process events. Core emits; adapters (IM, log, webhook) implement.
// A nil sink is allowed — Core still records registry state.
type EventSink interface {
	Emit(ctx context.Context, ev ProcessEvent) error
}

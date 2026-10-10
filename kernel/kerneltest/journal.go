package kerneltest

import (
	"context"
	"fmt"
	"sync"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// Journal is an in-memory contract.Journal keyed by the lease's window id.
// It applies memstore's ordering and uniqueness rules but does not fence
// leases, so tests can bind hosts to synthetic windows and re-claim them
// with any lease.
type Journal struct {
	mu      sync.Mutex
	windows map[string]*journalWindow
}

type journalWindow struct {
	turns []contract.ModelTurn
	tools []contract.ToolRecord
}

// NewJournal returns an empty journal.
func NewJournal() *Journal { return &Journal{windows: map[string]*journalWindow{}} }

var _ contract.Journal = (*Journal)(nil)

func (j *Journal) window(id string) *journalWindow {
	w := j.windows[id]
	if w == nil {
		w = &journalWindow{}
		j.windows[id] = w
	}
	return w
}

// ModelTurns implements contract.Journal.
func (j *Journal) ModelTurns(_ context.Context, lease contract.Lease) ([]contract.ModelTurn, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]contract.ModelTurn(nil), j.window(lease.WindowID).turns...), nil
}

// SaveModelTurn implements contract.Journal.
func (j *Journal) SaveModelTurn(_ context.Context, lease contract.Lease, turn contract.ModelTurn) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	w := j.window(lease.WindowID)
	if turn.Seq != len(w.turns) {
		return fmt.Errorf("%w: model turn %d out of order", contract.ErrConflict, turn.Seq)
	}
	w.turns = append(w.turns, turn)
	return nil
}

// Tools implements contract.Journal.
func (j *Journal) Tools(_ context.Context, lease contract.Lease) ([]contract.ToolRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]contract.ToolRecord(nil), j.window(lease.WindowID).tools...), nil
}

// SaveTool implements contract.Journal.
func (j *Journal) SaveTool(_ context.Context, lease contract.Lease, rec contract.ToolRecord) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	w := j.window(lease.WindowID)
	for _, cur := range w.tools {
		if cur.Turn == rec.Turn && cur.NativeID == rec.NativeID {
			return fmt.Errorf("%w: tool %d/%s already recorded", contract.ErrConflict, rec.Turn, rec.NativeID)
		}
	}
	w.tools = append(w.tools, rec)
	return nil
}

// Turns returns the recorded model turns of a window.
func (j *Journal) Turns(windowID string) []contract.ModelTurn {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]contract.ModelTurn(nil), j.window(windowID).turns...)
}

// Records returns the recorded tool results of a window in save order.
func (j *Journal) Records(windowID string) []contract.ToolRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]contract.ToolRecord(nil), j.window(windowID).tools...)
}

// Seed records tool results directly, for replay-mismatch tests.
func (j *Journal) Seed(windowID string, recs ...contract.ToolRecord) {
	j.mu.Lock()
	defer j.mu.Unlock()
	w := j.window(windowID)
	w.tools = append(w.tools, recs...)
}

// Package workflow composes versioned Providers and durable multi-turn sessions
// with Eino. It owns ordering, not business authorization or side effects.
package workflow

import (
	"context"
	"encoding/json"
	"errors"

	loop "github.com/D1-2004/eino-employee-loop/employee/kernel/decision"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var (
	ErrDenied   = errors.New("workflow: admission denied")
	ErrBusy     = errors.New("workflow: session is busy")
	ErrConflict = errors.New("workflow: frozen input conflict")
	ErrVersion  = errors.New("workflow: unknown provider version")
)

// Key is supplied by authenticated admission, never inferred from messages.
type Key struct {
	Profile         string `json:"profile,omitempty"`
	ProfileRevision string `json:"profile_revision,omitempty"`
	ProfileBinding  string `json:"profile_binding,omitempty"`
	Workspace       string
	Tenant          string
	Employee        string
	Scene           string
	Lane            string
	Subject         string
}

// Request is one immutable input receipt within a scoped session.
type Request struct {
	Key      Key
	Receipt  string
	Kind     string
	Version  string
	Messages []*schema.Message
	// Data holds the Host's bounded, frozen business evidence.
	Data json.RawMessage `json:",omitempty"`
}

// Prepared has a per-wake model and Host, including their durable journals.
// Messages includes system policy, context, selected history and current input.
type Prepared struct {
	Config   loop.Config
	Model    model.ToolCallingChatModel
	Host     loop.Host
	Messages []*schema.Message
}

// Provider supplies all business-specific behavior. Commit must be idempotent
// under Key+Receipt and recheck current permissions and fencing before writing.
type Provider interface {
	Authorize(context.Context, Request) error
	Prepare(context.Context, Request, Snapshot) (Prepared, error)
	Commit(context.Context, Request, loop.Outcome) (string, error)
}

// Gate allows an exact trusted scope and version before any private state loads.
type Gate func(context.Context, Request) error

type Phase string

const (
	Generated Phase = "generated"
	Completed Phase = "completed"
	Failed    Phase = "failed"
)

// Record retains failed outcomes too: an error does not prove no effects ran.
type Record struct {
	Request Request
	Phase   Phase
	Outcome loop.Outcome
	Failure string
	// FailureKinds preserves standard error classifications across restart.
	FailureKinds []string `json:",omitempty"`
	Commit       string
}

// Snapshot is pinned to a Provider revision. Providers receive a detached copy.
type Snapshot struct {
	Key      Key
	Kind     string
	Version  string
	Revision uint64
	Records  []Record
}

// History returns successful typed turns, excluding system policy. Whole wakes
// are retained, so assistant calls and tool results are never split by a limit.
func (s Snapshot) History() []*schema.Message {
	var out []*schema.Message
	for _, r := range s.Records {
		if r.Phase != Completed {
			continue
		}
		// Prepared context/history may repeat earlier turns. Keep only the
		// receipt's own input and the transcript suffix generated in this wake.
		out = append(out, r.Request.Messages...)
		for _, m := range r.Outcome.Transcript {
			if m.Role != schema.System {
				out = append(out, m)
			}
		}
	}
	return out
}

// Store leases a complete session, including generation and result commit.
type Store interface {
	Acquire(context.Context, Key) (Lease, error)
}

// Lease implementations must fence stale writers; Save atomically advances the
// expected revision by one. A cloud Host must also fence its business effects.
type Lease interface {
	Load(context.Context) (Snapshot, error)
	Save(context.Context, Snapshot) error
	Release() error
}

// Result separates generated output from the durable business commit receipt.
type Result struct {
	// Outcome.Transcript contains this wake's generated suffix only. Prepared
	// context and original requests remain in their own journals/records.
	Outcome loop.Outcome
	Commit  string
	Replay  bool
	Phase   Phase
}

package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

// Context provides source-fenced projections, without exposing the builder's storage.
type Context interface {
	Build(context.Context, contract.Scope, contextbuilder.View, string, string) (contextbuilder.Frame, error)
	Source(context.Context, contract.Scope, string) (contextbuilder.Source, contextbuilder.Ref, error)
	Revalidate(context.Context, contextbuilder.Frame) error
}

// Options binds M6 to business facts and Host ports. QuestionCheck validates only
// front-office questions without a Task; Task questions are checked against Waits.
type Options struct {
	State             storage.State
	Profile           contract.Profile
	Tasks             *work.Manager
	Context           Context
	Artifacts         storage.Artifacts
	Outbox            *Manager
	Runner            kernel.Agent
	Authorize         func(context.Context, contract.Scope) error
	AuthorizeDelivery func(context.Context, Intent) error
	QuestionCheck     func(context.Context, Intent) error
}

// Service owns message composition, verified result projection and Outbox work.
// It records transport facts on the existing Task and never owns a second Goal.
type Service struct {
	opts Options
	mu   sync.Mutex
}

func NewService(opts Options) (*Service, error) {
	if opts.State == nil || opts.Tasks == nil || opts.Context == nil || opts.Artifacts == nil || opts.Outbox == nil || opts.Runner == nil || opts.Authorize == nil {
		return nil, errors.New("interaction: complete service binding required")
	}
	if err := opts.Profile.Validate(); err != nil {
		return nil, err
	}
	s := &Service{opts: opts}
	opts.Outbox.ValidateSend = s.ValidateDelivery
	return s, nil
}

func encode(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func sourceForTask(task work.Task) string {
	if len(task.Sources) > 0 {
		return task.Sources[0].Receipt
	}
	return fmt.Sprintf("task/%s", task.ID)
}

// Failures reports only M6 delivery failures. Other modules report their own
// recovery handles; M6 does not inspect another module's job namespace.
func (s *Service) Failures(ctx context.Context, scope contract.Scope) ([]contract.Failure, error) {
	if err := s.opts.Authorize(ctx, scope); err != nil {
		return nil, err
	}
	records, err := s.opts.Outbox.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := []contract.Failure{}
	for _, record := range records {
		if record.State == "failed" {
			out = append(out, contract.Failure{Kind: "delivery", ID: record.Intent.ID, State: record.State, Error: record.LastError})
		}
	}
	return out, nil
}

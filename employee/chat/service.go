// Package chat owns M1's durable inbox, foreground decisions and human input.
// Entry supplies lifecycle and ports; it does not interpret Chat's storage.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	native "github.com/D1-2004/eino-employee-loop/employee/kernel/eino"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

// Context exposes only the source and frozen-frame operations required by M1.
type Context interface {
	Admit(context.Context, contract.Scope, contextbuilder.Source) (contextbuilder.Source, error)
	Source(context.Context, contract.Scope, string) (contextbuilder.Source, contextbuilder.Ref, error)
	Build(context.Context, contract.Scope, contextbuilder.View, string, string) (contextbuilder.Frame, error)
	Revalidate(context.Context, contextbuilder.Frame) error
}
type Scheduler interface {
	Schedule(context.Context, contract.Scope, work.Schedule) (work.Schedule, error)
	Observe(context.Context, contract.Scope, work.Attention) (work.Attention, error)
	Attention(context.Context, contract.Scope) ([]work.Attention, error)
	ResolveAttention(context.Context, contract.Scope, string, string, work.Source) error
}
type Options struct {
	State           storage.State
	Profile         contract.Profile
	Context         Context
	Tasks           *work.Manager
	Scheduler       Scheduler
	Outbox          *interaction.Manager
	Runner          kernel.Agent
	Authorize       func(context.Context, contract.Scope) error
	EnqueueMessage  func(context.Context, contract.Profile, contract.Input, string, string, string, string) (any, error)
	CancelExecution func(string)
}
type Service struct {
	db              storage.State
	profile         contract.Profile
	context         Context
	work            *work.Manager
	scheduler       Scheduler
	delivery        *interaction.Manager
	runner          kernel.Agent
	authorize       func(context.Context, contract.Scope) error
	enqueueMessage  func(context.Context, contract.Profile, contract.Input, string, string, string, string) (any, error)
	cancelExecution func(string)
	sceneLocks      map[string]*sync.Mutex
}

// Job preserves the persisted inbox format, including frozen profile and frame.
// The historical namespace remains entry-jobs for existing local directories.
type Job struct {
	Sequence     int64 `json:",omitempty"`
	Input        contract.Input
	Profile      contract.Profile
	State, Error string
	Frame        *contextbuilder.Frame
	Interrupts   []native.Interrupt
	Targets      map[string]any
	Answers      []work.Source
}
type Admitted struct {
	Input   contract.Input
	Profile contract.Profile
}

func New(opts Options) (*Service, error) {
	if opts.State == nil || opts.Context == nil || opts.Tasks == nil || opts.Scheduler == nil || opts.Outbox == nil || opts.Runner == nil || opts.Authorize == nil || opts.EnqueueMessage == nil || opts.CancelExecution == nil {
		return nil, errors.New("chat: complete module dependencies required")
	}
	// Clone configuration at the module boundary; caller mutation is not a profile update.
	var profile contract.Profile
	if err := json.Unmarshal(encode(opts.Profile), &profile); err != nil {
		return nil, err
	}
	locks := make(map[string]*sync.Mutex, len(profile.Scenes))
	for _, scene := range profile.Scenes {
		locks[scene] = &sync.Mutex{}
	}
	return &Service{sceneLocks: locks, db: opts.State, profile: profile, context: opts.Context, work: opts.Tasks, scheduler: opts.Scheduler, delivery: opts.Outbox, runner: opts.Runner, authorize: opts.Authorize, enqueueMessage: opts.EnqueueMessage, cancelExecution: opts.CancelExecution}, nil
}
func encode(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func settlement(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
}

func (s *Service) Submit(ctx context.Context, in contract.Input) (contract.Admission, error) {
	scope := s.profile.Scope(in.Scene)
	if err := s.authorize(ctx, scope); err != nil {
		return contract.Admission{}, err
	}
	if in.Actor != scope.Requester || in.ID == "" || in.Text == "" || in.Origin == "" {
		return contract.Admission{}, work.ErrScope
	}
	if len(in.Native) > 0 && !json.Valid(in.Native) {
		return contract.Admission{}, errors.New("entry: invalid native envelope")
	}
	if _, err := s.context.Admit(ctx, scope, contextbuilder.Source{ID: in.ID, Actor: in.Actor, Text: in.Text}); err != nil {
		return contract.Admission{}, err
	}
	id := contextbuilder.Digest([]byte(in.Scene + "/" + in.Actor + "/" + in.ID))
	admission := contract.Admission{ID: id, State: "queued", Backend: s.profile.Backend, ProfileRevision: s.profile.Revision}
	err := s.db.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "entry-jobs", s.profile.Key(), id)
		if e == nil {
			var old Job
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			if string(encode(old.Input)) != string(encode(in)) {
				return storage.ErrConflict
			}
			admission.State = old.State
			admission.ProfileRevision = old.Profile.Revision
			admission.Replay = true
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		sequence, e := tx.Append(ctx, storage.Event{Namespace: "entry-admissions", Scope: s.profile.Key(), ID: id, Kind: "admitted", Body: encode(map[string]string{"scene": in.Scene})})
		if e != nil {
			return e
		}
		_, e = tx.Put(ctx, storage.Document{Namespace: "entry-jobs", Scope: s.profile.Key(), ID: id, Body: encode(Job{Input: in, Profile: s.profile, State: "queued", Sequence: sequence})})
		return e
	})
	return admission, err
}

// Admissions reconciles source-driven workers without exposing M1's namespace.
func (s *Service) Admissions(ctx context.Context) ([]Admitted, error) {
	docs, err := s.db.List(ctx, "entry-jobs", s.profile.Key())
	if err != nil {
		return nil, err
	}
	out := make([]Admitted, 0, len(docs))
	for _, d := range docs {
		var j Job
		if err = json.Unmarshal(d.Body, &j); err != nil {
			return nil, err
		}
		if err = s.authorize(ctx, j.Profile.Scope(j.Input.Scene)); err != nil {
			return nil, err
		}
		out = append(out, Admitted{Input: j.Input, Profile: j.Profile})
	}
	return out, nil
}

// Retry never rebuilds the frozen frame or resets the physical model budget.
func (s *Service) Retry(ctx context.Context, id string) (bool, error) {
	d, err := s.db.Get(ctx, "entry-jobs", s.profile.Key(), id)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var j Job
	if err = json.Unmarshal(d.Body, &j); err != nil {
		return true, err
	}
	if err = s.authorize(ctx, j.Profile.Scope(j.Input.Scene)); err != nil {
		return true, err
	}
	scene := j.Input.Scene
	lock, err := s.sceneLock(j.Input.Scene)
	if err != nil {
		return true, err
	}
	if !lock.TryLock() {
		return true, storage.ErrBusy
	}
	defer lock.Unlock()
	d, err = s.db.Get(ctx, "entry-jobs", s.profile.Key(), id)
	if err != nil {
		return true, err
	}
	if err = json.Unmarshal(d.Body, &j); err != nil {
		return true, err
	}
	if j.Input.Scene != scene {
		return true, work.ErrScope
	}
	if err = s.authorize(ctx, j.Profile.Scope(j.Input.Scene)); err != nil {
		return true, err
	}
	if j.State != "failed" {
		return true, work.ErrState
	}
	j.State = "queued"
	j.Error = ""
	d.Body = encode(j)
	_, err = s.db.Put(ctx, d)
	return true, err
}
func (s *Service) Failures(ctx context.Context, scope contract.Scope) ([]contract.Failure, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	docs, err := s.db.List(ctx, "entry-jobs", s.profile.Key())
	if err != nil {
		return nil, err
	}
	out := []contract.Failure{}
	for _, d := range docs {
		var j Job
		if err = json.Unmarshal(d.Body, &j); err != nil {
			return nil, err
		}
		if j.Input.Scene == scope.Scene && j.State == "failed" {
			out = append(out, contract.Failure{Kind: "chat", ID: d.ID, State: j.State, Error: j.Error})
		}
	}
	return out, nil
}

// ValidateQuestion checks a source-bound foreground interruption. Task questions
// belong to M2 and are checked by its own delivery contract.
func (s *Service) ValidateQuestion(ctx context.Context, in interaction.Intent) error {
	if err := s.authorize(ctx, in.Scope); err != nil {
		return err
	}
	docs, err := s.db.List(ctx, "entry-jobs", s.profile.Key())
	if err != nil {
		return err
	}
	for _, d := range docs {
		var j Job
		if err = json.Unmarshal(d.Body, &j); err != nil {
			return err
		}
		if j.Input.ID != in.SourceReceipt || j.Input.Scene != in.Scope.Scene {
			continue
		}
		if j.State == "interrupted" {
			return nil
		}
		return interaction.ErrObsolete
	}
	return interaction.ErrObsolete
}

// jobs orders the durable inbox by transactionally allocated admission sequence.
// Legacy jobs predate that counter; their source admission times provide the
// best preserved ordering and they precede admissions made after this upgrade.
func (s *Service) jobs(ctx context.Context) ([]storage.Document, error) {
	docs, err := s.db.List(ctx, "entry-jobs", s.profile.Key())
	if err != nil {
		return nil, err
	}
	type position struct {
		sequence int64
		at       time.Time
	}
	order := make(map[string]position, len(docs))
	for _, d := range docs {
		var j Job
		if err := json.Unmarshal(d.Body, &j); err != nil {
			return nil, err
		}
		pos := position{sequence: j.Sequence}
		if j.Sequence == 0 {
			key, err := j.Profile.Scope(j.Input.Scene).Key()
			if err != nil {
				return nil, err
			}
			source, err := s.db.Get(ctx, "sources", key, j.Input.ID)
			if err == nil {
				var admitted contextbuilder.Source
				if err := json.Unmarshal(source.Body, &admitted); err != nil {
					return nil, err
				}
				pos.at = admitted.At
			} else if !errors.Is(err, storage.ErrNotFound) {
				return nil, err
			}
		}
		order[d.ID] = pos
	}
	sort.SliceStable(docs, func(i, j int) bool {
		a, b := order[docs[i].ID], order[docs[j].ID]
		if a.sequence != b.sequence {
			return a.sequence < b.sequence
		}
		if !a.at.Equal(b.at) {
			return a.at.Before(b.at)
		}
		return docs[i].ID < docs[j].ID
	})
	return docs, nil
}

func (s *Service) sceneLock(scene string) (*sync.Mutex, error) {
	lock := s.sceneLocks[scene]
	if lock == nil {
		return nil, work.ErrScope
	}
	return lock, nil
}

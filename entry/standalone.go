package entry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	native "github.com/D1-2004/eino-employee-loop/agent"
	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/execution"
	"github.com/D1-2004/eino-employee-loop/interaction"
	"github.com/D1-2004/eino-employee-loop/ownerloop"
	"github.com/D1-2004/eino-employee-loop/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/work"
	"github.com/D1-2004/eino-employee-loop/workflow"
)

type standalone struct {
	executionMu                  sync.Mutex
	executionCancels             map[string]context.CancelFunc
	ownerWorker                  *ownerloop.Worker
	scheduler                    *work.Scheduler
	ownerMu, timingMu            sync.Mutex
	executor                     *execution.Local
	opts                         Options
	db                           *sqlite.Store
	files                        *markdown.Store
	work                         *work.Manager
	context                      *contextbuilder.Builder
	delivery                     *interaction.Manager
	lease                        workflow.Lease
	owner                        string
	mu, chatMu, bgMu, deliveryMu sync.Mutex
	started, closed              bool
	cancel                       context.CancelFunc
	wg                           sync.WaitGroup
	errors                       chan error
}
type job struct {
	Input        Input
	Profile      Profile
	State, Error string
	Frame        *contextbuilder.Frame
	Interrupts   []native.Interrupt
	Targets      map[string]any
	Answers      []work.Source
}

func settlement(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
}

func openStandalone(ctx context.Context, opts Options) (result *standalone, err error) {
	if opts.Models == nil {
		return nil, errors.New("entry: model factory required")
	}
	db, err := sqlite.Open(ctx, filepath.Join(opts.Directory, "state"))
	if err != nil {
		return nil, err
	}
	s := &standalone{opts: opts, db: db, errors: make(chan error, 16)}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	key := opts.Profile.Scope("__entry__")
	s.lease, err = db.Acquire(ctx, workflow.Key{Workspace: key.Workspace, Tenant: key.Tenant, Employee: key.Employee, Scene: key.Scene, Lane: "entry", Subject: key.Requester})
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	s.owner = hex.EncodeToString(nonce)
	s.files, err = markdown.Open(filepath.Join(opts.Directory, "workspace"))
	if err != nil {
		return nil, err
	}
	s.work, err = work.New(db)
	if err != nil {
		return nil, err
	}
	s.context, err = contextbuilder.New(db, s.files, s.work, func(c context.Context, scope work.Scope, _ contextbuilder.View) error { return s.authorize(c, scope) })
	if err != nil {
		return nil, err
	}
	port := opts.Delivery
	if port == nil {
		port = interaction.Local{Files: s.files}
	}
	s.delivery = &interaction.Manager{DB: db, Port: port, Owner: s.owner, Authorize: s.authorize, ValidateSend: s.validateDelivery}
	s.executor = &execution.Local{DB: db, Owner: s.owner, Authorize: s.authorize}
	s.scheduler, err = work.NewScheduler(db, s.work, s.authorize, func(c context.Context, scope work.Scope, src work.Source) error {
		current, _, e := s.context.Source(c, scope, src.Receipt)
		if e != nil {
			return e
		}
		if current.Actor != src.Actor || current.Text != src.Text {
			return work.ErrScope
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.ownerWorker, err = s.ownerFor(opts.Profile)
	if err != nil {
		return nil, err
	}
	binding := struct{ Backend, ProfileID string }{opts.Profile.Backend, opts.Profile.ID}
	bindingKey := contextbuilder.OwnerKey(opts.Profile.Scope("binding"))
	err = db.Transaction(ctx, func(tx *sqlite.Tx) error {
		d, e := tx.Get(ctx, "business-bindings", bindingKey, "selected")
		raw := encode(binding)
		if e == nil {
			if string(d.Body) != string(raw) {
				return errors.New("entry: directory is bound to a different business profile/backend; explicit migration required")
			}
			return nil
		}
		if !errors.Is(e, sqlite.ErrNotFound) {
			return e
		}
		_, e = tx.Put(ctx, sqlite.Document{Namespace: "business-bindings", Scope: bindingKey, ID: "selected", Body: raw})
		return e
	})
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(opts.Profile)
	id := opts.Profile.ID + "/" + opts.Profile.Revision
	err = db.Transaction(ctx, func(tx *sqlite.Tx) error {
		d, e := tx.Get(ctx, "profiles", opts.Profile.Key(), id)
		if e == nil {
			if string(d.Body) != string(raw) {
				return errors.New("entry: profile changed under same revision")
			}
			return nil
		}
		if !errors.Is(e, sqlite.ErrNotFound) {
			return e
		}
		_, e = tx.Put(ctx, sqlite.Document{Namespace: "profiles", Scope: opts.Profile.Key(), ID: id, Body: raw})
		return e
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *standalone) authorize(ctx context.Context, scope work.Scope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p := s.opts.Profile
	if scope.Workspace != p.Workspace || scope.Tenant != p.Tenant || scope.Employee != p.Employee || scope.Requester != p.Owner {
		return work.ErrScope
	}
	for _, scene := range p.Scenes {
		if scope.Scene == scene {
			return nil
		}
	}
	return work.ErrScope
}
func (s *standalone) Submit(ctx context.Context, in Input) (Admission, error) {
	scope := s.opts.Profile.Scope(in.Scene)
	if err := s.authorize(ctx, scope); err != nil {
		return Admission{}, err
	}
	if in.Actor != scope.Requester || in.ID == "" || in.Text == "" || in.Origin == "" {
		return Admission{}, work.ErrScope
	}
	if len(in.Native) > 0 && !json.Valid(in.Native) {
		return Admission{}, errors.New("entry: invalid native envelope")
	}
	if _, err := s.context.Admit(ctx, scope, contextbuilder.Source{ID: in.ID, Actor: in.Actor, Text: in.Text}); err != nil {
		return Admission{}, err
	}
	id := contextbuilder.Digest([]byte(in.Scene + "/" + in.Actor + "/" + in.ID))
	admission := Admission{ID: id, State: "queued", Backend: s.opts.Profile.Backend, ProfileRevision: s.opts.Profile.Revision}
	err := s.db.Transaction(ctx, func(tx *sqlite.Tx) error {
		d, e := tx.Get(ctx, "entry-jobs", s.opts.Profile.Key(), id)
		if e == nil {
			var old job
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			a, _ := json.Marshal(old.Input)
			b, _ := json.Marshal(in)
			if string(a) != string(b) {
				return sqlite.ErrConflict
			}
			admission.State = old.State
			admission.ProfileRevision = old.Profile.Revision
			admission.Replay = true
			return nil
		}
		if !errors.Is(e, sqlite.ErrNotFound) {
			return e
		}
		raw, _ := json.Marshal(job{Input: in, Profile: s.opts.Profile, State: "queued"})
		_, e = tx.Put(ctx, sqlite.Document{Namespace: "entry-jobs", Scope: s.opts.Profile.Key(), ID: id, Body: raw})
		return e
	})
	return admission, err
}
func (s *standalone) Tasks(ctx context.Context, scope work.Scope) ([]work.Task, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	return s.work.List(ctx, scope)
}
func (s *standalone) Deliveries(ctx context.Context, scope work.Scope) ([]interaction.Record, error) {
	return s.delivery.List(ctx, scope)
}
func (s *standalone) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.started {
		return errors.New("entry: already started or closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	for _, fn := range []func(context.Context) (bool, error){s.chatOnce, s.backgroundOnce, s.deliveryOnce, s.timingOnce, s.ownerOnce} {
		s.wg.Go(func() {
			for workerCtx.Err() == nil {
				progress, err := fn(workerCtx)
				if err != nil {
					select {
					case s.errors <- err:
					default:
					}
				}
				if !progress || err != nil {
					select {
					case <-workerCtx.Done():
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
		})
	}
	return nil
}
func (s *standalone) RunOnce(ctx context.Context) (bool, error) {
	for _, fn := range []func(context.Context) (bool, error){s.chatOnce, s.backgroundOnce, s.deliveryOnce, s.timingOnce, s.ownerOnce} {
		ok, err := fn(ctx)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}
func (s *standalone) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	// Wait for explicit RunOnce callers as well as owned workers.
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	var err error
	if s.files != nil {
		err = errors.Join(err, s.files.Close())
	}
	if s.lease != nil {
		err = errors.Join(err, s.lease.Release())
	}
	if s.db != nil {
		err = errors.Join(err, s.db.Close())
	}
	return err
}
func encode(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func jobError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("entry worker: %w", err)
}

func (s *standalone) Errors() <-chan error { return s.errors }

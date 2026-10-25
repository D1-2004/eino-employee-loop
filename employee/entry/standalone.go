package entry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/D1-2004/eino-employee-loop/employee/chat"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	native "github.com/D1-2004/eino-employee-loop/employee/kernel/eino"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/workflow"
	host "github.com/D1-2004/eino-employee-loop/employee/ports/services"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	taskmodule "github.com/D1-2004/eino-employee-loop/employee/task"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"io"
	"sync"
	"time"
)

type standalone struct {
	services        host.Services
	ownerWorker     OwnerRuntime
	ownerWorkers    map[string]OwnerRuntime
	moduleClosers   []io.Closer
	scheduler       SchedulerRuntime
	ownerMu         sync.Mutex
	executor        execution.Port
	bridge          *execution.Bridge
	runner          *kernel.Runner
	chat            *chat.Service
	tasksWorker     *taskmodule.Worker
	interaction     *interaction.Service
	opts            Options
	db              storage.State
	files           storage.Artifacts
	work            *work.Manager
	authority       chat.Tasks
	hostTasks       *TaskManagerBinding
	context         ContextRuntime
	delivery        *interaction.Manager
	lease           workflow.Lease
	owner           string
	mu              sync.Mutex
	started, closed bool
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	errors          chan error
}

func openStandalone(ctx context.Context, opts Options) (result *standalone, err error) {
	if opts.Models == nil {
		return nil, errors.New("entry: model factory required")
	}
	if err = validateTaskManagerOptions(opts); err != nil {
		return nil, err
	}
	if err = bindModules(opts); err != nil {
		return nil, err
	}
	factory := opts.DefaultStorage
	if opts.Profile.Modules.Storage.Effective() != builtinModule {
		opts.Modules.mu.RLock()
		factory = opts.Modules.storage[opts.Profile.Modules.Storage]
		opts.Modules.mu.RUnlock()
	}
	if factory == nil {
		return nil, errors.New("entry: storage assembly required; use app/local for the default application")
	}
	db, files, err := factory(ctx, StorageDependencies{Directory: opts.Directory, Profile: profileSnapshot(opts.Profile)})
	if err != nil || nilModule(db) || nilModule(files) {
		if err == nil {
			err = errors.New("entry: storage factory returned unavailable resources")
		}
		if !nilModule(files) {
			err = errors.Join(err, files.Close())
		}
		if !nilModule(db) {
			err = errors.Join(err, db.Close())
		}
		return nil, err
	}
	s := &standalone{opts: opts, db: db, files: files, ownerWorkers: map[string]OwnerRuntime{}, errors: make(chan error, 16)}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
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
	err = s.openTaskManager(ctx)
	if err != nil {
		return nil, err
	}
	if opts.Profile.Modules.Context.Effective() == builtinModule {
		s.context, err = contextbuilder.New(db, files, s.authority, func(c context.Context, scope work.Scope, _ contextbuilder.View) error { return s.authorize(c, scope) }, contextbuilder.WithAudiencePolicy(opts.AudiencePolicy))
	} else {
		opts.Modules.mu.RLock()
		factory := opts.Modules.contexts[opts.Profile.Modules.Context]
		opts.Modules.mu.RUnlock()
		s.context, err = factory(ctx, s.contextDependencies())
	}
	if !nilModule(s.context) {
		s.own(s.context)
	}
	if err != nil {
		return nil, err
	}
	if nilModule(s.context) {
		return nil, errors.New("entry: context factory returned nil")
	}
	s.services, err = s.localServices()
	if err != nil {
		return nil, err
	}
	s.runner = &kernel.Runner{State: db, Owner: s.owner, Models: opts.Models, Authorize: s.authorize, Callbacks: opts.Callbacks, Plugins: func(c context.Context, p contract.Profile, cfg native.Config) (native.Config, error) {
		return opts.Plugins.Resolve(p.Plugins, cfg)
	}, ExtraTools: func(c context.Context, in kernel.Invocation, check func(context.Context) error) ([]*kernel.Function, error) {
		if !in.Profile.HostTools || in.Role != "executor" {
			return nil, nil
		}
		frame, ok := in.Context.(contextbuilder.Frame)
		if !ok {
			return nil, errors.New("entry: invalid Host context frame")
		}
		return s.hostFunctions(c, in.Scope, in.Receipt, frame, check)
	}}
	if s.hostTasks != nil {
		if err = s.bindHostDriven(ctx); err != nil {
			return nil, err
		}
	} else {
		if opts.Profile.Modules.Executor.Effective() == builtinModule {
			if opts.DefaultExecutor == nil {
				return nil, errors.New("entry: execution Provider must be assembled by the application")
			}
			s.executor, err = opts.DefaultExecutor(ctx, s.executorDependencies())
		} else {
			opts.Modules.mu.RLock()
			factory := opts.Modules.executors[opts.Profile.Modules.Executor]
			opts.Modules.mu.RUnlock()
			s.executor, err = factory(ctx, s.executorDependencies())
		}
		if !nilModule(s.executor) {
			s.own(s.executor)
		}
		if err != nil {
			return nil, err
		}
		if nilModule(s.executor) {
			return nil, errors.New("entry: executor factory returned nil")
		}
		if opts.Profile.Modules.Scheduler.Effective() == builtinModule {
			s.scheduler, err = s.defaultScheduler()
		} else {
			opts.Modules.mu.RLock()
			factory := opts.Modules.schedulers[opts.Profile.Modules.Scheduler]
			opts.Modules.mu.RUnlock()
			s.scheduler, err = factory(ctx, s.schedulerDependencies())
		}
		if !nilModule(s.scheduler) {
			s.own(s.scheduler)
		}
		if err != nil {
			return nil, err
		}
		if nilModule(s.scheduler) {
			return nil, errors.New("entry: scheduler factory returned nil")
		}
		s.bridge = &execution.Bridge{State: db, Port: s.executor, Context: s.context, Authorize: s.authorize, Tasks: s.work}
		port := opts.Delivery
		if nilModule(port) {
			if opts.DefaultDelivery == nil {
				return nil, errors.New("entry: delivery Provider must be assembled by the application")
			}
			port, err = opts.DefaultDelivery(ctx, DeliveryDependencies{Artifacts: files})
			if !nilModule(port) {
				s.own(port)
			}
			if err != nil {
				return nil, err
			}
			if nilModule(port) {
				return nil, errors.New("entry: delivery factory returned nil")
			}
		}
		s.delivery = &interaction.Manager{DB: db, Port: port, Owner: s.owner, Authorize: s.authorize, PrepareIntent: opts.ResolveDelivery}
		s.interaction, err = interaction.NewService(interaction.Options{State: db, Profile: opts.Profile, Tasks: s.work, Context: s.context, Artifacts: files, Outbox: s.delivery, Runner: s.runner, Authorize: s.authorize, AuthorizeDelivery: opts.AuthorizeDelivery, QuestionCheck: func(c context.Context, in interaction.Intent) error { return s.chat.ValidateQuestion(c, in) }})
		if err != nil {
			return nil, err
		}
		s.chat, err = chat.New(chat.Options{State: db, Profile: opts.Profile, Context: s.context, Tasks: s.work, Scheduler: s.scheduler, Outbox: s.delivery, Runner: s.runner, Authorize: s.authorize, EnqueueMessage: s.interaction.EnqueueMessage, CancelExecution: func(id string) {
			if running, ok := s.executor.(execution.Interrupter); ok {
				running.Interrupt(id)
			}
		}})
		if err != nil {
			return nil, err
		}
		s.tasksWorker, err = taskmodule.New(taskmodule.Options{State: db, Artifacts: files, Profile: opts.Profile, Tasks: s.work, Context: s.context, Executor: s.executor, Bridge: s.bridge, Runner: s.runner, Authorize: s.authorize, Scheduler: s.scheduler, Now: s.now, ReviewFactory: s.reviewerFor})
		if err != nil {
			return nil, err
		}
		s.ownerWorker, err = s.ownerFor(ctx, opts.Profile)
		if err != nil {
			return nil, err
		}
	}
	binding := struct {
		Backend, ProfileID string
		Modules            ModuleSelection
	}{opts.Profile.Backend, opts.Profile.ID, opts.Profile.Modules.Core()}
	bindingKey := contextbuilder.OwnerKey(opts.Profile.Scope("binding"))
	err = db.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "business-bindings", bindingKey, "selected")
		raw := encode(binding)
		if e == nil {
			var old struct {
				Backend, ProfileID string
				Modules            ModuleSelection
			}
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			old.Modules = old.Modules.Core()
			if string(encode(old)) != string(raw) {
				return errors.New("entry: directory is bound to a different business profile/backend; explicit migration required")
			}
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		_, e = tx.Put(ctx, storage.Document{Namespace: "business-bindings", Scope: bindingKey, ID: "selected", Body: raw})
		return e
	})
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(opts.Profile)
	id := opts.Profile.ID + "/" + opts.Profile.Revision
	err = db.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "profiles", opts.Profile.Key(), id)
		if e == nil {
			var old Profile
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			if string(encode(old)) != string(raw) {
				return errors.New("entry: profile changed under same revision")
			}
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		_, e = tx.Put(ctx, storage.Document{Namespace: "profiles", Scope: opts.Profile.Key(), ID: id, Body: raw})
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
			if s.opts.AuthorizeScope != nil {
				return s.opts.AuthorizeScope(ctx, scope)
			}
			return nil
		}
	}
	return work.ErrScope
}
func (s *standalone) Submit(ctx context.Context, in Input) (Admission, error) {
	return s.chat.Submit(ctx, in)
}
func (s *standalone) Resume(ctx context.Context, id string, in Input, target string) error {
	return s.chat.Resume(ctx, id, in, target)
}
func (s *standalone) Pending(ctx context.Context, scene string) (map[string][]string, error) {
	return s.chat.Pending(ctx, scene)
}
func (s *standalone) Tasks(ctx context.Context, scope work.Scope) ([]work.Task, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	return s.authority.List(ctx, scope)
}
func (s *standalone) Deliveries(ctx context.Context, scope work.Scope) ([]interaction.Record, error) {
	if s.hostTasks != nil {
		return s.hostDeliveries(ctx, scope)
	}
	return s.delivery.List(ctx, scope)
}
func (s *standalone) steps() []func(context.Context) (bool, error) {
	steps := make([]func(context.Context) (bool, error), 0, len(s.opts.Profile.Scenes)+4)
	seen := map[string]bool{}
	for _, scene := range s.opts.Profile.Scenes {
		if seen[scene] {
			continue
		}
		seen[scene] = true
		steps = append(steps, func(ctx context.Context) (bool, error) { return s.chat.StepScene(ctx, scene) })
	}
	if s.hostTasks != nil {
		return steps
	}
	return append(steps, s.tasksWorker.Step, s.interaction.Step, s.tasksWorker.Timing, s.ownerOnce)
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
	for _, step := range s.steps() {
		s.wg.Go(func() {
			for workerCtx.Err() == nil {
				progress, err := step(workerCtx)
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
	for _, step := range s.steps() {
		if progress, err := step(ctx); progress || err != nil {
			return progress, err
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
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	var err error
	for i := len(s.moduleClosers) - 1; i >= 0; i-- {
		err = errors.Join(err, s.moduleClosers[i].Close())
	}
	if !nilModule(s.files) {
		err = errors.Join(err, s.files.Close())
	}
	if s.lease != nil {
		err = errors.Join(err, s.lease.Release())
	}
	if !nilModule(s.db) {
		err = errors.Join(err, s.db.Close())
	}
	return err
}
func encode(v any) json.RawMessage         { raw, _ := json.Marshal(v); return raw }
func (s *standalone) Errors() <-chan error { return s.errors }

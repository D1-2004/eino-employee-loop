package entry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	native "github.com/D1-2004/eino-employee-loop/agent"
	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/execution"
	"github.com/D1-2004/eino-employee-loop/ownerloop"
	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/work"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
)

// ModuleRef selects trusted compiled code, never an import path or command.
// The zero value selects the immutable builtin@1 implementation.
type ModuleRef struct{ Name, Version string }

var builtinModule = ModuleRef{"builtin", "1"}

func (r ModuleRef) effective() ModuleRef {
	if r == (ModuleRef{}) {
		return builtinModule
	}
	return r
}
func (r ModuleRef) valid() bool { return validPlugin(PluginRef{r.Name, r.Version}) }

type ModuleSelection struct{ Storage, Context, Executor, Review, Scheduler, Owner ModuleRef }

func (m ModuleSelection) core() ModuleSelection {
	m.Review = ModuleRef{}
	m.Owner = ModuleRef{}
	m.Storage = m.Storage.effective()
	m.Context = m.Context.effective()
	m.Executor = m.Executor.effective()
	m.Scheduler = m.Scheduler.effective()
	return m
}

// ContextRuntime never treats a frozen frame as a continuing permission grant.
type ContextRuntime interface {
	Admit(context.Context, work.Scope, contextbuilder.Source) (contextbuilder.Source, error)
	Source(context.Context, work.Scope, string) (contextbuilder.Source, contextbuilder.Ref, error)
	Build(context.Context, work.Scope, contextbuilder.View, string, string) (contextbuilder.Frame, error)
	Revalidate(context.Context, contextbuilder.Frame) error
	Revoke(context.Context, work.Scope, string) error
	ResetOwner(context.Context, work.Scope) error
	OwnerEpoch(context.Context, work.Scope) (uint64, error)
	CheckAudience(context.Context, work.Scope, work.Scope, contextbuilder.View) error
}
type OwnerRuntime interface {
	Observe(context.Context, work.Scope, string) error
	Tick(context.Context, work.Scope) (bool, error)
	Documents(context.Context, work.Scope) ([]ownerloop.Document, error)
}
type SchedulerRuntime interface {
	Schedule(context.Context, work.Scope, work.Schedule) (work.Schedule, error)
	Signal(context.Context, work.Scope, work.Signal) error
	Tick(context.Context, work.Scope, time.Time) (bool, error)
	Occurrences(context.Context, work.Scope) ([]work.Occurrence, error)
	RecoverOccurrence(context.Context, work.Scope, string) (bool, error)
	Acknowledge(context.Context, work.Scope, string) error
	Schedules(context.Context, work.Scope) ([]work.Schedule, error)
	AcknowledgeForGoal(context.Context, work.Scope, string, uint64, uint64) error
	Observe(context.Context, work.Scope, work.Attention) (work.Attention, error)
	Attention(context.Context, work.Scope) ([]work.Attention, error)
	ResolveAttention(context.Context, work.Scope, string, string, work.Source) error
}
type ReviewRequest struct {
	Task    work.Task
	Frame   contextbuilder.Frame
	Profile Profile
	Receipt string
}
type Reviewer interface {
	Propose(context.Context, ReviewRequest) (work.Review, error)
}
type ReviewFunc func(context.Context, ReviewRequest) (work.Review, error)

func (f ReviewFunc) Propose(ctx context.Context, r ReviewRequest) (work.Review, error) {
	return f(ctx, r)
}

// Factory dependencies expose only the contract needed by that module. Shared
// resources stay Host-owned; factories must not close State or Artifacts.
type StorageDependencies struct {
	Directory string
	Profile   Profile
}
type ContextDependencies struct {
	State     storage.State
	Artifacts storage.Artifacts
	Tasks     contextbuilder.Tasks
	Authorize func(context.Context, work.Scope) error
	Audience  contextbuilder.AudiencePolicy
}
type ExecutorDependencies struct {
	State     storage.State
	Owner     string
	Authorize func(context.Context, work.Scope) error
	Run       func(context.Context, execution.Record) (execution.Result, error)
}
type ReviewDependencies struct {
	Authorize func(context.Context, work.Scope) error
	Profile   Profile
}

// SourceReader is the only Context capability required by Scheduler.
type SourceReader interface {
	Source(context.Context, work.Scope, string) (contextbuilder.Source, contextbuilder.Ref, error)
}
type SchedulerDependencies struct {
	State     storage.State
	Work      *work.Manager
	Context   SourceReader
	Authorize func(context.Context, work.Scope) error
}
type OwnerDependencies struct {
	State     storage.State
	Artifacts storage.Artifacts
	Context   ownerloop.Context
	Owner     string
	Authorize func(context.Context, work.Scope) error
	Profile   Profile
	Models    func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error)
	Callbacks []callbacks.Handler
	// Plugins resolves the observation's frozen configuration, not the current
	// Host profile. It does not expose registry mutation or unrelated Options.
	Plugins func(context.Context, ownerloop.Request, native.Config) (native.Config, error)
}
type StorageFactory func(context.Context, StorageDependencies) (storage.State, storage.Artifacts, error)
type ContextFactory func(context.Context, ContextDependencies) (ContextRuntime, error)
type ExecutorFactory func(context.Context, ExecutorDependencies) (execution.Port, error)
type ReviewFactory func(context.Context, ReviewDependencies) (Reviewer, error)
type SchedulerFactory func(context.Context, SchedulerDependencies) (SchedulerRuntime, error)
type OwnerFactory func(context.Context, OwnerDependencies) (OwnerRuntime, error)

type ModuleRegistry struct {
	mu         sync.RWMutex
	storage    map[ModuleRef]StorageFactory
	contexts   map[ModuleRef]ContextFactory
	executors  map[ModuleRef]ExecutorFactory
	reviews    map[ModuleRef]ReviewFactory
	schedulers map[ModuleRef]SchedulerFactory
	owners     map[ModuleRef]OwnerFactory
}

func NewModuleRegistry() *ModuleRegistry { return &ModuleRegistry{} }
func registerModule[F any](mu *sync.RWMutex, m *map[ModuleRef]F, r ModuleRef, f F) error {
	if !r.valid() || r == builtinModule {
		return errors.New("entry: exact non-builtin module name/version required")
	}
	mu.Lock()
	defer mu.Unlock()
	if *m == nil {
		*m = map[ModuleRef]F{}
	}
	if _, ok := (*m)[r]; ok {
		return fmt.Errorf("entry: module %s@%s already registered", r.Name, r.Version)
	}
	(*m)[r] = f
	return nil
}
func (r *ModuleRegistry) RegisterStorage(ref ModuleRef, f StorageFactory) error {
	if r == nil || f == nil {
		return errors.New("entry: registry/factory required")
	}
	return registerModule(&r.mu, &r.storage, ref, f)
}
func (r *ModuleRegistry) RegisterContext(ref ModuleRef, f ContextFactory) error {
	if r == nil || f == nil {
		return errors.New("entry: registry/factory required")
	}
	return registerModule(&r.mu, &r.contexts, ref, f)
}
func (r *ModuleRegistry) RegisterExecutor(ref ModuleRef, f ExecutorFactory) error {
	if r == nil || f == nil {
		return errors.New("entry: registry/factory required")
	}
	return registerModule(&r.mu, &r.executors, ref, f)
}
func (r *ModuleRegistry) RegisterReview(ref ModuleRef, f ReviewFactory) error {
	if r == nil || f == nil {
		return errors.New("entry: registry/factory required")
	}
	return registerModule(&r.mu, &r.reviews, ref, f)
}
func (r *ModuleRegistry) RegisterScheduler(ref ModuleRef, f SchedulerFactory) error {
	if r == nil || f == nil {
		return errors.New("entry: registry/factory required")
	}
	return registerModule(&r.mu, &r.schedulers, ref, f)
}
func (r *ModuleRegistry) RegisterOwner(ref ModuleRef, f OwnerFactory) error {
	if r == nil || f == nil {
		return errors.New("entry: registry/factory required")
	}
	return registerModule(&r.mu, &r.owners, ref, f)
}
func (r *ModuleRegistry) validate(m ModuleSelection) error {
	if r != nil {
		r.mu.RLock()
		defer r.mu.RUnlock()
	}
	for _, v := range []struct {
		kind   string
		ref    ModuleRef
		exists bool
	}{
		{"storage", m.Storage, r != nil && r.storage[m.Storage] != nil}, {"context", m.Context, r != nil && r.contexts[m.Context] != nil},
		{"executor", m.Executor, r != nil && r.executors[m.Executor] != nil}, {"review", m.Review, r != nil && r.reviews[m.Review] != nil},
		{"scheduler", m.Scheduler, r != nil && r.schedulers[m.Scheduler] != nil}, {"owner", m.Owner, r != nil && r.owners[m.Owner] != nil},
	} {
		ref := v.ref.effective()
		if !ref.valid() || ref != builtinModule && !v.exists {
			return fmt.Errorf("entry: %s module %s@%s is not registered", v.kind, ref.Name, ref.Version)
		}
	}
	return nil
}
func profileSnapshot(p Profile) Profile {
	p.Scenes = append([]string(nil), p.Scenes...)
	p.OwnerLoops = append([]string(nil), p.OwnerLoops...)
	p.DeliveryLoops = append([]string(nil), p.DeliveryLoops...)
	p.Plugins = append([]PluginRef(nil), p.Plugins...)
	return p
}

type taskReader struct{ manager *work.Manager }

func (r taskReader) Get(ctx context.Context, scope work.Scope, id string) (work.Task, error) {
	return r.manager.Get(ctx, scope, id)
}
func (r taskReader) List(ctx context.Context, scope work.Scope) ([]work.Task, error) {
	return r.manager.List(ctx, scope)
}
func (s *standalone) contextDependencies() ContextDependencies {
	return ContextDependencies{State: s.db, Artifacts: s.files, Tasks: taskReader{s.work}, Authorize: s.authorize, Audience: s.opts.AudiencePolicy}
}
func (s *standalone) executorDependencies() ExecutorDependencies {
	return ExecutorDependencies{State: s.db, Owner: s.owner, Authorize: s.authorize, Run: s.execute}
}
func (s *standalone) reviewDependencies(p Profile) ReviewDependencies {
	return ReviewDependencies{Authorize: s.authorize, Profile: profileSnapshot(p)}
}

type sourceReader struct{ context ContextRuntime }

func (r sourceReader) Source(ctx context.Context, scope work.Scope, id string) (contextbuilder.Source, contextbuilder.Ref, error) {
	return r.context.Source(ctx, scope, id)
}
func (s *standalone) schedulerDependencies() SchedulerDependencies {
	return SchedulerDependencies{State: s.db, Work: s.work, Context: sourceReader{s.context}, Authorize: s.authorize}
}
func (s *standalone) own(v any) {
	if c, ok := v.(io.Closer); ok && !nilModule(v) {
		s.moduleClosers = append(s.moduleClosers, c)
	}
}

func nilModule(v any) bool {
	if v == nil {
		return true
	}
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

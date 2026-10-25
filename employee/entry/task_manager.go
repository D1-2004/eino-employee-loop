package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/D1-2004/eino-employee-loop/employee/chat"
	cb "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	taskmodule "github.com/D1-2004/eino-employee-loop/employee/task"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

// Host factories receive identity/current authority, never the SDK's business
// store. The Host supplies authorized Task projections from its own authority.
type TaskManagerDependencies struct {
	Profile   Profile
	Authorize func(context.Context, work.Scope) error
}
type TaskTiming interface {
	chat.Scheduler
	Signal(context.Context, work.Scope, work.Signal) error
}
type TaskRecovery interface {
	Retry(context.Context, work.Scope, string) (bool, error)
	Failures(context.Context, work.Scope) ([]Failure, error)
}
type IntentAuthority interface {
	interaction.IntentSink
	List(context.Context, work.Scope) ([]interaction.Record, error)
	LookupIntent(context.Context, work.Scope, string) (interaction.Record, error)
}
type MessageComposer interface {
	EnqueueMessage(context.Context, Profile, Input, string, string, string, string) (any, error)
}

// TaskManagerBinding is a whole Host-driven module, split into narrow callers.
// The reserved builtin factory opens its Manager first and binds local runtime
// components after Context/Runner exist. External factories must be HostDriven;
// none of the local execution/review/timing/result/Outbox workers are constructed.
type TaskManagerBinding struct {
	HostDriven bool
	Queries    cb.Tasks
	Operations taskmodule.Operations
	Timing     TaskTiming
	Recovery   TaskRecovery
	Intents    IntentAuthority
	Messages   MessageComposer
	Closer     io.Closer
	builtin    *work.Manager
}
type TaskManagerFactory func(context.Context, TaskManagerDependencies) (TaskManagerBinding, error)

func (s *standalone) openTaskManager(ctx context.Context) error {
	ref := s.opts.Profile.Modules.TaskManagerRef()
	factory := TaskManagerFactory(func(_ context.Context, _ TaskManagerDependencies) (TaskManagerBinding, error) {
		manager, err := work.New(s.db)
		return TaskManagerBinding{Queries: manager, builtin: manager}, err
	})
	if ref != builtinModule {
		s.opts.Modules.mu.RLock()
		factory = s.opts.Modules.taskManagers[ref]
		s.opts.Modules.mu.RUnlock()
	}
	if factory == nil {
		return fmt.Errorf("entry: original TaskManager %s@%s unavailable", ref.Name, ref.Version)
	}
	binding, err := factory(ctx, TaskManagerDependencies{Profile: profileSnapshot(s.opts.Profile), Authorize: s.authorize})
	s.own(binding.Closer)
	if err != nil {
		return err
	}
	if ref == builtinModule {
		s.work = binding.builtin
		s.authority = binding.builtin
		return nil
	}
	if !binding.HostDriven || nilModule(binding.Queries) || nilModule(binding.Operations) || nilModule(binding.Timing) || nilModule(binding.Recovery) || nilModule(binding.Intents) || nilModule(binding.Messages) || nilModule(binding.Closer) {
		return errors.New("entry: complete HostDriven TaskManager/Outbox binding required")
	}
	s.hostTasks = &binding
	s.authority = &hostTaskAuthority{s: s, binding: &binding}
	return nil
}

func validateTaskManagerOptions(opts Options) error {
	if opts.Profile.Modules.TaskManagerRef() == builtinModule {
		return nil
	}
	for _, ref := range []ModuleRef{opts.Profile.Modules.Executor, opts.Profile.Modules.Review, opts.Profile.Modules.Scheduler, opts.Profile.Modules.Owner} {
		if ref.Effective() != builtinModule {
			return errors.New("entry: HostDriven TaskManager conflicts with local executor/review/scheduler/owner module")
		}
	}
	if len(opts.Profile.OwnerLoops) > 0 || len(opts.Profile.DeliveryLoops) > 0 || !nilModule(opts.Delivery) {
		return errors.New("entry: HostDriven TaskManager owns cognitive loops and Outbox; local loops/transport must not be selected")
	}
	return nil
}

type hostTaskAuthority struct {
	s       *standalone
	binding *TaskManagerBinding
}

func (h *hostTaskAuthority) frozenTask(t work.Task) error {
	var marker struct{ Version string }
	if json.Unmarshal(t.Definition.Configuration, &marker) != nil || marker.Version != "local-el-profile/1" {
		return nil
	}
	var profile Profile
	if err := json.Unmarshal(t.Definition.Configuration, &profile); err != nil {
		return err
	}
	ref := profile.Modules.TaskManagerRef()
	if ref != h.s.opts.Profile.Modules.TaskManagerRef() {
		return fmt.Errorf("entry: Task %s retains original TaskManager %s@%s; original provider required", t.ID, ref.Name, ref.Version)
	}
	return nil
}

func (h *hostTaskAuthority) Get(ctx context.Context, scope work.Scope, id string) (work.Task, error) {
	if err := h.s.authorize(ctx, scope); err != nil {
		return work.Task{}, err
	}
	t, err := h.binding.Queries.Get(ctx, scope, id)
	if err != nil {
		return t, err
	}
	if t.Scope != scope || t.ID != id {
		return work.Task{}, work.ErrScope
	}
	if err = h.frozenTask(t); err != nil {
		return work.Task{}, err
	}
	if err = h.s.authorize(ctx, scope); err != nil {
		return work.Task{}, err
	}
	return t, nil
}
func (h *hostTaskAuthority) List(ctx context.Context, scope work.Scope) ([]work.Task, error) {
	if err := h.s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	tasks, err := h.binding.Queries.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.Scope != scope || t.ID == "" {
			return nil, work.ErrScope
		}
		if err = h.frozenTask(t); err != nil {
			return nil, err
		}
	}
	if err = h.s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	return tasks, nil
}
func (h *hostTaskAuthority) source(ctx context.Context, scope work.Scope, source work.Source) error {
	if err := h.s.authorize(ctx, scope); err != nil {
		return err
	}
	if h.s.context == nil || source.Actor != scope.Requester || source.Receipt == "" || source.Text == "" {
		return work.ErrScope
	}
	current, _, err := h.s.context.Source(ctx, scope, source.Receipt)
	if err != nil {
		return err
	}
	if current.Actor != source.Actor || current.Text != source.Text {
		return work.ErrScope
	}
	return nil
}
func (h *hostTaskAuthority) execute(ctx context.Context, op taskmodule.Operation) (work.Receipt, error) {
	if op.TaskID != "" {
		if _, err := h.Get(ctx, op.Scope, op.TaskID); err != nil {
			return work.Receipt{}, err
		}
	}
	if op.Call == "" {
		return work.Receipt{}, work.ErrScope
	}
	if err := h.source(ctx, op.Scope, op.Source); err != nil {
		return work.Receipt{}, err
	}
	for _, source := range op.ExtraSources {
		if err := h.source(ctx, op.Scope, source); err != nil {
			return work.Receipt{}, err
		}
	}
	raw, err := json.Marshal(op)
	if err != nil {
		return work.Receipt{}, err
	}
	key, hash := op.Key(), op.SHA256()
	copyOp := func() taskmodule.Operation {
		var copy taskmodule.Operation
		_ = json.Unmarshal(raw, &copy)
		return copy
	}
	validate := func(r taskmodule.OperationResult) error {
		if r.Scope != op.Scope {
			return work.ErrScope
		}
		if r.OperationKey != key || r.RequestSHA256 != hash {
			return storage.ErrConflict
		}
		return nil
	}
	result, err := h.binding.Operations.LookupOperation(ctx, copyOp())
	if err != nil {
		return work.Receipt{}, err
	}
	if err = validate(result); err != nil {
		return work.Receipt{}, err
	}
	if err = h.source(ctx, op.Scope, op.Source); err != nil {
		return work.Receipt{}, err
	}
	for _, source := range op.ExtraSources {
		if err = h.source(ctx, op.Scope, source); err != nil {
			return work.Receipt{}, err
		}
	}
	switch result.State {
	case "accepted":
	case "unknown":
		return work.Receipt{}, taskmodule.ErrOperationUnknown
	case "not_started":
		result, err = h.binding.Operations.ExecuteOperation(ctx, copyOp())
		if err != nil {
			return work.Receipt{}, err
		}
		if err = validate(result); err != nil {
			return work.Receipt{}, err
		}
	default:
		return work.Receipt{}, errors.New("entry: invalid original operation lookup")
	}
	if result.State != "accepted" {
		return work.Receipt{}, taskmodule.ErrOperationUnknown
	}
	if result.Receipt.ID == "" || result.Receipt.TaskID == "" || (op.TaskID != "" && result.Receipt.TaskID != op.TaskID) {
		return work.Receipt{}, storage.ErrConflict
	}
	if err = h.source(ctx, op.Scope, op.Source); err != nil {
		return work.Receipt{}, err
	}
	return result.Receipt, nil
}

func (s *standalone) hostTasksGuardSource(ctx context.Context, scope work.Scope, source work.Source) error {
	return (&hostTaskAuthority{s: s, binding: s.hostTasks}).source(ctx, scope, source)
}
func (h *hostTaskAuthority) DispatchWithSources(ctx context.Context, s work.Scope, source work.Source, call string, d work.Definition, extra []work.Source) (work.Receipt, error) {
	return h.execute(ctx, taskmodule.Operation{Scope: s, Source: source, Call: call, Kind: "dispatch", Definition: d, ExtraSources: extra})
}
func (h *hostTaskAuthority) Control(ctx context.Context, s work.Scope, source work.Source, call, id, kind, instruction string) (work.Receipt, error) {
	return h.execute(ctx, taskmodule.Operation{Scope: s, Source: source, Call: call, Kind: "control", TaskID: id, Action: kind, Instruction: instruction})
}
func (h *hostTaskAuthority) Answer(ctx context.Context, s work.Scope, source work.Source, call, id, wait string, revision uint64) (work.Receipt, error) {
	return h.execute(ctx, taskmodule.Operation{Scope: s, Source: source, Call: call, Kind: "answer", TaskID: id, WaitID: wait, GoalRevision: revision})
}

type hostIntentSink struct{ s *standalone }

func (h hostIntentSink) Enqueue(ctx context.Context, in interaction.Intent) (interaction.Record, error) {
	if err := interaction.ValidateIntent(in); err != nil {
		return interaction.Record{}, err
	}
	var original cb.Ref
	check := func() error {
		if err := h.s.authorize(ctx, in.Scope); err != nil {
			return err
		}
		source, ref, err := h.s.context.Source(ctx, in.Scope, in.SourceReceipt)
		if err != nil {
			return err
		}
		if source.Actor != in.Scope.Requester {
			return work.ErrScope
		}
		if original.ID != "" && ref != original {
			return cb.ErrRevoked
		}
		original = ref
		if h.s.opts.AuthorizeDelivery != nil {
			if err = h.s.opts.AuthorizeDelivery(ctx, in); err != nil {
				return err
			}
		}
		return h.s.authorize(ctx, in.Scope)
	}
	if err := check(); err != nil {
		return interaction.Record{}, err
	}
	prior, err := h.s.hostTasks.Intents.LookupIntent(ctx, in.Scope, in.ID)
	if err == nil {
		if err = check(); err != nil {
			return interaction.Record{}, err
		}
		if prior.Intent.Scope != in.Scope || !interaction.SameIntentCommand(prior.Intent, in) {
			return interaction.Record{}, storage.ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return interaction.Record{}, err
	}
	// Lookup may have performed remote I/O. Recheck the original source and
	// current delivery grant immediately before any new Outbox admission.
	if err = check(); err != nil {
		return interaction.Record{}, err
	}
	record, err := h.s.hostTasks.Intents.Enqueue(ctx, in)
	if err != nil {
		return interaction.Record{}, err
	}
	if record.Intent.Scope != in.Scope || !interaction.SameIntentCommand(record.Intent, in) {
		return interaction.Record{}, storage.ErrConflict
	}
	if err = check(); err != nil {
		return interaction.Record{}, err
	}
	return record, nil
}

func (s *standalone) bindHostDriven(ctx context.Context) error {
	var err error
	s.chat, err = chat.New(chat.Options{State: s.db, Profile: s.opts.Profile, Context: s.context, Tasks: s.authority, Scheduler: hostTiming{s}, Outbox: hostIntentSink{s}, Runner: s.runner, Authorize: s.authorize, EnqueueMessage: func(c context.Context, p Profile, in Input, job, call, kind, instruction string) (any, error) {
		if p.Modules.TaskManagerRef() != s.opts.Profile.Modules.TaskManagerRef() {
			return nil, errors.New("entry: original TaskManager differs")
		}
		if err := s.hostTasksGuardSource(c, p.Scope(in.Scene), work.Source{Receipt: in.ID, Actor: in.Actor, Text: in.Text}); err != nil {
			return nil, err
		}
		out, err := s.hostTasks.Messages.EnqueueMessage(c, profileSnapshot(p), in, job, call, kind, instruction)
		if err != nil {
			return nil, err
		}
		if err = s.hostTasksGuardSource(c, p.Scope(in.Scene), work.Source{Receipt: in.ID, Actor: in.Actor, Text: in.Text}); err != nil {
			return nil, err
		}
		return out, nil
	}, CancelExecution: func(string) {}})
	return err
}

type hostTiming struct{ s *standalone }

func (h hostTiming) Schedule(ctx context.Context, scope work.Scope, in work.Schedule) (work.Schedule, error) {
	if err := h.s.hostTasksGuardSource(ctx, scope, in.Source); err != nil {
		return work.Schedule{}, err
	}
	if _, err := h.s.authority.Get(ctx, scope, in.TaskID); err != nil {
		return work.Schedule{}, err
	}
	if err := h.s.hostTasksGuardSource(ctx, scope, in.Source); err != nil {
		return work.Schedule{}, err
	}
	out, err := h.s.hostTasks.Timing.Schedule(ctx, scope, in)
	if err != nil {
		return work.Schedule{}, err
	}
	if err = h.s.hostTasksGuardSource(ctx, scope, in.Source); err != nil {
		return work.Schedule{}, err
	}
	return out, nil
}
func (h hostTiming) Observe(ctx context.Context, scope work.Scope, in work.Attention) (work.Attention, error) {
	if err := h.s.hostTasksGuardSource(ctx, scope, in.Source); err != nil {
		return work.Attention{}, err
	}
	if in.TaskID != "" {
		if _, err := h.s.authority.Get(ctx, scope, in.TaskID); err != nil {
			return work.Attention{}, err
		}
	}
	if err := h.s.hostTasksGuardSource(ctx, scope, in.Source); err != nil {
		return work.Attention{}, err
	}
	out, err := h.s.hostTasks.Timing.Observe(ctx, scope, in)
	if err != nil {
		return work.Attention{}, err
	}
	if err = h.s.hostTasksGuardSource(ctx, scope, in.Source); err != nil {
		return work.Attention{}, err
	}
	return out, nil
}
func (h hostTiming) Attention(ctx context.Context, scope work.Scope) ([]work.Attention, error) {
	if err := h.s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	items, err := h.s.hostTasks.Timing.Attention(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, in := range items {
		if err = h.s.hostTasksGuardSource(ctx, scope, in.Source); err != nil {
			return nil, err
		}
	}
	if err = h.s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	return items, nil
}
func (h hostTiming) ResolveAttention(ctx context.Context, scope work.Scope, id, state string, source work.Source) error {
	if err := h.s.hostTasksGuardSource(ctx, scope, source); err != nil {
		return err
	}
	if err := h.s.hostTasks.Timing.ResolveAttention(ctx, scope, id, state, source); err != nil {
		return err
	}
	return h.s.hostTasksGuardSource(ctx, scope, source)
}

func (s *standalone) hostDeliveries(ctx context.Context, scope work.Scope) ([]interaction.Record, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	items, err := s.hostTasks.Intents.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, r := range items {
		if r.Intent.Scope != scope || r.Intent.ID == "" {
			return nil, work.ErrScope
		}
	}
	if err = s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	return items, nil
}

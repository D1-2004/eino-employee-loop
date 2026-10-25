package entry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	taskmodule "github.com/D1-2004/eino-employee-loop/employee/task"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/schema"
)

// This Host is independent: no work.Manager, SDK business DB, Bridge or worker.
// Its maps represent its own operation/Task/Outbox authority across SDK reopen.
type independentTasks struct {
	mu                                                               sync.Mutex
	tasks                                                            map[string]work.Task
	ops                                                              map[string]taskmodule.OperationResult
	intents                                                          map[string]interaction.Record
	directory                                                        string
	executes, lookups, closes, retries, schedules, signals, messages int
	loseResponse                                                     bool
	lookupState, corrupt                                             string
	foreign                                                          bool
	intentLookupHook, failureHook, taskGetHook                       func()
	intentEnqueues                                                   int
}

func newIndependentTasks(t *testing.T) *independentTasks {
	return &independentTasks{tasks: map[string]work.Task{}, ops: map[string]taskmodule.OperationResult{}, intents: map[string]interaction.Record{}, directory: t.TempDir()}
}
func (h *independentTasks) Close() error { h.mu.Lock(); defer h.mu.Unlock(); h.closes++; return nil }
func (h *independentTasks) Get(_ context.Context, s work.Scope, id string) (work.Task, error) {
	h.mu.Lock()
	hook := h.taskGetHook
	h.taskGetHook = nil
	defer func() {
		h.mu.Unlock()
		if hook != nil {
			hook()
		}
	}()
	v, ok := h.tasks[id]
	if !ok || v.Scope != s {
		return work.Task{}, storage.ErrNotFound
	}
	if h.foreign {
		v.Scope.Requester = "another-owner"
	}
	return v, nil
}
func (h *independentTasks) List(_ context.Context, s work.Scope) ([]work.Task, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []work.Task{}
	for _, v := range h.tasks {
		if v.Scope == s {
			if h.foreign {
				v.Scope.Requester = "another-owner"
			}
			out = append(out, v)
		}
	}
	return out, nil
}
func (h *independentTasks) LookupOperation(_ context.Context, op taskmodule.Operation) (taskmodule.OperationResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lookups++
	r, found := h.ops[op.Key()]
	if !found {
		r = taskmodule.OperationResult{State: "not_started", Scope: op.Scope, OperationKey: op.Key(), RequestSHA256: op.SHA256()}
	}
	if h.lookupState != "" {
		r.State = h.lookupState
		if r.State == "accepted" && r.Receipt.ID == "" {
			r.Receipt = work.Receipt{ID: "real-format-receipt", TaskID: "real-format-host-task"}
		}
	}
	switch h.corrupt {
	case "hash":
		r.RequestSHA256 = ""
	case "scope":
		r.Scope.Requester = "another-owner"
	case "scope_zero":
		r.Scope = work.Scope{}
	case "key":
		r.OperationKey = "another-operation"
	}
	return r, nil
}
func (h *independentTasks) ExecuteOperation(_ context.Context, op taskmodule.Operation) (taskmodule.OperationResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.executes++
	if old, ok := h.ops[op.Key()]; ok {
		if old.RequestSHA256 != op.SHA256() {
			return old, storage.ErrConflict
		}
		return old, nil
	}
	var task work.Task
	switch op.Kind {
	case "dispatch":
		id := fmt.Sprintf("actual-host-task-%d", len(h.tasks)+1)
		task = work.Task{ID: id, Scope: op.Scope, Revision: 1, Definition: op.Definition, Sources: append([]work.Source{op.Source}, op.ExtraSources...), State: work.Running, Goal: work.Goal{Revision: 1, State: work.Running}, Runs: []work.Run{{ID: "actual-host-run-1", GoalRevision: 1, State: work.RunActive}}}
	case "control", "answer":
		var ok bool
		task, ok = h.tasks[op.TaskID]
		if !ok || task.Scope != op.Scope {
			return taskmodule.OperationResult{}, work.ErrScope
		}
		task.Revision++
		task.Sources = append(task.Sources, op.Source)
		if op.Kind == "answer" {
			if task.State != work.Waiting || op.GoalRevision != task.Goal.Revision || len(task.Waits) != 1 || task.Waits[0].ID != op.WaitID {
				return taskmodule.OperationResult{}, work.ErrStale
			}
			task.Waits[0].Resolved = true
			task.Waits[0].Answer = op.Source.Text
			task.State = work.Running
		} else {
			switch op.Action {
			case "stop":
				task.State = work.Stopped
			case "pause":
				task.State = work.Paused
			case "continue", "steer":
				task.State = work.Running
			default:
				return taskmodule.OperationResult{}, work.ErrState
			}
		}
	default:
		return taskmodule.OperationResult{}, work.ErrState
	}
	h.tasks[task.ID] = task
	r := taskmodule.OperationResult{State: "accepted", Scope: op.Scope, OperationKey: op.Key(), RequestSHA256: op.SHA256(), Receipt: work.Receipt{ID: "actual-host-receipt:" + op.Call, TaskID: task.ID, RunID: task.Runs[0].ID, TaskRevision: task.Revision, GoalRevision: task.Goal.Revision, State: task.State}}
	h.ops[op.Key()] = r
	if h.loseResponse {
		h.loseResponse = false
		return taskmodule.OperationResult{}, taskmodule.ErrOperationUnknown
	}
	return r, nil
}
func (h *independentTasks) Schedule(_ context.Context, _ work.Scope, in work.Schedule) (work.Schedule, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.schedules++
	return in, nil
}
func (h *independentTasks) Signal(_ context.Context, _ work.Scope, _ work.Signal) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.signals++
	return nil
}
func (h *independentTasks) Observe(_ context.Context, _ work.Scope, in work.Attention) (work.Attention, error) {
	return in, nil
}
func (h *independentTasks) Attention(context.Context, work.Scope) ([]work.Attention, error) {
	return nil, nil
}
func (h *independentTasks) ResolveAttention(context.Context, work.Scope, string, string, work.Source) error {
	return nil
}
func (h *independentTasks) Retry(_ context.Context, s work.Scope, id string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.tasks[id]
	if !ok || v.Scope != s {
		return false, nil
	}
	h.retries++
	return true, nil
}
func (h *independentTasks) Failures(_ context.Context, s work.Scope) ([]Failure, error) {
	h.mu.Lock()
	hook := h.failureHook
	h.failureHook = nil
	defer func() {
		h.mu.Unlock()
		if hook != nil {
			hook()
		}
	}()
	for _, v := range h.tasks {
		if v.Scope == s {
			return []Failure{{ID: v.ID, Kind: "host", State: "waiting", Error: "Host-owned recovery handle"}}, nil
		}
	}
	return nil, nil
}
func (h *independentTasks) LookupIntent(_ context.Context, s work.Scope, id string) (interaction.Record, error) {
	h.mu.Lock()
	hook := h.intentLookupHook
	h.intentLookupHook = nil
	defer func() {
		h.mu.Unlock()
		if hook != nil {
			hook()
		}
	}()
	v, ok := h.intents[id]
	if !ok || v.Intent.Scope != s {
		return interaction.Record{}, storage.ErrNotFound
	}
	return v, nil
}
func (h *independentTasks) Enqueue(_ context.Context, in interaction.Intent) (interaction.Record, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.intentEnqueues++
	if old, ok := h.intents[in.ID]; ok {
		if !interaction.SameIntentCommand(old.Intent, in) {
			return interaction.Record{}, storage.ErrConflict
		}
		return old, nil
	}
	path := filepath.Join(h.directory, contract.Digest([]byte(in.ID))+".txt")
	if err := os.WriteFile(path, []byte(in.Body), 0600); err != nil {
		return interaction.Record{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return interaction.Record{}, err
	}
	r := interaction.Record{Intent: in, State: "applied", Receipt: &interaction.Receipt{ID: in.ID, ProviderMessageID: "host-file:" + filepath.Base(path), BodySHA256: contract.Digest(raw), Stage: interaction.StageLocalApplied, ObservedAt: time.Now()}}
	h.intents[in.ID] = r
	return r, nil
}
func (h *independentTasks) EnqueueMessage(_ context.Context, _ Profile, in Input, job, call, kind, instruction string) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages++
	return map[string]string{"accepted_message": "host-message:" + job + ":" + call, "kind": kind, "source": in.ID}, nil
}

type independentOutbox struct{ *independentTasks }

func (h independentOutbox) List(_ context.Context, s work.Scope) ([]interaction.Record, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []interaction.Record{}
	for _, r := range h.intents {
		if r.Intent.Scope == s {
			out = append(out, r)
		}
	}
	return out, nil
}

func externalOptions(t *testing.T, h *independentTasks, dir string, ms map[string]*looptest.Model) (Options, *atomic.Int32) {
	t.Helper()
	p := testProfile()
	ref := ModuleRef{Name: "independent-task-manager", Version: "1"}
	p.Modules.TaskManager = &ref
	// Only Chat runs in this SDK mode, so no fake executor/review/Owner prompts.
	p.PromptBundle = &contract.PromptBundle{Contract: contract.NativePromptContract, SourceRevision: "synthetic/1", SourceManifestSHA256: contract.Digest([]byte("synthetic")), CompilerVersion: "synthetic/1", Roles: map[string]string{"chat": "Use the actual Host Task authority."}}
	p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
	r := NewModuleRegistry()
	if err := r.RegisterTaskManager(ref, func(context.Context, TaskManagerDependencies) (TaskManagerBinding, error) {
		return TaskManagerBinding{HostDriven: true, Queries: h, Operations: h, Timing: h, Recovery: h, Intents: independentOutbox{h}, Messages: h, Closer: h}, nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	opts := Options{Directory: dir, Profile: p, Modules: r, Models: models(ms), DefaultExecutor: func(context.Context, ExecutorDependencies) (execution.Port, error) {
		calls.Add(1)
		return nil, errors.New("must not construct local executor")
	}, DefaultDelivery: func(context.Context, DeliveryDependencies) (interaction.Port, error) {
		calls.Add(1)
		return nil, errors.New("must not construct local transport")
	}}
	return opts, calls
}
func assertNoSDKBusiness(t *testing.T, e *Entry) {
	t.Helper()
	s := e.backend.(*standalone)
	if s.work != nil || s.executor != nil || s.bridge != nil || s.tasksWorker != nil || s.scheduler != nil || s.delivery != nil || s.interaction != nil || s.ownerWorker != nil {
		t.Fatal("Host mode constructed a local business component")
	}
	key, _ := s.opts.Profile.Scope("dm").Key()
	for _, namespace := range []string{"tasks", "work-receipts", "work-events", "execution-requests", "executions", "execution-file-effects", "review-holds", "work-schedules", "work-signals", "work-occurrences", "work-attention", "outbox", "message-jobs", "owner-observations"} {
		docs, err := s.db.List(context.Background(), namespace, key)
		if err != nil || len(docs) != 0 {
			t.Fatalf("SDK business documents written in %s: %d %v", namespace, len(docs), err)
		}
		events, err := s.db.Events(context.Background(), namespace, key, 0, 1000)
		if err != nil || len(events) != 0 {
			t.Fatalf("SDK business events written in %s", namespace)
		}
	}
}

func TestWholeTaskManagerHostOwnsNativeDispatchWaitControlAndOutbox(t *testing.T) {
	ctx := context.Background()
	h := newIndependentTasks(t)
	dir := t.TempDir()
	ms := map[string]*looptest.Model{"chat": looptest.New(looptest.Func(func(in []*schema.Message) looptest.Step {
		f := frameOf(t, in)
		return call("dispatch_task", "dispatch", map[string]any{"goal": f.Current.Text, "instruction": f.Current.Text, "reply": "Accepted by original Host", "source_ref": f.Current.ID})
	}))}
	opts, factories := externalOptions(t, h, dir, ms)
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	input := testInput("source", "Host owns the actual task")
	a, err := e.Submit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || tasks[0].ID != "actual-host-task-1" {
		t.Fatal("Host identity was replaced", err)
	}
	id := tasks[0].ID
	if duplicate, err := e.Submit(ctx, input); err != nil || !duplicate.Replay || duplicate.ID != a.ID {
		t.Fatal("duplicate input did not retain admission", err)
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if h.executes != 1 {
		t.Fatal("duplicate created another Host task")
	}
	// Host progress is read directly, without a local execution/review worker.
	h.mu.Lock()
	v := h.tasks[id]
	v.State = work.Waiting
	v.Waits = []work.Wait{{ID: "actual-host-wait", Kind: "human", Prompt: "Which period?"}}
	v.Runs[0].ExitProven = true
	h.tasks[id] = v
	h.mu.Unlock()
	ms["chat"] = looptest.New(call("answer_question", "answer", map[string]any{"task_id": id, "wait_id": "actual-host-wait", "goal_revision": 1, "source_ref": "answer-source"}))
	if _, err = e.Submit(ctx, testInput("answer-source", "Q3")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Tasks(ctx, "dm"); len(got) != 1 || got[0].Waits[0].Answer != "Q3" || got[0].ID != id {
		t.Fatal("Host wait answer was mirrored or lost")
	}
	ms["chat"] = looptest.New(call("stop_task", "stop", map[string]any{"task_id": id, "source_ref": "stop-source", "reply": "Stop accepted"}))
	if _, err = e.Submit(ctx, testInput("stop-source", "Stop the existing task")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Tasks(ctx, "dm"); len(got) != 1 || got[0].State != work.Stopped || got[0].ID != id {
		t.Fatal("control lost original Host identity")
	}
	if _, err = e.Schedule(ctx, "dm", work.Schedule{ID: "original-watch", TaskID: id, GoalRevision: 1, Source: work.Source{Receipt: input.ID, Actor: input.Actor, Text: input.Text}, Condition: "report.changed"}); err != nil {
		t.Fatal(err)
	}
	if err = e.Signal(ctx, testInput("signal-source", "The report changed"), "report.changed"); err != nil {
		t.Fatal(err)
	}
	if err = e.Retry(ctx, id); err != nil {
		t.Fatal(err)
	}
	if failures, err := e.Failures(ctx, "dm"); err != nil || len(failures) != 1 || failures[0].ID != id {
		t.Fatal("Host recovery was not consulted", err)
	}
	// Foreground questions/replies use the original Host Outbox; composition
	// delegates to the Host and must not create SDK M6 message jobs.
	ms["chat"] = looptest.New(call("ask_input", "ask", map[string]any{"prompt": "Which period?", "source_ref": "question-source"}), call("reply", "answer-reply", map[string]any{"text": "Original answer received", "source_ref": "question-source"}))
	if _, err = e.Submit(ctx, testInput("question-source", "Ask me for the missing period")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	pending, err := e.Pending(ctx, "dm")
	if err != nil || len(pending) != 1 {
		t.Fatal("missing native interruption", err)
	}
	for job, targets := range pending {
		if len(targets) != 1 {
			t.Fatal("wrong native target")
		}
		if err = e.Resume(ctx, job, testInput("question-answer", "Q3"), targets[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	ms["chat"] = looptest.New(call("compose_message", "greeting", map[string]any{"kind": "greeting", "instruction": "Prepare the requested greeting", "source_ref": "greeting-source"}))
	if _, err = e.Submit(ctx, testInput("greeting-source", "Prepare a greeting")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if h.messages != 1 {
		t.Fatal("requested composition was not delegated to Host")
	}
	rows, err := e.Deliveries(ctx, "dm")
	if err != nil || len(rows) != 4 {
		t.Fatal("did not use original Host Outbox", err)
	}
	for _, r := range rows {
		if r.State != "applied" || r.Receipt == nil || !strings.HasPrefix(r.Receipt.ProviderMessageID, "host-file:") {
			t.Fatal("missing actual Host file receipt")
		}
	}
	assertNoSDKBusiness(t, e)
	if factories.Load() != 0 || h.schedules != 1 || h.signals != 1 || h.retries != 1 {
		t.Fatal("local runtime ran or Host capabilities were dropped")
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	if h.closes != 1 {
		t.Fatal("owned provider was not closed exactly once")
	}
	e, err = openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Tasks(ctx, "dm"); len(got) != 1 || got[0].ID != id {
		t.Fatal("reopen did not use original Host authority")
	}
	// A Host projection of an admitted public Profile must retain its original
	// provider too, even if this directory itself selects the current factory.
	h.mu.Lock()
	oldTask := h.tasks[id]
	wrong := opts.Profile.Snapshot()
	oldRef := ModuleRef{Name: "original-task-manager", Version: "0"}
	wrong.Modules.TaskManager = &oldRef
	changed := oldTask
	changed.Definition.Configuration = encode(wrong)
	h.tasks[id] = changed
	h.mu.Unlock()
	if _, err = e.Tasks(ctx, "dm"); err == nil || !strings.Contains(err.Error(), "original-task-manager@0") {
		t.Fatal("current provider interpreted an old Task binding", err)
	}
	h.mu.Lock()
	h.tasks[id] = oldTask
	h.mu.Unlock()
	assertNoSDKBusiness(t, e)
	_ = e.Close()
	other := ModuleRef{Name: "independent-task-manager", Version: "2"}
	opts.Profile.Modules.TaskManager = &other
	if err = opts.Modules.RegisterTaskManager(other, func(context.Context, TaskManagerDependencies) (TaskManagerBinding, error) {
		t.Fatal("wrong provider factory was called")
		return TaskManagerBinding{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = openDefault(ctx, opts); err == nil || !strings.Contains(err.Error(), "independent-task-manager@1") {
		t.Fatal("new provider reinterpreted old directory", err)
	}
}

func TestWholeTaskManagerLostResponseReconcilesOriginalNativeCall(t *testing.T) {
	ctx := context.Background()
	h := newIndependentTasks(t)
	h.loseResponse = true
	ms := map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t))}
	opts, _ := externalOptions(t, h, t.TempDir(), ms)
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Submit(ctx, testInput("source", "Create once despite a lost response"))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 20); !errors.Is(err, taskmodule.ErrOperationUnknown) {
		t.Fatal("lost response became completion", err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	opts.Profile.Revision = "future-admission-v2"
	e, err = openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Retry(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if h.executes != 1 || h.lookups < 2 || len(h.tasks) != 1 {
		t.Fatal("unknown retried under another operation identity")
	}
	assertNoSDKBusiness(t, e)
}

func TestWholeTaskManagerRejectsUntrustedLookupAndCurrentRevocation(t *testing.T) {
	for _, test := range []struct{ state, corrupt string }{{"accepted", "hash"}, {"accepted", "scope"}, {"not_started", "hash"}, {"not_started", "scope"}, {"not_started", "scope_zero"}, {"unknown", ""}} {
		t.Run(test.state+test.corrupt, func(t *testing.T) {
			ctx := context.Background()
			h := newIndependentTasks(t)
			h.lookupState = test.state
			h.corrupt = test.corrupt
			opts, _ := externalOptions(t, h, t.TempDir(), map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t))})
			e, err := openDefault(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			_, err = e.Submit(ctx, testInput("source", "Do not accept untrusted lookup"))
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Drain(ctx, 20); err == nil || h.executes != 0 {
				t.Fatal("untrusted lookup allowed an effect", err)
			}
			assertNoSDKBusiness(t, e)
		})
	}
	ctx := context.Background()
	h := newIndependentTasks(t)
	opts, _ := externalOptions(t, h, t.TempDir(), map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t))})
	var revoked atomic.Bool
	opts.AuthorizeScope = func(context.Context, work.Scope) error {
		if revoked.Load() {
			return work.ErrScope
		}
		return nil
	}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	_, err = e.Submit(ctx, testInput("source", "Check current permissions"))
	if err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	if err = e.Drain(ctx, 20); err == nil || h.executes != 0 {
		t.Fatal("revoked source executed", err)
	}
	revoked.Store(false)
	s := e.backend.(*standalone)
	foreign := opts.Profile.Scope("dm")
	foreign.Requester = "another-owner"
	if _, err = s.authority.List(ctx, foreign); !errors.Is(err, work.ErrScope) {
		t.Fatal("cross-owner read succeeded", err)
	}
	h.tasks["foreign-projection"] = work.Task{ID: "foreign-projection", Scope: opts.Profile.Scope("dm")}
	h.foreign = true
	if _, err = e.Tasks(ctx, "dm"); !errors.Is(err, work.ErrScope) {
		t.Fatal("Host's foreign projection reached model", err)
	}
}

func TestWholeTaskManagerRejectsHybridAndIncompleteFactories(t *testing.T) {
	for _, kind := range []string{"executor", "review", "scheduler", "owner", "owner-loop", "delivery-loop", "missing-port"} {
		t.Run(kind, func(t *testing.T) {
			h := newIndependentTasks(t)
			opts, _ := externalOptions(t, h, t.TempDir(), map[string]*looptest.Model{})
			ref := ModuleRef{Name: "local", Version: "1"}
			switch kind {
			case "executor":
				opts.Profile.Modules.Executor = ref
			case "review":
				opts.Profile.Modules.Review = ref
			case "scheduler":
				opts.Profile.Modules.Scheduler = ref
			case "owner":
				opts.Profile.Modules.Owner = ref
			case "owner-loop":
				opts.Profile.OwnerLoops = []string{"memory"}
			case "delivery-loop":
				opts.Profile.DeliveryLoops = []string{"expression"}
			case "missing-port":
				r := NewModuleRegistry()
				_ = r.RegisterTaskManager(opts.Profile.Modules.TaskManagerRef(), func(context.Context, TaskManagerDependencies) (TaskManagerBinding, error) {
					return TaskManagerBinding{HostDriven: true, Closer: h}, nil
				})
				opts.Modules = r
			}
			if _, err := openDefault(context.Background(), opts); err == nil {
				t.Fatal("hybrid or incomplete binding accepted")
			}
		})
	}
}

func TestHostOutboxLookupCannotCarryARevokedGrantIntoAdmission(t *testing.T) {
	for _, kind := range []string{"scope", "source", "delivery"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			h := newIndependentTasks(t)
			opts, _ := externalOptions(t, h, t.TempDir(), map[string]*looptest.Model{"chat": looptest.New(call("reply", "reply", map[string]any{"text": "Do not send after withdrawal", "source_ref": "source"}))})
			var revoked atomic.Bool
			opts.AuthorizeScope = func(context.Context, work.Scope) error {
				if kind == "scope" && revoked.Load() {
					return work.ErrScope
				}
				return nil
			}
			opts.AuthorizeDelivery = func(context.Context, interaction.Intent) error {
				if kind == "delivery" && revoked.Load() {
					return work.ErrScope
				}
				return nil
			}
			e, err := openDefault(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if _, err = e.Submit(ctx, testInput("source", "Reply only while authorized")); err != nil {
				t.Fatal(err)
			}
			h.intentLookupHook = func() {
				if kind == "source" {
					if err := e.backend.(*standalone).context.Revoke(ctx, opts.Profile.Scope("dm"), "source"); err != nil {
						t.Error(err)
					}
				} else {
					revoked.Store(true)
				}
			}
			if err = e.Drain(ctx, 20); err == nil {
				t.Fatal("lookup retained a revoked grant")
			}
			if h.intentEnqueues != 0 || len(h.intents) != 0 {
				t.Fatal("post-lookup withdrawal still admitted a Host Outbox effect")
			}
			assertNoSDKBusiness(t, e)
		})
	}
}

func TestHostFailureReadAndTimingQueryRecheckCurrentAuthority(t *testing.T) {
	ctx := context.Background()
	h := newIndependentTasks(t)
	opts, _ := externalOptions(t, h, t.TempDir(), map[string]*looptest.Model{})
	var revoked atomic.Bool
	opts.AuthorizeScope = func(context.Context, work.Scope) error {
		if revoked.Load() {
			return work.ErrScope
		}
		return nil
	}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	scope := opts.Profile.Scope("dm")
	h.tasks["private-handle"] = work.Task{ID: "private-handle", Scope: scope}
	h.failureHook = func() { revoked.Store(true) }
	items, err := e.Failures(ctx, "dm")
	if !errors.Is(err, work.ErrScope) || len(items) != 0 {
		t.Fatal("failure details returned after query revoked authority", err)
	}
	revoked.Store(false)
	if _, err = e.Submit(ctx, testInput("source", "Watch the actual Task")); err != nil {
		t.Fatal(err)
	}
	h.taskGetHook = func() {
		if err := e.backend.(*standalone).context.Revoke(ctx, scope, "source"); err != nil {
			t.Error(err)
		}
	}
	_, err = e.Schedule(ctx, "dm", work.Schedule{ID: "original-watch", TaskID: "private-handle", GoalRevision: 1, Condition: "changed", Source: work.Source{Receipt: "source", Actor: scope.Requester, Text: "Watch the actual Task"}})
	if err == nil || h.schedules != 0 {
		t.Fatal("Task query withdrawal still reached timing effect", err)
	}
	assertNoSDKBusiness(t, e)
}

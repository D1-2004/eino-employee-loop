// Package memtask is an in-memory contract.TaskManager. It runs every task
// run on its own goroutine through a taskmgr.Executor and reports to the
// Employee Loop only by publishing events to a contract.EventSink (see the
// taskmgr package for the event sequence). Every event carries the task's
// Event.Employee; to serve several loops, pass a contract.SinkResolver.
//
// Semantics:
//   - Every mutation is idempotent by (employee, idempotency key). Replaying
//     a key with the same request returns the stored receipt; reusing it for
//     a different request returns contract.ErrConflict.
//   - Only the requester controls its task, and only from the task's scene
//     (contract.ErrForbidden). Another employee's task is not found.
//   - A control's key is looked up before anything else, so a replay gets
//     its stored receipt even after the first attempt moved the task on.
//     Then a positive ExpectedRevision must equal the task's revision
//     (contract.ErrStaleRevision otherwise).
//   - Steer and Stop need an active task, Continue a done or failed one
//     (contract.ErrTaskState otherwise).
//   - Every state change increments the task revision.
//
// State lives in process memory: tasks, idempotency records and unpublished
// events are lost when the process exits. Use it for tests, demos and
// single-process assemblies.
package memtask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/taskmgr"
)

// Option configures a Manager.
type Option func(*options)

type options struct {
	now            func() time.Time
	queueLimit     int
	backoffBase    time.Duration
	backoffMax     time.Duration
	publishTimeout time.Duration
	onRejected     func(contract.Event, error)
}

// WithClock sets the clock used for timestamps.
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithQueueLimit sets the soft bound of the outbound event queue (default
// 1024; <= 0 means unbounded).
func WithQueueLimit(n int) Option { return func(o *options) { o.queueLimit = n } }

// WithBackoff sets the retry backoff for failed publishes (default 100ms
// doubling up to 30s).
func WithBackoff(base, max time.Duration) Option {
	return func(o *options) { o.backoffBase, o.backoffMax = base, max }
}

// WithPublishTimeout bounds one publish attempt (default 10s).
func WithPublishTimeout(d time.Duration) Option { return func(o *options) { o.publishTimeout = d } }

// WithRejectHandler receives events the sink rejected permanently (invalid
// event, unknown type or scene, idempotency conflict). They are not retried.
func WithRejectHandler(f func(contract.Event, error)) Option {
	return func(o *options) { o.onRejected = f }
}

// Manager is the in-memory Task Manager.
type Manager struct {
	exec taskmgr.Executor
	out  *outbound
	now  func() time.Time

	baseCtx   context.Context
	cancelAll context.CancelFunc
	runs      sync.WaitGroup
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error

	mu           sync.Mutex
	closed       bool
	seq          int64
	tasks        map[string]*task
	dispatchKeys map[string]dispatchRecord
	controlKeys  map[string]controlRecord
}

type dispatchRecord struct{ fingerprint, ref string }

type controlRecord struct {
	fingerprint string
	receipt     contract.TaskReceipt
}

type task struct {
	ref         string
	employee    string
	scene       contract.SceneRef
	requester   contract.Actor
	sourceRef   string
	goal        string
	prompt      string
	locale      string
	status      string
	revision    int64
	report      string
	failure     string
	corrections []string
	created     time.Time
	updated     time.Time
	seq         int64
	runs        int
	lastRun     string
	cur         *run
}

type run struct {
	ref      string
	ctx      context.Context
	cancel   context.CancelFunc
	progress int
}

var _ contract.TaskManager = (*Manager)(nil)

// New returns a running Manager. Call Close to stop it.
func New(exec taskmgr.Executor, sink contract.EventSink, opts ...Option) *Manager {
	o := options{
		now: time.Now, queueLimit: 1024, backoffBase: 100 * time.Millisecond,
		backoffMax: 30 * time.Second, publishTimeout: 10 * time.Second,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.backoffBase <= 0 {
		o.backoffBase = time.Millisecond
	}
	if o.backoffMax < o.backoffBase {
		o.backoffMax = o.backoffBase
	}
	if o.publishTimeout <= 0 {
		o.publishTimeout = 10 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		exec: exec, out: newOutbound(sink, o), now: o.now,
		baseCtx: ctx, cancelAll: cancel, closeDone: make(chan struct{}),
		tasks: map[string]*task{}, dispatchKeys: map[string]dispatchRecord{}, controlKeys: map[string]controlRecord{},
	}
}

// Pending returns the number of events not yet published.
func (m *Manager) Pending() int { return m.out.pending() }

// Flush waits until every queued event was published or rejected.
func (m *Manager) Flush(ctx context.Context) error { return m.out.flush(ctx) }

// Close stops accepting requests, waits for running runs to finish and for
// queued events to be published. When ctx ends first, it cancels the
// remaining runs (their terminal events report reason "shutdown"), waits for
// their goroutines, stops publishing and returns an error naming how many
// events stayed unpublished.
func (m *Manager) Close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		finished := make(chan struct{})
		go func() { m.runs.Wait(); close(finished) }()
		var err error
		select {
		case <-finished:
		case <-ctx.Done():
			m.cancelAll()
			<-finished
			err = ctx.Err()
		}
		if err == nil {
			err = m.out.flush(ctx)
		}
		m.cancelAll()
		left := m.out.pending()
		m.out.shutdown()
		if err != nil {
			m.closeErr = fmt.Errorf("memtask: close left %d unpublished events: %w", left, err)
		}
		close(m.closeDone)
	})
	select {
	case <-m.closeDone:
		return m.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func invalid(field string) error {
	return fmt.Errorf("%w: %s is required", contract.ErrInvalidRequest, field)
}

func (m *Manager) waitRoom(ctx context.Context) error {
	if err := m.out.waitRoom(ctx); err != nil {
		if err == errStopped {
			return contract.ErrUnavailable
		}
		return err
	}
	return nil
}

// Dispatch implements contract.TaskManager.
func (m *Manager) Dispatch(ctx context.Context, req contract.DispatchRequest) (contract.TaskReceipt, error) {
	switch {
	case strings.TrimSpace(req.IdempotencyKey) == "":
		return contract.TaskReceipt{}, invalid("idempotency_key")
	case strings.TrimSpace(req.Employee) == "":
		return contract.TaskReceipt{}, invalid("employee")
	case strings.TrimSpace(req.Scene.ID) == "":
		return contract.TaskReceipt{}, invalid("scene.id")
	case strings.TrimSpace(req.Requester.ID) == "":
		return contract.TaskReceipt{}, invalid("requester.id")
	case strings.TrimSpace(req.Goal) == "" && strings.TrimSpace(req.Prompt) == "":
		return contract.TaskReceipt{}, invalid("goal")
	}
	fp := fingerprint("dispatch", req)
	if err := m.waitRoom(ctx); err != nil {
		return contract.TaskReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return contract.TaskReceipt{}, contract.ErrUnavailable
	}
	key := req.Employee + "\x00" + req.IdempotencyKey
	if rec, ok := m.dispatchKeys[key]; ok {
		if rec.fingerprint != fp {
			return contract.TaskReceipt{}, fmt.Errorf("%w: idempotency key %s", contract.ErrConflict, req.IdempotencyKey)
		}
		return receipt(m.tasks[rec.ref], false), nil
	}
	now := m.now()
	scene := req.Scene
	if scene.Tenant == "" {
		scene.Tenant = req.Tenant
	}
	m.seq++
	t := &task{
		ref: taskRef(key), employee: req.Employee, scene: scene, requester: req.Requester,
		sourceRef: req.SourceRef, goal: req.Goal, prompt: req.Prompt, locale: req.Locale,
		status: contract.TaskQueued, revision: 1, created: now, updated: now, seq: m.seq,
	}
	m.tasks[t.ref] = t
	m.dispatchKeys[key] = dispatchRecord{fingerprint: fp, ref: t.ref}
	r := m.newRunLocked(t)
	m.out.push(m.eventLocked(t, r, "accepted", taskmgr.EventAccepted, taskmgr.RunAccepted, "", 0, ""))
	m.startLocked(t, r)
	return receipt(t, true), nil
}

// Get implements contract.TaskManager.
func (m *Manager) Get(_ context.Context, employee, ref string) (contract.TaskView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[ref]
	if t == nil || t.employee != employee {
		return contract.TaskView{}, fmt.Errorf("%w: task %s", contract.ErrNotFound, ref)
	}
	return view(t), nil
}

// Find implements contract.TaskManager. Text is a case-insensitive
// substring of the goal; Limit defaults to 20 and is capped at 200.
func (m *Manager) Find(_ context.Context, q contract.TaskQuery) ([]contract.TaskView, error) {
	if strings.TrimSpace(q.Employee) == "" {
		return nil, invalid("employee")
	}
	if strings.TrimSpace(q.Scene) == "" {
		return nil, invalid("scene")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	text := strings.ToLower(q.Text)
	m.mu.Lock()
	matched := make([]*task, 0)
	for _, t := range m.tasks {
		if t.employee != q.Employee || t.scene.ID != q.Scene {
			continue
		}
		if q.Requester != "" && t.requester.ID != q.Requester {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(t.goal), text) {
			continue
		}
		matched = append(matched, t)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].seq > matched[j].seq })
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out := make([]contract.TaskView, len(matched))
	for i, t := range matched {
		out[i] = view(t)
	}
	m.mu.Unlock()
	return out, nil
}

// Steer implements contract.TaskManager: it supersedes the current run with
// a new one that receives every correction so far.
func (m *Manager) Steer(ctx context.Context, c contract.TaskControl) (contract.TaskReceipt, error) {
	if strings.TrimSpace(c.Text) == "" {
		return contract.TaskReceipt{}, invalid("text")
	}
	return m.control(ctx, "steer", c, func(t *task) error {
		if !contract.TaskActive(t.status) {
			return fmt.Errorf("%w: task %s is %s", contract.ErrTaskState, t.ref, t.status)
		}
		old := t.cur
		old.cancel()
		t.corrections = append(t.corrections, c.Text)
		m.setStatusLocked(t, t.status)
		m.out.push(m.eventLocked(t, old, "terminal", taskmgr.EventTerminal, taskmgr.RunSuperseded, taskmgr.ReasonSteered, 0, ""))
		r := m.newRunLocked(t)
		m.out.push(
			m.eventLocked(t, r, "steered", taskmgr.EventSteered, taskmgr.RunAccepted, taskmgr.ReasonSteered, 0, c.Text),
			m.eventLocked(t, r, "accepted", taskmgr.EventAccepted, taskmgr.RunAccepted, taskmgr.ReasonSteered, 0, ""),
		)
		m.startLocked(t, r)
		return nil
	})
}

// Stop implements contract.TaskManager: it cancels the current run and
// marks the task cancelled.
func (m *Manager) Stop(ctx context.Context, c contract.TaskControl) (contract.TaskReceipt, error) {
	return m.control(ctx, "stop", c, func(t *task) error {
		if !contract.TaskActive(t.status) {
			return fmt.Errorf("%w: task %s is %s", contract.ErrTaskState, t.ref, t.status)
		}
		old := t.cur
		old.cancel()
		t.cur = nil
		m.setStatusLocked(t, contract.TaskCancelled)
		m.out.push(m.eventLocked(t, old, "terminal", taskmgr.EventTerminal, taskmgr.RunCancelled, taskmgr.ReasonStopped, 0, ""))
		return nil
	})
}

// Continue implements contract.TaskManager: it starts a new run on a done
// or failed task. A non-empty Text replaces the prompt.
func (m *Manager) Continue(ctx context.Context, c contract.TaskControl) (contract.TaskReceipt, error) {
	return m.control(ctx, "continue", c, func(t *task) error {
		if t.status != contract.TaskDone && t.status != contract.TaskFailed {
			return fmt.Errorf("%w: task %s is %s", contract.ErrTaskState, t.ref, t.status)
		}
		if strings.TrimSpace(c.Text) != "" {
			t.prompt = c.Text
		}
		t.report, t.failure = "", ""
		m.setStatusLocked(t, contract.TaskQueued)
		r := m.newRunLocked(t)
		m.out.push(m.eventLocked(t, r, "accepted", taskmgr.EventAccepted, taskmgr.RunAccepted, taskmgr.ReasonContinued, 0, c.Text))
		m.startLocked(t, r)
		return nil
	})
}

func (m *Manager) control(ctx context.Context, op string, c contract.TaskControl, apply func(*task) error) (contract.TaskReceipt, error) {
	switch {
	case strings.TrimSpace(c.IdempotencyKey) == "":
		return contract.TaskReceipt{}, invalid("idempotency_key")
	case strings.TrimSpace(c.Employee) == "":
		return contract.TaskReceipt{}, invalid("employee")
	case strings.TrimSpace(c.TaskRef) == "":
		return contract.TaskReceipt{}, invalid("task_ref")
	case strings.TrimSpace(c.Scene) == "":
		return contract.TaskReceipt{}, invalid("scene")
	case strings.TrimSpace(c.Requester.ID) == "":
		return contract.TaskReceipt{}, invalid("requester.id")
	}
	fp := fingerprint(op, c)
	if err := m.waitRoom(ctx); err != nil {
		return contract.TaskReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return contract.TaskReceipt{}, contract.ErrUnavailable
	}
	// A known key answers first: the control may have been applied and
	// moved the task on, so its revision and state no longer show the
	// request's preconditions. The fingerprint covers task, scene and
	// requester.
	key := c.Employee + "\x00" + c.IdempotencyKey
	if rec, ok := m.controlKeys[key]; ok {
		if rec.fingerprint != fp {
			return contract.TaskReceipt{}, fmt.Errorf("%w: idempotency key %s", contract.ErrConflict, c.IdempotencyKey)
		}
		return rec.receipt, nil
	}
	t := m.tasks[c.TaskRef]
	if t == nil || t.employee != c.Employee {
		return contract.TaskReceipt{}, fmt.Errorf("%w: task %s", contract.ErrNotFound, c.TaskRef)
	}
	if t.scene.ID != c.Scene || t.requester.ID != c.Requester.ID {
		return contract.TaskReceipt{}, fmt.Errorf("%w: task %s", contract.ErrForbidden, c.TaskRef)
	}
	if c.ExpectedRevision > 0 && c.ExpectedRevision != t.revision {
		return contract.TaskReceipt{}, fmt.Errorf("%w: task %s is at revision %d, not %d", contract.ErrStaleRevision, t.ref, t.revision, c.ExpectedRevision)
	}
	if err := apply(t); err != nil {
		return contract.TaskReceipt{}, err
	}
	rc := receipt(t, false)
	m.controlKeys[key] = controlRecord{fingerprint: fp, receipt: rc}
	return rc, nil
}

// newRunLocked makes a new current run of t.
func (m *Manager) newRunLocked(t *task) *run {
	t.runs++
	ctx, cancel := context.WithCancel(m.baseCtx)
	r := &run{ref: fmt.Sprintf("%s.r%d", t.ref, t.runs), ctx: ctx, cancel: cancel}
	t.cur, t.lastRun = r, r.ref
	return r
}

func (m *Manager) startLocked(t *task, r *run) {
	job := taskmgr.Job{
		TaskRef: t.ref, RunRef: r.ref, Employee: t.employee, Scene: t.scene.ID,
		Goal: t.goal, Prompt: t.prompt, Locale: t.locale,
		Corrections: append([]string(nil), t.corrections...),
	}
	m.runs.Add(1)
	go m.execute(t, r, job)
}

func (m *Manager) setStatusLocked(t *task, status string) {
	t.status = status
	t.revision++
	t.updated = m.now()
}

func (m *Manager) execute(t *task, r *run, job taskmgr.Job) {
	defer m.runs.Done()
	defer r.cancel()
	m.mu.Lock()
	if t.cur != r {
		m.mu.Unlock()
		return
	}
	m.setStatusLocked(t, contract.TaskRunning)
	m.out.push(m.eventLocked(t, r, "started", taskmgr.EventStarted, taskmgr.RunRunning, "", 0, ""))
	m.mu.Unlock()

	report, err := m.safeExecute(r.ctx, job, &reporter{m: m, t: t, r: r})

	m.mu.Lock()
	defer m.mu.Unlock()
	if t.cur != r {
		// Stopped or superseded: the control already reported the run's end.
		return
	}
	t.cur = nil
	switch {
	case r.ctx.Err() != nil:
		// Only Close cancels a current run.
		m.setStatusLocked(t, contract.TaskCancelled)
		m.out.push(m.eventLocked(t, r, "terminal", taskmgr.EventTerminal, taskmgr.RunCancelled, taskmgr.ReasonShutdown, 0, ""))
	case err != nil:
		t.failure = err.Error()
		m.setStatusLocked(t, contract.TaskFailed)
		m.out.push(
			m.eventLocked(t, r, "result", taskmgr.EventResult, taskmgr.RunFailed, taskmgr.ReasonFailed, 0, t.failure),
			m.eventLocked(t, r, "terminal", taskmgr.EventTerminal, taskmgr.RunFailed, taskmgr.ReasonFailed, 0, ""),
		)
	default:
		t.report = report
		m.setStatusLocked(t, contract.TaskDone)
		m.out.push(
			m.eventLocked(t, r, "result", taskmgr.EventResult, taskmgr.RunDone, taskmgr.ReasonCompleted, 0, report),
			m.eventLocked(t, r, "terminal", taskmgr.EventTerminal, taskmgr.RunDone, taskmgr.ReasonCompleted, 0, ""),
		)
	}
}

func (m *Manager) safeExecute(ctx context.Context, job taskmgr.Job, rep taskmgr.Reporter) (report string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("memtask: executor panic: %v", p)
		}
	}()
	return m.exec.Execute(ctx, job, rep)
}

type reporter struct {
	m *Manager
	t *task
	r *run
}

// Progress implements taskmgr.Reporter.
func (rp *reporter) Progress(ctx context.Context, text string) error {
	if err := rp.m.waitRoom(ctx); err != nil {
		return err
	}
	m := rp.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if rp.t.cur != rp.r || rp.r.ctx.Err() != nil {
		return fmt.Errorf("%w: run %s ended", contract.ErrTaskState, rp.r.ref)
	}
	rp.r.progress++
	kind := fmt.Sprintf("progress-%d", rp.r.progress)
	m.out.push(m.eventLocked(rp.t, rp.r, kind, taskmgr.EventProgress, taskmgr.RunRunning, "", rp.r.progress, text))
	return nil
}

func (m *Manager) eventLocked(t *task, r *run, kind, typ, runStatus, reason string, seq int, text string) contract.Event {
	payload, _ := json.Marshal(taskmgr.ExecutionPayload{
		TaskStatus: t.status, RunStatus: runStatus, Reason: reason, Revision: t.revision, Seq: seq,
	})
	return contract.Event{
		ID:            t.ref + "/" + r.ref + "/" + kind,
		Source:        taskmgr.Source,
		Employee:      t.employee,
		Type:          typ,
		Scene:         t.scene,
		Actor:         contract.Actor{ID: taskmgr.ActorID, Kind: contract.ActorSystem},
		OccurredAt:    m.now(),
		Locale:        t.locale,
		Refs:          map[string]string{contract.RefTask: t.ref, contract.RefRun: r.ref},
		Text:          text,
		PayloadSchema: taskmgr.PayloadSchema,
		Payload:       payload,
	}
}

func receipt(t *task, created bool) contract.TaskReceipt {
	return contract.TaskReceipt{TaskRef: t.ref, RunRef: t.lastRun, Status: t.status, Created: created, Revision: t.revision}
}

func view(t *task) contract.TaskView {
	return contract.TaskView{
		TaskRef: t.ref, Employee: t.employee, Scene: t.scene.ID, Requester: t.requester.ID,
		SourceRef: t.sourceRef, Goal: t.goal, Status: t.status, Revision: t.revision,
		Report: t.report, Failure: t.failure, CreatedAt: t.created, UpdatedAt: t.updated,
	}
}

func taskRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "task-" + hex.EncodeToString(sum[:10])
}

func fingerprint(op string, v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(append([]byte(op+"\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

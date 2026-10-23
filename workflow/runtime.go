package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/loop"
	"github.com/cloudwego/eino/compose"
)

// Registry registers immutable kind/version bindings. Duplicate registration is
// an error; replacing code under a version would invalidate frozen sessions.
type Registry struct {
	mu        sync.RWMutex
	providers map[[2]string]Provider
}

func (r *Registry) Register(kind, version string, p Provider) error {
	if strings.TrimSpace(kind) == "" || strings.TrimSpace(version) == "" || p == nil {
		return ErrVersion
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[[2]string]Provider)
	}
	k := [2]string{kind, version}
	if _, exists := r.providers[k]; exists {
		return fmt.Errorf("%w: duplicate %s/%s", ErrVersion, kind, version)
	}
	r.providers[k] = p
	return nil
}

func (r *Registry) lookup(kind, version string) (Provider, error) {
	if r == nil {
		return nil, ErrVersion
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.providers[[2]string{kind, version}]
	if p == nil {
		return nil, fmt.Errorf("%w: %s/%s", ErrVersion, kind, version)
	}
	return p, nil
}

// Runtime has no default admission path: a non-nil Gate is mandatory.
type Runtime struct {
	Registry *Registry
	Store    Store
	Gate     Gate
}

type execution struct {
	request  Request
	provider Provider
	lease    Lease
	snapshot Snapshot
	prepared Prepared
	result   Result
	err      error
	index    int
	resume   bool
}

// Run serializes one session, replays completed receipts and resumes a generated
// result at commit. Generation errors preserve their partial effect outcomes.
func (r *Runtime) Run(ctx context.Context, request Request) (result Result, err error) {
	if err = validateRequest(request); err != nil {
		return result, err
	}
	// Freeze caller-owned maps/slices before asynchronous Eino nodes use them.
	request, err = detached(request)
	if err != nil {
		return result, err
	}
	if r.Gate == nil {
		return result, ErrDenied
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = r.Gate(ctx, request); err != nil {
		return result, errors.Join(ErrDenied, err)
	}
	p, err := r.Registry.lookup(request.Kind, request.Version)
	if err != nil {
		return result, err
	}
	if err = p.Authorize(ctx, request); err != nil {
		return result, errors.Join(ErrDenied, err)
	}
	if r.Store == nil {
		return result, errors.New("workflow: missing session store")
	}
	lease, err := r.Store.Acquire(ctx, request.Key)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Release()) }()
	s, err := lease.Load(ctx)
	if err != nil {
		return result, err
	}
	if s.Revision == 0 && len(s.Records) == 0 {
		s.Key, s.Kind, s.Version = request.Key, request.Kind, request.Version
	}
	if s.Key != request.Key || s.Kind != request.Kind || s.Version != request.Version {
		return result, ErrConflict
	}
	if err = validateSnapshot(s); err != nil {
		return result, err
	}
	e := &execution{request: request, provider: p, lease: lease, snapshot: s, index: len(s.Records)}
	for i, record := range s.Records {
		if record.Request.Receipt != request.Receipt {
			continue
		}
		if !same(record.Request, request) {
			return result, ErrConflict
		}
		e.index = i
		e.result = Result{Outcome: record.Outcome, Commit: record.Commit, Phase: record.Phase, Replay: true}
		switch record.Phase {
		case Completed:
			return e.result, nil
		case Failed:
			return e.result, restoredFailure(record)
		case Generated:
			e.resume = true
		default:
			return result, errors.New("workflow: corrupt session phase")
		}
		break
	}
	// A later wake must not overtake an unknown business commit.
	for i, record := range s.Records {
		if record.Phase == Generated && (!e.resume || i != e.index) {
			return result, ErrBusy
		}
	}
	g := compose.NewGraph[*execution, *execution]()
	nodes := []struct {
		name string
		fn   func(context.Context, *execution) (*execution, error)
	}{
		{"prepare", prepare}, {"agent", generate}, {"checkpoint", checkpoint}, {"commit", commit},
	}
	previous := compose.START
	for _, node := range nodes {
		if err = g.AddLambdaNode(node.name, compose.InvokableLambda(func(_ context.Context, state *execution) (*execution, error) {
			return node.fn(ctx, state)
		})); err != nil {
			return result, err
		}
		if err = g.AddEdge(previous, node.name); err != nil {
			return result, err
		}
		previous = node.name
	}
	if err = g.AddEdge(previous, compose.END); err != nil {
		return result, err
	}
	graph, err := g.Compile(ctx)
	if err != nil {
		return result, err
	}
	// Nodes keep the request context. Only graph scheduling survives cancellation
	// long enough to checkpoint a partial outcome before returning its error.
	_, graphErr := graph.Invoke(context.WithoutCancel(ctx), e)
	return e.result, errors.Join(e.err, graphErr)
}

func prepare(ctx context.Context, e *execution) (*execution, error) {
	if e.resume {
		return e, nil
	}
	snapshot, err := detached(e.snapshot)
	if err != nil {
		return e, err
	}
	e.prepared, err = e.provider.Prepare(ctx, e.request, snapshot)
	if err != nil {
		return e, err
	}
	if len(e.prepared.Messages) == 0 {
		return e, errors.New("workflow: Provider returned no messages")
	}
	return e, nil
}

func generate(ctx context.Context, e *execution) (*execution, error) {
	if e.resume {
		return e, nil
	}
	k := e.request.Key
	e.result.Outcome, e.err = loop.New(e.prepared.Config, e.prepared.Model, e.prepared.Host).Run(ctx, loop.Wake{
		Identity: loop.Identity{Tenant: k.Tenant, Employee: k.Employee, Scene: k.Scene, Receipt: e.request.Receipt},
		Messages: e.prepared.Messages,
	})
	// Persist only this wake's generated suffix; earlier context is owned by
	// its own receipt. Raw prepared snapshots belong in the model journal.
	if n := len(e.prepared.Messages); len(e.result.Outcome.Transcript) >= n {
		e.result.Outcome.Transcript = e.result.Outcome.Transcript[n:]
	}
	e.result.Phase = Generated
	if e.err != nil {
		e.result.Phase = Failed
	}
	return e, nil
}

func checkpoint(ctx context.Context, e *execution) (*execution, error) {
	if e.resume {
		return e, nil
	}
	record := Record{Request: e.request, Phase: e.result.Phase, Outcome: e.result.Outcome}
	if e.err != nil {
		record.Failure = e.err.Error()
		for kind, sentinel := range failureSentinels {
			if errors.Is(e.err, sentinel) {
				record.FailureKinds = append(record.FailureKinds, kind)
			}
		}
		sort.Strings(record.FailureKinds)
	}
	e.snapshot.Records = append(e.snapshot.Records, record)
	// Preserve known effects even when cancellation ended the agent. This is
	// a bounded storage write only; no business action runs after cancellation.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := e.lease.Save(saveCtx, e.snapshot); err != nil {
		return e, err
	}
	e.snapshot.Revision++
	return e, nil
}

func commit(ctx context.Context, e *execution) (*execution, error) {
	if e.err != nil {
		return e, nil
	}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	if err := e.provider.Authorize(ctx, e.request); err != nil {
		return e, errors.Join(ErrDenied, err)
	}
	receipt, err := e.provider.Commit(ctx, e.request, e.result.Outcome)
	if err != nil {
		return e, err
	}
	if strings.TrimSpace(receipt) == "" {
		return e, errors.New("workflow: commit has no durable receipt")
	}
	record := &e.snapshot.Records[e.index]
	record.Phase, record.Commit = Completed, receipt
	// A cancelled request can still have committed. Record that proof using a
	// bounded independent context; replay will verify current access again.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err = e.lease.Save(saveCtx, e.snapshot); err != nil {
		return e, err
	}
	e.result.Phase, e.result.Commit = Completed, receipt
	return e, nil
}

func validateRequest(r Request) error {
	for _, s := range []string{r.Key.Workspace, r.Key.Tenant, r.Key.Employee, r.Key.Scene, r.Key.Lane, r.Key.Subject, r.Receipt, r.Kind, r.Version} {
		if strings.TrimSpace(s) == "" {
			return errors.New("workflow: incomplete trusted identity or version")
		}
	}
	if len(r.Messages) == 0 {
		return errors.New("workflow: missing input messages")
	}
	for _, m := range r.Messages {
		if m == nil || m.Role != "user" || len(m.ToolCalls) != 0 || m.ToolCallID != "" {
			return errors.New("workflow: input messages must be user data")
		}
	}
	if len(r.Data) > 0 && !json.Valid(r.Data) {
		return errors.New("workflow: invalid frozen data")
	}
	return nil
}

func validateSnapshot(s Snapshot) error {
	seen := map[string]bool{}
	for _, record := range s.Records {
		r := record.Request
		if r.Key != s.Key || r.Kind != s.Kind || r.Version != s.Version || seen[r.Receipt] || validateRequest(r) != nil {
			return ErrConflict
		}
		seen[r.Receipt] = true
		switch record.Phase {
		case Generated:
			if record.Failure != "" || record.Commit != "" {
				return ErrConflict
			}
		case Failed:
			if record.Failure == "" || record.Commit != "" {
				return ErrConflict
			}
		case Completed:
			if record.Failure != "" || strings.TrimSpace(record.Commit) == "" {
				return ErrConflict
			}
		default:
			return ErrConflict
		}
	}
	return nil
}

func detached[T any](value T) (T, error) {
	var out T
	raw, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

func same(a, b Request) bool {
	x, errX := json.Marshal(a)
	y, errY := json.Marshal(b)
	return errX == nil && errY == nil && string(x) == string(y)
}

var failureSentinels = map[string]error{
	"cancelled":          context.Canceled,
	"deadline":           context.DeadlineExceeded,
	"budget":             loop.ErrModelBudget,
	"consecutive_errors": loop.ErrConsecutiveModelErrors,
	"responses":          loop.ErrModelResponseBudget,
	"attempts":           loop.ErrPhysicalModelBudget,
	"missing_receipt":    loop.ErrMissingReceipt,
	"terminal_conflict":  loop.ErrTerminalConflict,
	"tool_failed":        loop.ErrToolFailed,
}

type storedFailure struct {
	text  string
	kinds []error
}

func (e storedFailure) Error() string   { return e.text }
func (e storedFailure) Unwrap() []error { return e.kinds }
func restoredFailure(r Record) error {
	e := storedFailure{text: r.Failure}
	for _, kind := range r.FailureKinds {
		if sentinel := failureSentinels[kind]; sentinel != nil {
			e.kinds = append(e.kinds, sentinel)
		}
	}
	return e
}

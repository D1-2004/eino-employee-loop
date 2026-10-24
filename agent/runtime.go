// Package agent exposes the native Eino Runner lifecycle for new entries.
// Unlike the compatibility loop, it preserves standard handlers, callbacks,
// events and resumable interrupts. Business journals remain Host-owned.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/D1-2004/eino-employee-loop/workflow"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

var (
	ErrDenied         = errors.New("agent: admission denied")
	ErrResumeRequired = errors.New("agent: checkpoint exists; targeted resume required")
)

type Request struct {
	Key      workflow.Key
	Receipt  string
	Messages []*schema.Message
}
type Factory func(context.Context, Request) (*adk.ChatModelAgentConfig, error)
type Stores func(context.Context, Request, string) (adk.CheckPointStore, error)
type Config struct {
	Name, Version string
	Authorize     func(context.Context, Request) error
	Factory       Factory
	Stores        Stores
	Callbacks     []callbacks.Handler
	// Observe receives private framework events, never a public delivery grant.
	// Errors stop execution through Eino cancellation, retaining a checkpoint.
	Observe   func(context.Context, Request, Event) error
	Streaming bool
}
type Runtime struct{ config Config }
type Interrupt struct {
	ID   string
	Info any
}
type Event struct {
	Agent      string
	Message    *schema.Message
	Interrupts []Interrupt
	Error      string
}
type Result struct {
	Events               []Event
	Output               *schema.Message
	Interrupts           []Interrupt
	Completed, Cancelled bool
}

func New(cfg Config) (*Runtime, error) {
	if strings.TrimSpace(cfg.Name) == "" || strings.TrimSpace(cfg.Version) == "" || cfg.Authorize == nil || cfg.Factory == nil || cfg.Stores == nil {
		return nil, errors.New("agent: name/version, admission, factory and scoped checkpoint store required")
	}
	cfg.Callbacks = append([]callbacks.Handler(nil), cfg.Callbacks...)
	return &Runtime{config: cfg}, nil
}
func (r *Runtime) prepare(ctx context.Context, req Request) (*adk.Runner, adk.CheckPointStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	for _, s := range []string{req.Key.Workspace, req.Key.Tenant, req.Key.Employee, req.Key.Scene, req.Key.Lane, req.Key.Subject, req.Receipt} {
		if strings.TrimSpace(s) == "" {
			return nil, nil, ErrDenied
		}
	}
	if err := r.config.Authorize(ctx, req); err != nil {
		return nil, nil, errors.Join(ErrDenied, err)
	}
	store, err := r.config.Stores(ctx, req, r.config.Version)
	if err != nil {
		return nil, nil, err
	}
	if store == nil {
		return nil, nil, errors.New("agent: scoped store unavailable")
	}
	cfg, err := r.config.Factory(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil || cfg.Model == nil || cfg.Name != r.config.Name || cfg.MaxIterations <= 0 {
		return nil, nil, errors.New("agent: factory must preserve agent name and bound iterations")
	}
	a, err := adk.NewChatModelAgent(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return adk.NewRunner(ctx, adk.RunnerConfig{Agent: a, CheckPointStore: store, EnableStreaming: r.config.Streaming}), store, nil
}
func (r *Runtime) Run(ctx context.Context, req Request) (Result, error) {
	if len(req.Messages) == 0 {
		return Result{}, errors.New("agent: input messages required")
	}
	for _, m := range req.Messages {
		if m == nil {
			return Result{}, errors.New("agent: nil input message")
		}
	}
	runner, store, err := r.prepare(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if _, exists, e := store.Get(ctx, req.Receipt); e != nil {
		return Result{}, e
	} else if exists {
		return Result{}, ErrResumeRequired
	}
	return r.execute(ctx, req, runner, nil)
}

// Resume targets exact interrupt IDs. The Host must authorize the current
// answer and bind it to the original scoped receipt before this call.
func (r *Runtime) Resume(ctx context.Context, req Request, targets map[string]any) (Result, error) {
	if len(targets) == 0 {
		return Result{}, errors.New("agent: explicit resume targets required")
	}
	for id := range targets {
		if strings.TrimSpace(id) == "" {
			return Result{}, errors.New("agent: empty resume target")
		}
	}
	runner, _, err := r.prepare(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return r.execute(ctx, req, runner, targets)
}
func (r *Runtime) execute(ctx context.Context, req Request, runner *adk.Runner, targets map[string]any) (Result, error) {
	cancelOption, cancelAgent := adk.WithCancel()
	// Let Eino own cancellation/checkpointing, instead of aborting its persistence
	// context when the entry shuts down. Tools still receive Eino's cancel context.
	stop := context.AfterFunc(ctx, func() { cancelAgent(adk.WithAgentCancelMode(adk.CancelImmediate), adk.WithRecursive()) })
	defer stop()
	runCtx := context.WithoutCancel(ctx)
	options := []adk.AgentRunOption{cancelOption, adk.WithCheckPointID(req.Receipt), adk.WithSessionValues(map[string]any{"eel_workspace": req.Key.Workspace, "eel_tenant": req.Key.Tenant, "eel_employee": req.Key.Employee, "eel_scene": req.Key.Scene, "eel_lane": req.Key.Lane, "eel_subject": req.Key.Subject, "eel_receipt": req.Receipt, "eel_version": r.config.Version})}
	if len(r.config.Callbacks) > 0 {
		options = append(options, adk.WithCallbacks(r.config.Callbacks...))
	}
	var iter *adk.AsyncIterator[*adk.AgentEvent]
	if targets == nil {
		iter = runner.Run(runCtx, req.Messages, options...)
	} else {
		var err error
		iter, err = runner.ResumeWithParams(runCtx, req.Receipt, &adk.ResumeParams{Targets: targets}, options...)
		if err != nil {
			return Result{}, err
		}
	}
	var result Result
	var runErr error
	for event, ok := iter.Next(); ok; event, ok = iter.Next() {
		item := Event{Agent: event.AgentName}
		if event.Err != nil {
			item.Error = event.Err.Error()
			runErr = errors.Join(runErr, event.Err)
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			var err error
			item.Message, err = event.Output.MessageOutput.GetMessage()
			if err != nil {
				runErr = errors.Join(runErr, err)
			}
			if item.Message != nil && event.AgentName == r.config.Name {
				result.Output = item.Message
			}
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, i := range event.Action.Interrupted.InterruptContexts {
				if i.IsRootCause {
					item.Interrupts = append(item.Interrupts, Interrupt{ID: i.ID, Info: i.Info})
				}
			}
			result.Interrupts = append(result.Interrupts, item.Interrupts...)
		}
		result.Events = append(result.Events, item)
		if r.config.Observe != nil {
			if err := r.config.Observe(ctx, req, item); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("agent: event journal: %w", err))
				cancelAgent(adk.WithAgentCancelMode(adk.CancelImmediate), adk.WithRecursive())
			}
		}
	}
	var cancelled *adk.CancelError
	result.Cancelled = ctx.Err() != nil || errors.As(runErr, &cancelled)
	runErr = errors.Join(runErr, ctx.Err())
	result.Completed = runErr == nil && len(result.Interrupts) == 0 && result.Output != nil
	if runErr == nil && len(result.Interrupts) == 0 && !result.Completed {
		return result, errors.New("agent: execution ended without output or interrupt")
	}
	return result, runErr
}

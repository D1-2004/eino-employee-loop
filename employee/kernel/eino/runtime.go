// Package agent exposes the native Eino Runner lifecycle for new entries.
// Unlike the compatibility loop, it preserves standard handlers, callbacks,
// events and resumable interrupts. Business journals remain Host-owned.
package eino

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"

	"github.com/D1-2004/eino-employee-loop/employee/kernel/workflow"
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
	Extensions    []ExtensionFactory
	// ToolsReady receives the final code-registered tool schemas before any model I/O.
	ToolsReady func(context.Context, Request, []*schema.ToolInfo) error
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
	cfg.Extensions = append([]ExtensionFactory(nil), cfg.Extensions...)
	for _, factory := range cfg.Extensions {
		if factory == nil {
			return nil, errors.New("agent: nil extension factory")
		}
	}
	return &Runtime{config: cfg}, nil
}
func (r *Runtime) prepare(ctx context.Context, req Request, ioCtx context.Context) (*adk.Runner, adk.CheckPointStore, []callbacks.Handler, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	for _, s := range []string{req.Key.Workspace, req.Key.Tenant, req.Key.Employee, req.Key.Scene, req.Key.Lane, req.Key.Subject, req.Receipt} {
		if strings.TrimSpace(s) == "" {
			return nil, nil, nil, ErrDenied
		}
	}
	if err := r.config.Authorize(ctx, req); err != nil {
		return nil, nil, nil, errors.Join(ErrDenied, err)
	}
	store, err := r.config.Stores(ctx, req, r.config.Version)
	if err != nil {
		return nil, nil, nil, err
	}
	if store == nil {
		return nil, nil, nil, errors.New("agent: scoped store unavailable")
	}
	cfg, err := r.config.Factory(ctx, req)
	if err != nil {
		return nil, nil, nil, err
	}
	if cfg == nil || cfg.Model == nil || cfg.Name != r.config.Name || cfg.MaxIterations <= 0 {
		return nil, nil, nil, errors.New("agent: factory must preserve agent name and bound iterations")
	}
	cfg, handlers, err := r.extend(ctx, req, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	// Native immediate cancellation checkpoints the graph, but Eino Generate
	// delegates without cancelling the underlying I/O. Keep that I/O attached
	// to the request through its standard middleware boundary.
	cfg.Handlers = append([]adk.ChatModelAgentMiddleware{&requestCancellation{request: ioCtx}}, cfg.Handlers...)
	a, err := adk.NewChatModelAgent(ctx, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	return adk.NewRunner(ctx, adk.RunnerConfig{Agent: a, CheckPointStore: store, EnableStreaming: r.config.Streaming}), store, handlers, nil
}
func (r *Runtime) Run(ctx context.Context, req Request) (Result, error) {
	ioCtx, cancelIO := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelIO()
	if len(req.Messages) == 0 {
		return Result{}, errors.New("agent: input messages required")
	}
	for _, m := range req.Messages {
		if m == nil {
			return Result{}, errors.New("agent: nil input message")
		}
	}
	runner, store, handlers, err := r.prepare(ctx, req, ioCtx)
	if err != nil {
		return Result{}, err
	}
	if _, exists, e := store.Get(ctx, req.Receipt); e != nil {
		return Result{}, e
	} else if exists {
		return Result{}, ErrResumeRequired
	}
	return r.execute(ctx, req, runner, handlers, nil, false, cancelIO)
}

// Resume targets exact interrupt IDs. The Host must authorize the current
// answer and bind it to the original scoped receipt before this call.
func (r *Runtime) Resume(ctx context.Context, req Request, targets map[string]any) (Result, error) {
	ioCtx, cancelIO := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelIO()
	if len(targets) == 0 {
		return Result{}, errors.New("agent: explicit resume targets required")
	}
	for id := range targets {
		if strings.TrimSpace(id) == "" {
			return Result{}, errors.New("agent: empty resume target")
		}
	}
	runner, _, handlers, err := r.prepare(ctx, req, ioCtx)
	if err != nil {
		return Result{}, err
	}
	return r.execute(ctx, req, runner, handlers, targets, true, cancelIO)
}

// Recover resumes an engine cancellation checkpoint without fabricating an
// answer. Business interrupts re-interrupt until explicit targets are supplied.
func (r *Runtime) Recover(ctx context.Context, req Request) (Result, error) {
	ioCtx, cancelIO := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelIO()
	runner, _, handlers, err := r.prepare(ctx, req, ioCtx)
	if err != nil {
		return Result{}, err
	}
	return r.execute(ctx, req, runner, handlers, nil, true, cancelIO)
}
func (r *Runtime) execute(ctx context.Context, req Request, runner *adk.Runner, handlers []callbacks.Handler, targets map[string]any, resume bool, cancelIO context.CancelFunc) (Result, error) {
	cancelOption, cancelAgent := adk.WithCancel()
	// Effects stop on the original request. Eino gets a separate context so its
	// checkpoint can settle after cancellation, bounded to five seconds.
	runCtx, cancelSettlement := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelSettlement()
	settled := make(chan struct{})
	defer close(settled)
	var stopOnce sync.Once
	stopExecution := func() {
		stopOnce.Do(func() {
			cancelAgent(adk.WithAgentCancelMode(adk.CancelImmediate), adk.WithRecursive())
			cancelIO()
			go func() {
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case <-timer.C:
					cancelSettlement()
				case <-settled:
				}
			}()
		})
	}
	stop := context.AfterFunc(ctx, stopExecution)
	defer stop()
	options := []adk.AgentRunOption{cancelOption, adk.WithCheckPointID(req.Receipt), adk.WithSessionValues(map[string]any{"eel_workspace": req.Key.Workspace, "eel_tenant": req.Key.Tenant, "eel_employee": req.Key.Employee, "eel_scene": req.Key.Scene, "eel_lane": req.Key.Lane, "eel_subject": req.Key.Subject, "eel_receipt": req.Receipt, "eel_version": r.config.Version})}
	if len(handlers) > 0 {
		options = append(options, adk.WithCallbacks(handlers...))
	}
	var iter *adk.AsyncIterator[*adk.AgentEvent]
	if !resume {
		iter = runner.Run(runCtx, req.Messages, options...)
	} else {
		var err error
		if targets == nil {
			iter, err = runner.Resume(runCtx, req.Receipt, options...)
		} else {
			iter, err = runner.ResumeWithParams(runCtx, req.Receipt, &adk.ResumeParams{Targets: targets}, options...)
		}
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
				stopExecution()
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

// requestCancellation uses Eino's endpoint middleware so ADK values (state,
// resume targets, tool call IDs) survive while cooperative I/O sees shutdown.
// Streaming endpoint contexts live until the owning request finishes.
type requestCancellation struct {
	*adk.BaseChatModelAgentMiddleware
	request context.Context
}

func (h *requestCancellation) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.request, cancel)
	if h.request.Err() != nil {
		cancel()
	}
	return child, func() { stop(); cancel() }
}
func (h *requestCancellation) WrapModel(_ context.Context, inner model.BaseChatModel, _ *adk.ModelContext) (model.BaseChatModel, error) {
	return &requestModel{inner: inner, cancellation: h}, nil
}
func (h *requestCancellation) WrapInvokableToolCall(_ context.Context, next adk.InvokableToolCallEndpoint, _ *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		ctx, done := h.callContext(ctx)
		defer done()
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return next(ctx, args, opts...)
	}, nil
}
func (h *requestCancellation) WrapEnhancedInvokableToolCall(_ context.Context, next adk.EnhancedInvokableToolCallEndpoint, _ *adk.ToolContext) (adk.EnhancedInvokableToolCallEndpoint, error) {
	return func(ctx context.Context, args *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		ctx, done := h.callContext(ctx)
		defer done()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return next(ctx, args, opts...)
	}, nil
}
func (h *requestCancellation) WrapStreamableToolCall(_ context.Context, next adk.StreamableToolCallEndpoint, _ *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		ctx, done := h.callContext(ctx)
		if err := ctx.Err(); err != nil {
			done()
			return nil, err
		}
		stream, err := next(ctx, args, opts...)
		if err != nil {
			done()
		}
		return stream, err
	}, nil
}
func (h *requestCancellation) WrapEnhancedStreamableToolCall(_ context.Context, next adk.EnhancedStreamableToolCallEndpoint, _ *adk.ToolContext) (adk.EnhancedStreamableToolCallEndpoint, error) {
	return func(ctx context.Context, args *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		ctx, done := h.callContext(ctx)
		if err := ctx.Err(); err != nil {
			done()
			return nil, err
		}
		stream, err := next(ctx, args, opts...)
		if err != nil {
			done()
		}
		return stream, err
	}, nil
}

type requestModel struct {
	inner        model.BaseChatModel
	cancellation *requestCancellation
}

func (m *requestModel) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	ctx, done := m.cancellation.callContext(ctx)
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, messages, opts...)
}
func (m *requestModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	ctx, done := m.cancellation.callContext(ctx)
	if err := ctx.Err(); err != nil {
		done()
		return nil, err
	}
	stream, err := m.inner.Stream(ctx, messages, opts...)
	if err != nil {
		done()
	}
	return stream, err
}

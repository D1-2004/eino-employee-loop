package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strings"
	"sync"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	core "github.com/D1-2004/eino-employee-loop/employee/execution"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/components/tool"
)

// Native is the default Eino execution worker. It owns execution tools and
// artifacts, while Task state transitions and Goal completion remain in M2.
type Native struct {
	State     storage.State
	Artifacts storage.Artifacts
	Tasks     core.TaskReader
	Context   core.NativeContext
	Runner    kernel.Agent
	mu        sync.Mutex
	cancels   map[string]context.CancelFunc
}

// Cancel asks the in-process invocation to stop. Only Port.Cancel can return
// durable exit or never-submitted proof.
func (n *Native) Cancel(runID string) {
	n.mu.Lock()
	cancel := n.cancels[runID]
	n.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

const executorPolicy = `Execute this accepted local Task, preserving original human requirements, methods and constraints. Original sources override model rewrites. Available tools define actual capabilities; never claim unavailable actions. All file paths are relative to this task's private workspace. Write actual deliverables through write_file and read back before finishing. finish_run only reports execution result, not Goal completion. If a business input is missing, ask_input saves a real continuation. Tool records and file readback are evidence; your prose is not a platform receipt.`

func (n *Native) Execute(ctx context.Context, record core.Record) (core.Result, error) {
	if n.State == nil || n.Artifacts == nil || n.Tasks == nil || n.Context == nil || n.Runner == nil {
		return core.Result{}, errors.New("execution: complete native binding required")
	}
	task := record.Request.Task
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	n.mu.Lock()
	if n.cancels == nil {
		n.cancels = map[string]context.CancelFunc{}
	}
	n.cancels[record.RunID] = cancel
	n.mu.Unlock()
	defer func() { n.mu.Lock(); delete(n.cancels, record.RunID); n.mu.Unlock() }()
	ctx = runCtx
	scope := task.Scope
	var profile contract.Profile
	if err := json.Unmarshal(record.Request.Configuration, &profile); err != nil {
		return core.Result{}, err
	}
	before := func(c context.Context) error {
		current, err := n.Tasks.Get(c, scope, task.ID)
		if err != nil {
			return err
		}
		run := current.CurrentRun()
		if current.State != work.Running || run == nil || run.ID != record.RunID || run.State != work.RunActive {
			return work.ErrState
		}
		return n.Context.Revalidate(c, record.Request.Frame)
	}
	if err := before(ctx); err != nil {
		return core.Result{}, err
	}
	prefix := "tasks/" + contextbuilder.OwnerKey(scope) + "/" + task.ID + "/"
	resolve := func(name string) (string, error) {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
		} else if strings.HasPrefix(name, "tasks/") {
			return "", work.ErrScope
		}
		if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return "", errors.New("execution: relative task path required")
		}
		return prefix + name, nil
	}
	result := core.Result{Artifacts: []work.Artifact{}}
	read := &kernel.Function{Spec: kernel.Spec("read_file", "Read a complete Markdown artifact in THIS task. Accepts a task-relative path or the exact same-task Path returned by write_file. Returns SHA256 for conditional updates.", map[string]any{"path": kernel.StringField()}, "path"), Before: before, Invoke: func(c context.Context, _ string, a map[string]any) (any, error) {
		p, err := resolve(kernel.Str(a, "path"))
		if err != nil {
			return nil, err
		}
		return n.Artifacts.Read(c, p)
	}}
	write := &kernel.Function{Spec: kernel.Spec("write_file", "Write a real task Markdown artifact. Existing content requires its current expected_sha256; identical content is a verified replay.", map[string]any{"path": kernel.StringField(), "content": kernel.StringField(), "expected_sha256": map[string]any{"type": "string"}}, "path", "content"), Before: before, Invoke: func(c context.Context, id string, a map[string]any) (any, error) {
		p, err := resolve(kernel.Str(a, "path"))
		if err != nil {
			return nil, err
		}
		body := kernel.Str(a, "content")
		scopeKey, _ := scope.Key()
		effectID := contextbuilder.Digest([]byte(record.ID + "/" + id))
		fingerprint := contextbuilder.Digest(encode(a))
		// Reserve immutable tool arguments before touching the filesystem. Native
		// IDs may reappear in a later model batch, so per-batch validation is insufficient.
		e := n.State.Transact(c, func(tx storage.Tx) error {
			prior, e := tx.Get(c, "execution-file-effects", scopeKey, effectID)
			if e == nil {
				if string(prior.Body) != string(encode(fingerprint)) {
					return storage.ErrConflict
				}
				return nil
			}
			if !errors.Is(e, storage.ErrNotFound) {
				return e
			}
			_, e = tx.Put(c, storage.Document{Namespace: "execution-file-effects", Scope: scopeKey, ID: effectID, Body: encode(fingerprint)})
			return e
		})
		if e != nil {
			return nil, e
		}
		old, e := n.Artifacts.Read(c, p)
		var doc storage.File
		if e == nil && old.Body == body {
			doc = old
		} else {
			if e != nil && !errors.Is(e, fs.ErrNotExist) {
				return nil, e
			}
			doc, err = n.Artifacts.Write(c, p, kernel.Str(a, "expected_sha256"), body)
			if err != nil {
				return nil, err
			}
		}
		verified, err := n.Artifacts.Read(c, p)
		if err != nil || verified.SHA256 != doc.SHA256 {
			return nil, errors.New("execution: artifact readback failed")
		}
		artifact := work.Artifact{Path: doc.Path, SHA256: doc.SHA256, Bytes: int64(len(doc.Body))}
		key := effectID
		err = n.State.Transact(c, func(tx storage.Tx) error {
			d, e := tx.Get(c, "execution-artifacts", scopeKey, key)
			raw := encode(artifact)
			if e == nil {
				if string(d.Body) != string(raw) {
					return storage.ErrConflict
				}
				return nil
			}
			if !errors.Is(e, storage.ErrNotFound) {
				return e
			}
			_, e = tx.Put(c, storage.Document{Namespace: "execution-artifacts", Scope: scopeKey, ID: key, Body: raw})
			return e
		})
		return artifact, err
	}}
	finish := &kernel.Function{Spec: kernel.Spec("finish_run", "Report the execution result after actual file/tool verification. This does not complete the business Goal.", map[string]any{"summary": kernel.StringField(), "state": map[string]any{"type": "string", "enum": []any{"succeeded", "failed"}}}, "summary", "state"), Before: before, Invoke: func(_ context.Context, _ string, a map[string]any) (any, error) {
		result.Output = kernel.Str(a, "summary")
		if kernel.Str(a, "state") == "failed" {
			result.Failure = result.Output
		}
		result.Exited = true
		return map[string]string{"execution_report": result.Output, "state": kernel.Str(a, "state")}, nil
	}}
	ask := &kernel.Function{Spec: kernel.Spec("ask_input", "Suspend this execution for a genuinely missing human input, preserving all completed tool results.", map[string]any{"prompt": kernel.StringField()}, "prompt"), Before: before, Invoke: func(c context.Context, id string, a map[string]any) (any, error) {
		was, _, saved := tool.GetInterruptState[string](c)
		if was {
			target, has, value := tool.GetResumeContext[string](c)
			if !target || !has {
				return nil, tool.StatefulInterrupt(c, saved, saved)
			}
			return map[string]string{"verified_answer": value}, nil
		}
		q := contract.Question{ID: "question-" + contextbuilder.Digest([]byte(record.ID+"/"+id)), Prompt: kernel.Str(a, "prompt")}
		raw := string(encode(q))
		return nil, tool.StatefulInterrupt(c, raw, raw)
	}}
	current, err := n.Tasks.Get(ctx, scope, task.ID)
	if err != nil {
		return result, err
	}
	targets := map[string]any{}
	frame := record.Request.Frame
	for _, w := range current.Waits {
		if w.Resolved && w.ExecutionID == record.ID && w.NativeTarget != "" {
			src, ref, e := n.Context.Source(ctx, scope, w.SourceReceipt)
			if e != nil {
				return result, e
			}
			if src.Text != w.Answer {
				return result, work.ErrScope
			}
			targets[w.NativeTarget] = src.Text
			frame.Refs = append(frame.Refs, ref)
		}
	}
	var targetArgs []map[string]any
	if len(targets) > 0 {
		targetArgs = append(targetArgs, targets)
	}
	output, err := n.Runner.Run(ctx, kernel.Invocation{Profile: profile, Scope: scope, Role: "executor", Receipt: record.ID, Context: frame, System: executorPolicy, Tools: []*kernel.Function{read, write, finish, ask}, Terminal: map[string]bool{"finish_run": true}, Targets: targetArgs, Check: func(c context.Context) error { return n.Context.Revalidate(c, frame) }})
	result.Interrupts = output.Interrupts
	// A checkpointed ReturnDirectly result is also authoritative after rebuild;
	// restore the execution report from the accepted terminal tool event.
	if !result.Exited && output.Completed && output.Output != nil {
		var report struct {
			ExecutionReport string `json:"execution_report"`
			State           string `json:"state"`
		}
		if json.Unmarshal([]byte(output.Output.Content), &report) == nil && report.ExecutionReport != "" {
			result.Output = report.ExecutionReport
			if report.State == "failed" {
				result.Failure = report.ExecutionReport
			}
			result.Exited = true
		}
	}
	key, _ := scope.Key()
	artifacts, e := n.State.List(context.WithoutCancel(ctx), "execution-artifacts", key)
	if e != nil {
		return result, e
	}
	seen := map[string]bool{}
	for _, d := range artifacts {
		var a work.Artifact
		if e = json.Unmarshal(d.Body, &a); e != nil {
			return result, e
		}
		if !strings.HasPrefix(a.Path, prefix) || seen[a.Path+"/"+a.SHA256] {
			continue
		}
		doc, e := n.Artifacts.Read(context.WithoutCancel(ctx), a.Path)
		if e != nil || doc.SHA256 != a.SHA256 {
			continue
		}
		result.Artifacts = append(result.Artifacts, a)
		seen[a.Path+"/"+a.SHA256] = true
	}
	return result, err
}

func encode(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }

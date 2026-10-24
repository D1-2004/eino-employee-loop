package entry

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strings"

	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/execution"
	"github.com/D1-2004/eino-employee-loop/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/work"
	"github.com/cloudwego/eino/components/tool"
)

const executorPolicy = `Execute this accepted local Task, preserving original human requirements, methods and constraints. Original sources override model rewrites. Available tools define actual capabilities; never claim unavailable actions. All file paths are relative to this task's private workspace. Write actual deliverables through write_file and read back before finishing. finish_run only reports execution result, not Goal completion. If a business input is missing, ask_input saves a real continuation. Tool records and file readback are evidence; your prose is not a platform receipt.`

func (s *standalone) backgroundOnce(ctx context.Context) (bool, error) {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, scene := range s.opts.Profile.Scenes {
		scope := s.opts.Profile.Scope(scene)
		tasks, err := s.work.List(ctx, scope)
		if err != nil {
			return false, err
		}
		for _, task := range tasks {
			run := task.CurrentRun()
			if run == nil {
				continue
			}
			if task.NeedsReview {
				held, e := s.reviewHeld(ctx, task)
				if e != nil {
					return false, e
				}
				if held {
					continue
				}
				e = s.review(ctx, task)
				if e == nil {
					return true, nil
				}
				if ctx.Err() != nil {
					return true, e
				}
				settle, cancel := settlement(ctx)
				saveErr := s.holdReview(settle, task, e)
				cancel()
				return true, errors.Join(e, saveErr)
			}
			if task.State == work.Waiting || task.State == work.Completed || task.State == work.Failed {
				continue
			}
			if task.State == work.Stopped || task.State == work.Paused || run.State == work.RunCancelling {
				if !run.ExitProven {
					record, e := s.executor.Lookup(ctx, scope, run.SubmitKey)
					if errors.Is(e, sqlite.ErrNotFound) {
						_, e = s.work.AbortLocalSubmission(ctx, scope, task.ID, run.ID)
						return true, e
					}
					if e != nil {
						return false, e
					}
					if record.State != "exited" {
						record, e = s.executor.Stop(ctx, scope, run.SubmitKey)
						if e != nil {
							return false, e
						}
					}
					state := work.RunStopped
					if record.Result.Failure == "" {
						state = work.RunSucceeded
					}
					_, e = s.work.RecordExecution(ctx, scope, task.ID, "local-exit-"+record.ID, work.ExecutionFact{RunID: run.ID, ExecutionID: record.ID, State: state, ExitProven: true, Output: record.Result.Output, Failure: record.Result.Failure, Artifacts: record.Result.Artifacts})
					return true, e
				}
				continue
			}
			if run.State == work.RunQueued {
				if _, err = s.work.BeginSubmission(ctx, scope, task.ID, run.ID); err != nil {
					return true, err
				}
				task, err = s.work.Get(ctx, scope, task.ID)
				if err != nil {
					return true, err
				}
				frame, err := s.context.Build(ctx, scope, contextbuilder.Execution, "", task.ID)
				if err != nil {
					return true, err
				}
				record, err := s.executor.Submit(ctx, execution.Request{Task: task, Frame: frame, Configuration: task.Definition.Configuration})
				if err != nil {
					return true, err
				}
				_, err = s.work.Submission(ctx, scope, task.ID, run.ID, record.ID, false)
				return true, err
			}
			if run.State == work.RunPreparing || run.State == work.RunUnknown {
				record, err := s.executor.Lookup(ctx, scope, run.SubmitKey)
				if errors.Is(err, sqlite.ErrNotFound) && run.State == work.RunPreparing {
					frame, e := s.context.Build(ctx, scope, contextbuilder.Execution, "", task.ID)
					if e != nil {
						return false, e
					}
					record, err = s.executor.Submit(ctx, execution.Request{Task: task, Frame: frame, Configuration: task.Definition.Configuration})
				}
				if err != nil {
					return false, err
				}
				_, err = s.work.Submission(ctx, scope, task.ID, run.ID, record.ID, false)
				return true, err
			}
			if run.State == work.RunActive || run.State == work.RunCancelling {
				record, err := s.executor.Advance(ctx, scope, run.SubmitKey, s.execute)
				if err != nil {
					return true, err
				}
				if record.State == "interrupted" {
					waits := []work.Wait{}
					for _, i := range record.Result.Interrupts {
						encoded, ok := i.Info.(string)
						if !ok {
							return true, errors.New("execution: unrecognized question")
						}
						var q nativeQuestion
						if err = json.Unmarshal([]byte(encoded), &q); err != nil {
							return true, err
						}
						waits = append(waits, work.Wait{ID: q.ID, Prompt: q.Prompt, Kind: "human", NativeTarget: i.ID, ExecutionID: record.ID})
					}
					_, err = s.work.Suspend(ctx, scope, task.ID, run.ID, record.ID, waits)
					return true, err
				}
				if record.State == "exited" && record.Result.Exited {
					state := work.RunSucceeded
					if record.Result.Failure != "" {
						state = work.RunFailed
					}
					_, err = s.work.RecordExecution(ctx, scope, task.ID, "result-"+record.ID, work.ExecutionFact{RunID: run.ID, ExecutionID: record.ID, State: state, Output: record.Result.Output, Failure: record.Result.Failure, ExitProven: true, Artifacts: record.Result.Artifacts})
					return true, err
				}
			}
		}
	}
	return false, nil
}
func (s *standalone) execute(ctx context.Context, record execution.Record) (execution.Result, error) {
	task := record.Request.Task
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.executionMu.Lock()
	if s.executionCancels == nil {
		s.executionCancels = map[string]context.CancelFunc{}
	}
	s.executionCancels[record.RunID] = cancel
	s.executionMu.Unlock()
	defer func() { s.executionMu.Lock(); delete(s.executionCancels, record.RunID); s.executionMu.Unlock() }()
	ctx = runCtx
	scope := task.Scope
	var profile Profile
	if err := json.Unmarshal(record.Request.Configuration, &profile); err != nil {
		return execution.Result{}, err
	}
	before := func(c context.Context) error {
		current, err := s.work.Get(c, scope, task.ID)
		if err != nil {
			return err
		}
		run := current.CurrentRun()
		if current.State != work.Running || run == nil || run.ID != record.RunID || run.State != work.RunActive {
			return work.ErrState
		}
		return s.context.Revalidate(c, record.Request.Frame)
	}
	if err := before(ctx); err != nil {
		return execution.Result{}, err
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
	result := execution.Result{Artifacts: []work.Artifact{}}
	read := &function{spec: spec("read_file", "Read a complete Markdown artifact in THIS task. Accepts a task-relative path or the exact same-task Path returned by write_file. Returns SHA256 for conditional updates.", map[string]any{"path": stringField()}, "path"), before: before, run: func(c context.Context, _ string, a map[string]any) (any, error) {
		p, err := resolve(str(a, "path"))
		if err != nil {
			return nil, err
		}
		return s.files.Read(c, p)
	}}
	write := &function{spec: spec("write_file", "Write a real task Markdown artifact. Existing content requires its current expected_sha256; identical content is a verified replay.", map[string]any{"path": stringField(), "content": stringField(), "expected_sha256": map[string]any{"type": "string"}}, "path", "content"), before: before, run: func(c context.Context, id string, a map[string]any) (any, error) {
		p, err := resolve(str(a, "path"))
		if err != nil {
			return nil, err
		}
		body := str(a, "content")
		scopeKey, _ := scope.Key()
		effectID := contextbuilder.Digest([]byte(record.ID + "/" + id))
		fingerprint := contextbuilder.Digest(encode(a))
		// Reserve immutable tool arguments before touching the filesystem. Native
		// IDs may reappear in a later model batch, so per-batch validation is insufficient.
		e := s.db.Transaction(c, func(tx *sqlite.Tx) error {
			prior, e := tx.Get(c, "execution-file-effects", scopeKey, effectID)
			if e == nil {
				if string(prior.Body) != string(encode(fingerprint)) {
					return sqlite.ErrConflict
				}
				return nil
			}
			if !errors.Is(e, sqlite.ErrNotFound) {
				return e
			}
			_, e = tx.Put(c, sqlite.Document{Namespace: "execution-file-effects", Scope: scopeKey, ID: effectID, Body: encode(fingerprint)})
			return e
		})
		if e != nil {
			return nil, e
		}
		old, e := s.files.Read(c, p)
		var doc markdown.Document
		if e == nil && old.Body == body {
			doc = old
		} else {
			if e != nil && !errors.Is(e, fs.ErrNotExist) {
				return nil, e
			}
			doc, err = s.files.Write(c, p, str(a, "expected_sha256"), body)
			if err != nil {
				return nil, err
			}
		}
		verified, err := s.files.Read(c, p)
		if err != nil || verified.SHA256 != doc.SHA256 {
			return nil, errors.New("execution: artifact readback failed")
		}
		artifact := work.Artifact{Path: doc.Path, SHA256: doc.SHA256, Bytes: int64(len(doc.Body))}
		key := effectID
		err = s.db.Transaction(c, func(tx *sqlite.Tx) error {
			d, e := tx.Get(c, "execution-artifacts", scopeKey, key)
			raw := encode(artifact)
			if e == nil {
				if string(d.Body) != string(raw) {
					return sqlite.ErrConflict
				}
				return nil
			}
			if !errors.Is(e, sqlite.ErrNotFound) {
				return e
			}
			_, e = tx.Put(c, sqlite.Document{Namespace: "execution-artifacts", Scope: scopeKey, ID: key, Body: raw})
			return e
		})
		return artifact, err
	}}
	finish := &function{spec: spec("finish_run", "Report the execution result after actual file/tool verification. This does not complete the business Goal.", map[string]any{"summary": stringField(), "state": map[string]any{"type": "string", "enum": []any{"succeeded", "failed"}}}, "summary", "state"), before: before, run: func(_ context.Context, _ string, a map[string]any) (any, error) {
		result.Output = str(a, "summary")
		if str(a, "state") == "failed" {
			result.Failure = result.Output
		}
		result.Exited = true
		return map[string]string{"execution_report": result.Output, "state": str(a, "state")}, nil
	}}
	ask := &function{spec: spec("ask_input", "Suspend this execution for a genuinely missing human input, preserving all completed tool results.", map[string]any{"prompt": stringField()}, "prompt"), before: before, run: func(c context.Context, id string, a map[string]any) (any, error) {
		was, _, saved := tool.GetInterruptState[string](c)
		if was {
			target, has, value := tool.GetResumeContext[string](c)
			if !target || !has {
				return nil, tool.StatefulInterrupt(c, saved, saved)
			}
			return map[string]string{"verified_answer": value}, nil
		}
		q := nativeQuestion{ID: "question-" + contextbuilder.Digest([]byte(record.ID+"/"+id)), Prompt: str(a, "prompt")}
		raw := string(encode(q))
		return nil, tool.StatefulInterrupt(c, raw, raw)
	}}
	current, err := s.work.Get(ctx, scope, task.ID)
	if err != nil {
		return result, err
	}
	targets := map[string]any{}
	frame := record.Request.Frame
	for _, w := range current.Waits {
		if w.Resolved && w.ExecutionID == record.ID && w.NativeTarget != "" {
			src, ref, e := s.context.Source(ctx, scope, w.SourceReceipt)
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
	output, err := s.runAgent(ctx, profile, scope, "executor", record.ID, frame, executorPolicy, []*function{read, write, finish, ask}, map[string]bool{"finish_run": true}, false, targetArgs...)
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
	artifacts, e := s.db.List(context.WithoutCancel(ctx), "execution-artifacts", key)
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
		doc, e := s.files.Read(context.WithoutCancel(ctx), a.Path)
		if e != nil || doc.SHA256 != a.SHA256 {
			continue
		}
		result.Artifacts = append(result.Artifacts, a)
		seen[a.Path+"/"+a.SHA256] = true
	}
	return result, err
}

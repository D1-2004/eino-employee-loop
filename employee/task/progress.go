package task

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func (s *Worker) Step(ctx context.Context) (bool, error) {
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
				e = s.Review(ctx, task)
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
					record, e := s.executor.Cancel(ctx, execution.TargetFor(task))
					if e != nil {
						return false, e
					}
					if e = s.bridge.ValidateRecord(ctx, record, task); e != nil {
						return false, e
					}
					if record.NeverSubmitted {
						_, e = s.work.RecordNeverSubmitted(ctx, scope, task.ID, run.ID, record.ID)
						return true, e
					}
					if record.State != "exited" || !record.Result.Exited {
						continue
					}
					state := work.RunStopped
					if record.Result.Failure == "" {
						state = work.RunSucceeded
					}
					_, e = s.work.RecordExecution(ctx, scope, task.ID, "provider-exit-"+record.ID, work.ExecutionFact{RunID: run.ID, ExecutionID: record.ID, State: state, ExitProven: true, Output: record.Result.Output, Failure: record.Result.Failure, Artifacts: record.Result.Artifacts})
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
				return s.bridge.Submit(ctx, task)
			}
			if run.State == work.RunPreparing {
				return s.bridge.Submit(ctx, task)
			}
			if run.State == work.RunUnknown {
				record, err := s.executor.Lookup(ctx, scope, run.SubmitKey)
				if err != nil {
					return false, err
				}
				if err = s.bridge.ValidateRecord(ctx, record, task); err != nil {
					return false, err
				}
				if record.NeverSubmitted {
					return false, execution.ErrUnknown
				}
				_, err = s.work.Submission(ctx, scope, task.ID, run.ID, record.ID, false)
				return true, err
			}
			if run.State == work.RunActive || run.State == work.RunCancelling {
				record, err := s.executor.Poll(ctx, scope, run.SubmitKey)
				if err != nil {
					return true, err
				}
				if err = s.bridge.ValidateRecord(ctx, record, task); err != nil {
					return true, err
				}
				if record.NeverSubmitted {
					return true, execution.ErrUnknown
				}
				if record.State == "interrupted" {
					waits := []work.Wait{}
					for _, i := range record.Result.Interrupts {
						encoded, ok := i.Info.(string)
						if !ok {
							return true, errors.New("execution: unrecognized question")
						}
						var q contract.Question
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

package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func (s *Service) Step(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if progress, err := s.messageOnce(ctx); progress || err != nil {
		return progress, err
	}
	for _, scene := range s.opts.Profile.Scenes {
		scope := s.opts.Profile.Scope(scene)
		key, _ := scope.Key()
		// Reconcile from durable work events. Goal completion and its original
		// summary survive a subsequent Goal revision before this worker runs.
		cursorDoc, err := s.opts.State.Get(ctx, "delivery-cursors", key, "work-events")
		var cursor int64
		if err == nil {
			if err = json.Unmarshal(cursorDoc.Body, &cursor); err != nil {
				return false, err
			}
		} else if !errors.Is(err, storage.ErrNotFound) {
			return false, err
		}
		events, err := s.opts.State.Events(ctx, "work-events", key, cursor, 100)
		if err != nil {
			return false, err
		}
		for _, event := range events {
			var changed struct {
				Task    *work.Task   `json:"task"`
				Receipt work.Receipt `json:"receipt"`
				State   work.State   `json:"task_state"`
				Goal    work.Goal    `json:"goal"`
				Waits   []work.Wait  `json:"waits"`
			}
			if err = json.Unmarshal(event.Body, &changed); err != nil {
				return false, err
			}
			if event.Kind == "goal.completed" && changed.State == work.Completed {
				task, e := s.opts.Tasks.Get(ctx, scope, changed.Receipt.TaskID)
				if e != nil {
					return false, e
				}
				if changed.Task != nil {
					task = *changed.Task
				}
				body, e := s.renderResult(ctx, task, changed.Goal, changed.Receipt.RunID)
				if e != nil {
					return false, e
				}
				id := "result-" + contextbuilder.Digest(encode([]any{task.ID, changed.Goal.Revision}))
				if _, e = s.opts.Outbox.Enqueue(ctx, Intent{ID: id, Scope: scope, SourceReceipt: sourceForTask(task), TaskID: task.ID, GoalRevision: changed.Goal.Revision, Kind: "result", Body: body}); e != nil {
					return false, e
				}
			}

			if (event.Kind == "goal.failed" || event.Kind == "goal.paused" || event.Kind == "goal.stopped") && changed.Task != nil {
				task := *changed.Task
				body := fmt.Sprintf("Task %s is %s. %s", task.ID, task.State, task.Goal.Summary)
				if run := task.CurrentRun(); run != nil && !run.ExitProven {
					body += " The current execution has not yet confirmed exit."
				}
				id := "status-" + contextbuilder.Digest(encode([]any{task.ID, event.ID}))
				if _, e := s.opts.Outbox.Enqueue(ctx, Intent{ID: id, Scope: scope, SourceReceipt: sourceForTask(task), TaskID: task.ID, GoalRevision: task.Goal.Revision, Kind: "status", Body: body}); e != nil {
					return false, e
				}
			}
			if changed.State == work.Waiting {
				task, e := s.opts.Tasks.Get(ctx, scope, changed.Receipt.TaskID)
				if e != nil {
					return false, e
				}
				for _, w := range changed.Waits {
					if (w.Kind == "human" || w.Kind == "recovery") && !w.Resolved {
						if _, e = s.opts.Outbox.Enqueue(ctx, Intent{ID: w.ID, Scope: scope, SourceReceipt: sourceForTask(task), TaskID: task.ID, GoalRevision: changed.Goal.Revision, Kind: "question", Body: w.Prompt}); e != nil {
							return false, e
						}
					}
				}
			}
			cursor = event.Sequence
			cursorDoc.Namespace = "delivery-cursors"
			cursorDoc.Scope = key
			cursorDoc.ID = "work-events"
			cursorDoc.Body = encode(cursor)
			rev, e := s.opts.State.Put(ctx, cursorDoc)
			if e != nil {
				return false, e
			}
			cursorDoc.Revision = rev
		}
		// Reconcile every scene's events before attempting their outboxes. An
		// unknown item in an earlier scene cannot prevent a later ready send.
		if scene == s.opts.Profile.Scenes[len(s.opts.Profile.Scenes)-1] {
			progress, e := s.advanceOutbox(ctx)
			if progress || e != nil {
				return progress, e
			}
		}
		if len(events) > 0 {
			return true, nil
		}
	}
	return false, nil
}

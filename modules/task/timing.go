package task

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/kernel"
	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
)

func (s *Worker) Timing(ctx context.Context) (bool, error) {
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	for _, scene := range s.opts.Profile.Scenes {
		scope := s.opts.Profile.Scope(scene)
		changed, err := s.scheduler.Tick(ctx, scope, s.now())
		if changed || err != nil {
			return changed, err
		}
		occurrences, err := s.scheduler.Occurrences(ctx, scope)
		if err != nil {
			return false, err
		}
		for _, o := range occurrences {
			if o.Acknowledged {
				continue
			}
			task, err := s.work.Get(ctx, scope, o.TaskID)
			if err != nil {
				return false, err
			}
			if task.Goal.Revision != o.GoalRevision {
				recovered, e := s.scheduler.RecoverOccurrence(ctx, scope, o.ID)
				if e != nil || recovered {
					return recovered, e
				}
				return true, s.scheduler.Acknowledge(ctx, scope, o.ID)
			}
			if task.State == work.Stopped || task.State == work.Paused {
				return true, s.scheduler.Acknowledge(ctx, scope, o.ID)
			}
			if task.State == work.Waiting || task.NeedsReview || task.CurrentRun() == nil || !task.CurrentRun().ExitProven {
				continue
			}
			return true, s.attend(ctx, task, o)
		}
	}
	return false, nil
}
func (s *Worker) attend(ctx context.Context, task work.Task, o work.Occurrence) error {
	frame, err := s.context.Build(ctx, task.Scope, contextbuilder.Execution, "", task.ID)
	if err != nil {
		return err
	}
	var profile contract.Profile
	if err = json.Unmarshal(task.Definition.Configuration, &profile); err != nil {
		return err
	}
	var proposal string
	fn := &kernel.Function{Spec: kernel.Spec("decide_attention", "Handle this authorized periodic occurrence on the same Task. Continue only the accepted work; skip if no action is appropriate.", map[string]any{"kind": map[string]any{"type": "string", "enum": []any{"continue", "skip"}}, "reason": kernel.StringField()}, "kind", "reason"), Before: func(c context.Context) error { return s.context.Revalidate(c, frame) }, Invoke: func(_ context.Context, _ string, a map[string]any) (any, error) {
		proposal = kernel.Str(a, "kind")
		return map[string]string{"attention_proposal": proposal}, nil
	}}
	frame.Gaps = append(frame.Gaps, "Authorized occurrence: "+string(encode(o)))
	out, err := s.runner.Run(ctx, kernel.Invocation{Profile: profile, Scope: task.Scope, Role: "task_attention", Receipt: o.ID, Context: frame, System: "Evaluate the authorized occurrence against the existing Task and original sources. You may continue the SAME Task or skip this occurrence. Never create a replacement Task or invent a new goal. An active or waiting task is not duplicated.", Tools: []*kernel.Function{fn}, Terminal: map[string]bool{"decide_attention": true}, Check: func(c context.Context) error { return s.context.Revalidate(c, frame) }})
	if err != nil {
		return err
	}
	if proposal == "" && out.Output != nil {
		var p struct {
			Kind string `json:"attention_proposal"`
		}
		_ = json.Unmarshal([]byte(out.Output.Content), &p)
		proposal = p.Kind
	}
	if proposal == "skip" {
		return s.scheduler.Acknowledge(ctx, task.Scope, o.ID)
	}
	if proposal != "continue" {
		return errors.New("attention: missing validated decision")
	}
	schedules, e := s.scheduler.Schedules(ctx, task.Scope)
	if e != nil {
		return e
	}
	var src work.Source
	for _, schedule := range schedules {
		if schedule.ID == o.ScheduleID {
			src = schedule.Source
			break
		}
	}
	if src.Receipt == "" {
		return work.ErrStale
	}
	receipt, err := s.work.Control(ctx, task.Scope, src, o.ID, task.ID, "continue", task.Definition.Instruction)
	if err != nil {
		return err
	}
	return s.scheduler.AcknowledgeForGoal(ctx, task.Scope, o.ID, o.GoalRevision, receipt.GoalRevision)
}
func (s *Worker) Schedule(ctx context.Context, scope work.Scope, in work.Schedule) (work.Schedule, error) {
	return s.scheduler.Schedule(ctx, scope, in)
}
func (s *Worker) Signal(ctx context.Context, in contract.Input, condition string) error {
	scope := s.opts.Profile.Scope(in.Scene)
	if in.Origin == "" {
		return work.ErrScope
	}
	if _, err := s.context.Admit(ctx, scope, contextbuilder.Source{ID: in.ID, Actor: in.Actor, Text: in.Text}); err != nil {
		return err
	}
	return s.scheduler.Signal(ctx, scope, work.Signal{ID: in.ID, Condition: condition, Payload: in.Text, Source: work.Source{Receipt: in.ID, Actor: in.Actor, Text: in.Text}, At: s.now()})
}

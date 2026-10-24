package entry

import (
	"context"
	"errors"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

type Failure = contract.Failure

func (s *standalone) Retry(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("entry: retry requires Task or admission ID")
	}
	if found, err := s.chat.Retry(ctx, id); found || err != nil {
		return err
	}
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	for _, scene := range s.opts.Profile.Scenes {
		scope := s.opts.Profile.Scope(scene)
		for _, worker := range s.ownerWorkers {
			if recovery, ok := worker.(interface {
				Retry(context.Context, work.Scope, string) (bool, error)
			}); ok {
				if found, err := recovery.Retry(ctx, scope, id); found || err != nil {
					return err
				}
			}
		}
		if found, err := s.tasksWorker.Retry(ctx, scope, id); found || err != nil {
			return err
		}
	}
	return storage.ErrNotFound
}
func (s *standalone) Failures(ctx context.Context, scope work.Scope) ([]Failure, error) {
	out, err := s.chat.Failures(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, read := range []func(context.Context, work.Scope) ([]contract.Failure, error){s.tasksWorker.Failures, s.interaction.Failures} {
		items, e := read(ctx, scope)
		if e != nil {
			return nil, e
		}
		out = append(out, items...)
	}
	if diagnostic, ok := s.ownerWorker.(interface {
		Failures(context.Context, work.Scope) ([]contract.Failure, error)
	}); ok {
		items, e := diagnostic.Failures(ctx, scope)
		if e != nil {
			return nil, e
		}
		out = append(out, items...)
	}
	return out, nil
}

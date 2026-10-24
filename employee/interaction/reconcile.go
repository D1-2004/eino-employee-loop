package interaction

import (
	"context"
	"errors"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

type pendingDelivery struct {
	scope  work.Scope
	record Record
}

// advanceOutbox tries ready intents across all scenes before reconciling unknown
// intents. An uncertain transport fact stays durable without pinning unrelated work.
func (s *Service) advanceOutbox(ctx context.Context) (bool, error) {
	ready, unknown := []pendingDelivery{}, []pendingDelivery{}
	var otherErr, unknownErr error
	for _, scene := range s.opts.Profile.Scenes {
		scope := s.opts.Profile.Scope(scene)
		records, err := s.opts.Outbox.List(ctx, scope)
		if err != nil {
			return false, err
		}
		for _, r := range records {
			switch r.State {
			case "cancelled", "failed":
				continue
			case "applied":
				if err = s.recordDelivery(ctx, scope, r); err != nil {
					otherErr = errors.Join(otherErr, err)
				}
			case "unknown", "submitting", "provider_accepted":
				unknown = append(unknown, pendingDelivery{scope, r})
			default:
				ready = append(ready, pendingDelivery{scope, r})
			}
		}
	}
	progress := false
	for _, item := range append(ready, unknown...) {
		if err := ctx.Err(); err != nil {
			return progress, errors.Join(otherErr, err)
		}
		r, err := s.opts.Outbox.Deliver(ctx, item.scope, item.record.Intent.ID)
		if err != nil {
			if errors.Is(err, ErrPending) || r.State == "provider_accepted" {
				unknownErr = errors.Join(unknownErr, ErrPending)
			} else if errors.Is(err, ErrUnknown) || r.State == "unknown" || r.State == "submitting" {
				unknownErr = errors.Join(unknownErr, ErrUnknown, err)
			} else {
				otherErr = errors.Join(otherErr, err)
			}
			continue
		}
		progress = true
		if err = s.recordDelivery(ctx, item.scope, r); err != nil {
			otherErr = errors.Join(otherErr, err)
		}
	}
	if otherErr != nil {
		return progress, otherErr
	}
	if progress {
		return true, nil
	}
	return false, unknownErr
}

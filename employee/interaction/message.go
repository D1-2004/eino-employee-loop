package interaction

import (
	"context"
	"encoding/json"
	"errors"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

type messageJob struct {
	Input                    contract.Input
	Profile                  contract.Profile
	Kind, Instruction, State string
	Frame                    *contextbuilder.Frame
}

// EnqueueMessage freezes an explicitly requested message. jobID identifies its
// caller; the durable ID remains source/call based for compatibility.
func (s *Service) EnqueueMessage(ctx context.Context, profile contract.Profile, input contract.Input, jobID, call, kind, instruction string) (any, error) {
	if kind != "greeting" && kind != "routine" {
		return nil, work.ErrState
	}
	id := "message-" + contextbuilder.Digest(encode([]any{profile.Scope(input.Scene), input.ID, call}))
	item := messageJob{Input: input, Profile: profile, Kind: kind, Instruction: instruction, State: "queued"}
	err := s.opts.State.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "message-jobs", profile.Key(), id)
		if e == nil {
			var old messageJob
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			old.State = "queued"
			old.Frame = nil
			if string(encode(old)) != string(encode(item)) {
				return storage.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		_, e = tx.Put(ctx, storage.Document{Namespace: "message-jobs", Scope: profile.Key(), ID: id, Body: encode(item)})
		return e
	})
	return map[string]string{"accepted_message": id, "kind": kind}, err
}

// Called within the service worker lock. A requested greeting/routine has its own frozen
// source and model budget; an idle timer does not create unsolicited messages.
func (s *Service) messageOnce(ctx context.Context) (bool, error) {
	docs, err := s.opts.State.List(ctx, "message-jobs", s.opts.Profile.Key())
	if err != nil {
		return false, err
	}
	for _, d := range docs {
		var item messageJob
		if err = json.Unmarshal(d.Body, &item); err != nil {
			return false, err
		}
		if item.State != "queued" {
			continue
		}
		scope := item.Profile.Scope(item.Input.Scene)
		if item.Frame == nil {
			f, e := s.opts.Context.Build(ctx, scope, contextbuilder.Front, item.Input.ID, "")
			if e != nil {
				return false, e
			}
			item.Frame = &f
			d.Body = encode(item)
			rev, e := s.opts.State.Put(ctx, d)
			if e != nil {
				return false, e
			}
			d.Revision = rev
		}
		text, e := s.compose(ctx, item.Profile, scope, item.Kind, d.ID, *item.Frame, "Prepare only this explicitly requested "+item.Kind+" message. The original source defines recipient scope, timing, tone and content. Never invent permissions, commitments or finished work. Do not create a new business Task.", "publish_result", "text", item.Instruction)
		if e != nil {
			return false, e
		}
		if _, e = s.opts.Outbox.Enqueue(ctx, Intent{ID: d.ID, SourceReceipt: item.Input.ID, Scope: scope, Kind: item.Kind, Body: text}); e != nil {
			return false, e
		}
		item.State = "done"
		d.Body = encode(item)
		_, e = s.opts.State.Put(ctx, d)
		return e == nil, e
	}
	return false, nil
}

package entry

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/interaction"
	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/work"
)

type messageJob struct {
	Input                    Input
	Profile                  Profile
	Kind, Instruction, State string
	Frame                    *contextbuilder.Frame
}

func (s *standalone) enqueueMessage(ctx context.Context, j job, call, kind, instruction string) (any, error) {
	if kind != "greeting" && kind != "routine" {
		return nil, work.ErrState
	}
	id := "message-" + contextbuilder.Digest(encode([]any{j.Profile.Scope(j.Input.Scene), j.Input.ID, call}))
	item := messageJob{Input: j.Input, Profile: j.Profile, Kind: kind, Instruction: instruction, State: "queued"}
	err := s.db.Transaction(ctx, func(tx *sqlite.Tx) error {
		d, e := tx.Get(ctx, "message-jobs", j.Profile.Key(), id)
		if e == nil {
			var old messageJob
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			old.State = "queued"
			old.Frame = nil
			if string(encode(old)) != string(encode(item)) {
				return sqlite.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sqlite.ErrNotFound) {
			return e
		}
		_, e = tx.Put(ctx, sqlite.Document{Namespace: "message-jobs", Scope: j.Profile.Key(), ID: id, Body: encode(item)})
		return e
	})
	return map[string]string{"accepted_message": id, "kind": kind}, err
}

// Called within deliveryMu. A requested greeting/routine has its own frozen
// source and model budget; an idle timer does not create unsolicited messages.
func (s *standalone) messageOnce(ctx context.Context) (bool, error) {
	docs, err := s.db.List(ctx, "message-jobs", s.opts.Profile.Key())
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
			f, e := s.context.Build(ctx, scope, contextbuilder.Front, item.Input.ID, "")
			if e != nil {
				return false, e
			}
			item.Frame = &f
			d.Body = encode(item)
			rev, e := s.db.Put(ctx, d)
			if e != nil {
				return false, e
			}
			d.Revision = rev
		}
		text, e := s.compose(ctx, item.Profile, scope, item.Kind, d.ID, *item.Frame, "Prepare only this explicitly requested "+item.Kind+" message. The original source defines recipient scope, timing, tone and content. Never invent permissions, commitments or finished work. Do not create a new business Task.", "publish_result", "text", item.Instruction)
		if e != nil {
			return false, e
		}
		if _, e = s.delivery.Enqueue(ctx, interaction.Intent{ID: d.ID, SourceReceipt: item.Input.ID, Scope: scope, Kind: item.Kind, Body: text}); e != nil {
			return false, e
		}
		item.State = "done"
		d.Body = encode(item)
		_, e = s.db.Put(ctx, d)
		return e == nil, e
	}
	return false, nil
}

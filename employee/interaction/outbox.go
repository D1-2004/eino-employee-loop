// Package interaction owns durable delivery intent and reconciled receipts.
// A Task's completion and a platform delivery are different facts.
package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

var ErrObsolete = errors.New("delivery: intent revoked or obsolete")

var ErrUnknown = errors.New("delivery: unknown submission; lookup required")
var ErrPending = errors.New("delivery: provider accepted; delivered receipt pending")

// ErrPermanent is only for a definitive rejection of this Send, never a timeout
// or failure to query an already accepted delivery.
var ErrPermanent = errors.New("delivery: permanent provider rejection")

type Port interface {
	Send(context.Context, Intent) (Receipt, error)
	Lookup(context.Context, Intent) (Lookup, error)
}
type Record struct {
	Intent                  Intent
	State, Owner, LastError string
	Receipt                 *Receipt
}

// IntentSink admits an immutable command to the owning Outbox, not a transport.
type IntentSink interface {
	Enqueue(context.Context, Intent) (Record, error)
}

// SameIntentCommand permits only the same resolver-filled fields as Manager.
func SameIntentCommand(prior, command Intent) bool { return replayMatches(prior, command) }

type Manager struct {
	DB            storage.State
	Port          Port
	Owner         string
	Authorize     func(context.Context, work.Scope) error
	ValidateSend  func(context.Context, Intent) error
	PrepareIntent func(context.Context, Intent) (Intent, error)
}

func (m *Manager) Enqueue(ctx context.Context, in Intent) (Record, error) {
	if m.DB == nil || m.Port == nil || m.Owner == "" || m.Authorize == nil {
		return Record{}, errors.New("delivery: complete binding required")
	}
	command, err := cloneIntent(in)
	if err != nil {
		return Record{}, err
	}
	if err = ValidateIntent(command); err != nil {
		return Record{}, err
	}
	if err = m.Authorize(ctx, command.Scope); err != nil {
		return Record{}, err
	}
	scope, err := command.Scope.Key()
	if err != nil {
		return Record{}, err
	}
	// Replays never resolve again: a changed Host locator cannot redirect an
	// accepted command. This read and the later transaction both verify arguments.
	old, err := m.DB.Get(ctx, "outbox", scope, command.ID)
	if err == nil {
		var prior Record
		if err = json.Unmarshal(old.Body, &prior); err != nil {
			return Record{}, err
		}
		if !replayMatches(prior.Intent, command) {
			return Record{}, storage.ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return Record{}, err
	}
	prepared, err := cloneIntent(command)
	if err != nil {
		return Record{}, err
	}
	if m.PrepareIntent != nil {
		prepared, err = m.PrepareIntent(ctx, prepared)
		if err != nil {
			return Record{}, err
		}
		if !preparedMatches(command, prepared) {
			return Record{}, errors.New("delivery: resolver may only fill target/payload")
		}
	}
	prepared, err = cloneIntent(prepared)
	if err != nil {
		return Record{}, err
	}
	if err = ValidateIntent(prepared); err != nil {
		return Record{}, err
	}
	r := Record{Intent: prepared, State: "queued"}
	err = m.DB.Transact(ctx, func(tx storage.Tx) error {
		old, e := tx.Get(ctx, "outbox", scope, command.ID)
		if e == nil {
			if e = json.Unmarshal(old.Body, &r); e != nil {
				return e
			}
			if !replayMatches(r.Intent, command) {
				return storage.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		raw, e := json.Marshal(r)
		if e != nil {
			return e
		}
		_, e = tx.Put(ctx, storage.Document{Namespace: "outbox", Scope: scope, ID: command.ID, Body: raw})
		return e
	})
	return r, err
}

func (m *Manager) List(ctx context.Context, scope work.Scope) ([]Record, error) {
	if err := m.Authorize(ctx, scope); err != nil {
		return nil, err
	}
	key, err := scope.Key()
	if err != nil {
		return nil, err
	}
	docs, err := m.DB.List(ctx, "outbox", key)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, d := range docs {
		var r Record
		if err = json.Unmarshal(d.Body, &r); err != nil {
			return nil, err
		}
		if r.Intent.Scope != scope || r.Intent.ID != d.ID {
			return nil, work.ErrScope
		}
		out = append(out, r)
	}
	return out, nil
}
func (m *Manager) Deliver(ctx context.Context, scope work.Scope, id string) (Record, error) {
	if err := m.Authorize(ctx, scope); err != nil {
		return Record{}, err
	}
	key, err := scope.Key()
	if err != nil {
		return Record{}, err
	}
	d, err := m.DB.Get(ctx, "outbox", key, id)
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err = json.Unmarshal(d.Body, &r); err != nil {
		return r, err
	}
	if r.Intent.Scope != scope || r.Intent.ID != id {
		return r, work.ErrScope
	}
	if r.State == "applied" || r.State == "cancelled" || r.State == "failed" {
		return r, nil
	}
	if r.State == "submitting" && r.Owner == m.Owner {
		return r, ErrUnknown
	}
	if r.State == "submitting" || r.State == "unknown" || r.State == "provider_accepted" {
		portIntent, e := cloneIntent(r.Intent)
		if e != nil {
			return r, e
		}
		lookup, e := m.Port.Lookup(ctx, portIntent)
		if e != nil {
			return r, e
		}
		if lookup.Receipt != nil {
			return m.confirm(ctx, d, r, lookup.Receipt)
		}
		if r.State == "provider_accepted" {
			return r, ErrPending
		}
		typedLocalAbsent := false
		if r.Intent.Target != nil && r.Intent.Target.Transport == "local-file" && lookup.ProvenAbsent {
			fingerprint, _ := IntentFingerprint(r.Intent)
			target, _ := TargetFingerprint(r.Intent.Target)
			typedLocalAbsent = lookup.IntentSHA256 == fingerprint && lookup.TargetSHA256 == target
		}
		if !lookup.ProvenAbsent || (r.Intent.Target != nil || r.Intent.Payload != nil) && !typedLocalAbsent {
			return r, ErrUnknown
		}
	}
	if r.Intent.Target != nil && r.Intent.Target.Transport != "local-file" && m.ValidateSend == nil {
		return r, errors.New("delivery: typed remote destination requires Host ValidateSend")
	}
	if m.ValidateSend != nil {
		validationIntent, e := cloneIntent(r.Intent)
		if e != nil {
			return r, e
		}
		if e := m.ValidateSend(ctx, validationIntent); e != nil {
			if !errors.Is(e, ErrObsolete) {
				return r, e
			}
			r.State = "cancelled"
			r.LastError = e.Error()
			d.Body, _ = json.Marshal(r)
			_, save := m.DB.Put(ctx, d)
			return r, save
		}
	}
	r.State = "submitting"
	r.Owner = m.Owner
	r.LastError = ""
	d.Body, _ = json.Marshal(r)
	rev, err := m.DB.Put(ctx, d)
	if err != nil {
		return r, err
	}
	d.Revision = rev
	portIntent, err := cloneIntent(r.Intent)
	if err != nil {
		return r, err
	}
	receipt, sendErr := m.Port.Send(ctx, portIntent)
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if sendErr != nil {
		r.State = "unknown"
		if errors.Is(sendErr, ErrPermanent) {
			if receipt.Stage == StagePermanentFailure && verifyReceipt(r.Intent, &receipt) == nil {
				return m.confirm(settle, d, r, &receipt)
			}
			// A rejection error with no receipt is definitive Host evidence.
			// A simultaneous accepted/delivered or foreign receipt contradicts
			// it and must stay unresolved rather than erase a possible effect.
			if receipt.ID == "" && receipt.ProviderMessageID == "" && receipt.ProviderOperationID == "" && receipt.Stage == "" {
				r.State = "failed"
			}
		}
		r.LastError = sendErr.Error()
		d.Body, _ = json.Marshal(r)
		_, saveErr := m.DB.Put(settle, d)
		return r, errors.Join(sendErr, saveErr)
	}
	return m.confirm(settle, d, r, &receipt)
}
func (m *Manager) confirm(ctx context.Context, d storage.Document, r Record, candidate *Receipt) (Record, error) {
	verificationErr := verifyReceipt(r.Intent, candidate)
	if verificationErr == nil && r.Receipt != nil && candidate != nil {
		if r.Receipt.ProviderMessageID != "" && candidate.ProviderMessageID != "" && r.Receipt.ProviderMessageID != candidate.ProviderMessageID || r.Receipt.ProviderOperationID != "" && candidate.ProviderOperationID != "" && r.Receipt.ProviderOperationID != candidate.ProviderOperationID {
			verificationErr = errors.New("delivery: provider changed accepted identity")
		}
	}
	if err := verificationErr; err != nil {
		if r.State != "provider_accepted" {
			r.State = "unknown"
			r.Receipt = nil
		}
		r.LastError = err.Error()
		d.Body, _ = json.Marshal(r)
		_, save := m.DB.Put(ctx, d)
		return r, errors.Join(err, save)
	}
	r.Receipt = candidate
	r.LastError = ""
	var status error
	switch r.Receipt.Stage {
	case StageProviderAccepted:
		r.State = "provider_accepted"
		status = ErrPending
	case StagePermanentFailure:
		r.State = "failed"
		r.LastError = r.Receipt.Failure
		status = ErrPermanent
	default:
		r.State = "applied"
	}
	d.Body, _ = json.Marshal(r)
	_, err := m.DB.Put(ctx, d)
	return r, errors.Join(status, err)
}

func verifyReceipt(in Intent, r *Receipt) error {
	invalid := errors.New("delivery: unverified receipt")
	if r == nil || r.ID != in.ID || r.BodySHA256 != contextbuilder.Digest([]byte(in.Body)) || len(r.Evidence) > 256<<10 || len(r.Evidence) > 0 && !json.Valid(r.Evidence) {
		return invalid
	}
	if r.Stage == "" {
		if in.Target != nil || in.Payload != nil || r.ProviderMessageID == "" {
			return invalid
		}
		return nil
	}
	fingerprint, err := IntentFingerprint(in)
	if err != nil {
		return err
	}
	target, err := TargetFingerprint(in.Target)
	if err != nil {
		return err
	}
	if r.IntentSHA256 != fingerprint || r.TargetSHA256 != target || r.Transport == "" {
		return invalid
	}
	if in.Target != nil && r.Transport != in.Target.Transport {
		return invalid
	}
	switch r.Stage {
	case StageProviderAccepted:
		if r.ProviderMessageID == "" && r.ProviderOperationID == "" || r.Transport == "local-file" {
			return invalid
		}
	case StageDelivered:
		if r.ProviderMessageID == "" || r.Transport == "local-file" {
			return invalid
		}
	case StageLocalApplied:
		if r.ProviderMessageID == "" || r.Transport != "local-file" {
			return invalid
		}
	case StagePermanentFailure:
		if r.Failure == "" {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}

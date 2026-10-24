// Package interaction owns durable delivery intent and reconciled receipts.
// A Task's completion and a platform delivery are different facts.
package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/work"
)

var ErrObsolete = errors.New("delivery: intent revoked or obsolete")

var ErrUnknown = errors.New("delivery: unknown submission; lookup required")

type Intent struct {
	ID, SourceReceipt, TaskID, Kind, Body string
	GoalRevision                          uint64
	Scope                                 work.Scope
}
type Receipt struct {
	ID, ProviderMessageID, BodySHA256 string
	ObservedAt                        time.Time
}
type Lookup struct {
	Receipt      *Receipt
	ProvenAbsent bool
}
type Port interface {
	Send(context.Context, Intent) (Receipt, error)
	Lookup(context.Context, Intent) (Lookup, error)
}
type Record struct {
	Intent                  Intent
	State, Owner, LastError string
	Receipt                 *Receipt
}
type Manager struct {
	DB           storage.State
	Port         Port
	Owner        string
	Authorize    func(context.Context, work.Scope) error
	ValidateSend func(context.Context, Intent) error
}

func (m *Manager) Enqueue(ctx context.Context, in Intent) (Record, error) {
	if m.DB == nil || m.Port == nil || m.Owner == "" || m.Authorize == nil || in.ID == "" || in.Body == "" || in.SourceReceipt == "" {
		return Record{}, errors.New("delivery: complete binding and intent required")
	}
	if err := m.Authorize(ctx, in.Scope); err != nil {
		return Record{}, err
	}
	scope, err := in.Scope.Key()
	if err != nil {
		return Record{}, err
	}
	r := Record{Intent: in, State: "queued"}
	err = m.DB.Transact(ctx, func(tx storage.Tx) error {
		old, e := tx.Get(ctx, "outbox", scope, in.ID)
		if e == nil {
			if e = json.Unmarshal(old.Body, &r); e != nil {
				return e
			}
			a, _ := json.Marshal(r.Intent)
			b, _ := json.Marshal(in)
			if string(a) != string(b) {
				return storage.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		raw, _ := json.Marshal(r)
		_, e = tx.Put(ctx, storage.Document{Namespace: "outbox", Scope: scope, ID: in.ID, Body: raw})
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
		if r.Intent.Scope != scope {
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
	if r.Intent.Scope != scope {
		return r, work.ErrScope
	}
	if r.State == "applied" || r.State == "cancelled" {
		return r, nil
	}
	if r.State == "submitting" && r.Owner == m.Owner {
		return r, ErrUnknown
	}
	if r.State == "submitting" || r.State == "unknown" {
		lookup, e := m.Port.Lookup(ctx, r.Intent)
		if e != nil {
			return r, e
		}
		if lookup.Receipt != nil {
			r.Receipt = lookup.Receipt
			return m.confirm(ctx, d, r)
		}
		if !lookup.ProvenAbsent {
			return r, ErrUnknown
		}
	}
	if m.ValidateSend != nil {
		if e := m.ValidateSend(ctx, r.Intent); e != nil {
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
	receipt, sendErr := m.Port.Send(ctx, r.Intent)
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if sendErr != nil {
		r.State = "unknown"
		r.LastError = sendErr.Error()
		d.Body, _ = json.Marshal(r)
		_, saveErr := m.DB.Put(settle, d)
		return r, errors.Join(sendErr, saveErr)
	}
	r.Receipt = &receipt
	return m.confirm(settle, d, r)
}
func (m *Manager) confirm(ctx context.Context, d storage.Document, r Record) (Record, error) {
	if r.Receipt == nil || r.Receipt.ID != r.Intent.ID || r.Receipt.ProviderMessageID == "" || r.Receipt.BodySHA256 != contextbuilder.Digest([]byte(r.Intent.Body)) {
		verificationErr := errors.New("delivery: unverified receipt")
		r.State = "unknown"
		r.LastError = verificationErr.Error()
		r.Receipt = nil
		d.Body, _ = json.Marshal(r)
		_, saveErr := m.DB.Put(ctx, d)
		return r, errors.Join(verificationErr, saveErr)
	}
	r.State = "applied"
	r.LastError = ""
	d.Body, _ = json.Marshal(r)
	_, err := m.DB.Put(ctx, d)
	return r, err
}

// Local writes a real immutable Markdown delivery, not an IM claim.
type Local struct{ Files storage.Artifacts }

func (l Local) path(in Intent) string {
	key, _ := in.Scope.Key()
	return "deliveries/" + contextbuilder.Digest([]byte(key+"/"+in.ID)) + ".md"
}
func (l Local) Lookup(ctx context.Context, in Intent) (Lookup, error) {
	d, err := l.Files.Read(ctx, l.path(in))
	if errors.Is(err, fs.ErrNotExist) {
		return Lookup{ProvenAbsent: true}, nil
	}
	if err != nil {
		return Lookup{}, err
	}
	if d.Body != in.Body {
		return Lookup{}, storage.ErrConflict
	}
	return Lookup{Receipt: &Receipt{ID: in.ID, ProviderMessageID: "local-file/" + d.Path, BodySHA256: d.SHA256, ObservedAt: time.Now().UTC()}}, nil
}
func (l Local) Send(ctx context.Context, in Intent) (Receipt, error) {
	old, err := l.Lookup(ctx, in)
	if err != nil {
		return Receipt{}, err
	}
	if old.Receipt != nil {
		return *old.Receipt, nil
	}
	if _, err = l.Files.Write(ctx, l.path(in), "", in.Body); err != nil {
		return Receipt{}, err
	}
	out, err := l.Lookup(ctx, in)
	if err != nil {
		return Receipt{}, err
	}
	if out.Receipt == nil {
		return Receipt{}, ErrUnknown
	}
	return *out.Receipt, nil
}

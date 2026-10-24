// Package contextbuilder assembles three distinct, source-fenced views. Frozen
// input is evidence, not a grant to read revoked material on a later attempt.
package contextbuilder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/work"
)

var ErrRevoked = errors.New("context: source withdrawn or revision changed")
var ErrAudience = errors.New("context: evidence not authorized for target audience")

type Source struct {
	ID, Actor, Text string
	At              time.Time
	Withdrawn       bool
}
type Ref struct {
	ID, SHA256 string
	Revision   uint64
}
type View string

const (
	Front       View = "front"
	Execution   View = "execution"
	Maintenance View = "maintenance"
)

type ScopedRef struct {
	Scope work.Scope
	Ref   Ref
}

// AudiencePolicy authorizes an exact evidence transfer. It must not infer
// audience membership from a scene's textual name.
type AudiencePolicy func(context.Context, work.Scope, work.Scope, View) error

type Option func(*Builder)

func WithAudiencePolicy(policy AudiencePolicy) Option {
	return func(b *Builder) { b.audience = policy }
}

type OwnerProjection struct {
	Path, SHA256 string
	Evidence     []ScopedRef
}
type Frame struct {
	OwnerEvidence    []ScopedRef
	OwnerProjections []OwnerProjection
	OwnerEpoch       uint64
	Materials        []storage.File
	View             View
	Scope            work.Scope
	Current          *Source
	Sources          []Source
	Refs             []Ref
	Tasks            []work.Task
	OwnerFiles       []storage.File
	Gaps             []string
}

// Tasks is the read-only business projection needed to assemble model context.
// The Context builder never changes a Task or Goal.
type Tasks interface {
	Get(context.Context, work.Scope, string) (work.Task, error)
	List(context.Context, work.Scope) ([]work.Task, error)
}

type Builder struct {
	db        storage.State
	md        storage.Artifacts
	tasks     Tasks
	authorize func(context.Context, work.Scope, View) error
	audience  AudiencePolicy
}

func New(db storage.State, md storage.Artifacts, tasks Tasks, authorize func(context.Context, work.Scope, View) error, options ...Option) (*Builder, error) {
	if db == nil || md == nil || tasks == nil || authorize == nil {
		return nil, errors.New("context: stores, task reader and authorization required")
	}
	b := &Builder{db: db, md: md, tasks: tasks, authorize: authorize}
	for _, option := range options {
		if option != nil {
			option(b)
		}
	}
	return b, nil
}

// CheckAudience applies mandatory owner boundaries before the optional host
// policy. Call this before loading evidence for a target model view.
func (b *Builder) CheckAudience(ctx context.Context, source, target work.Scope, view View) error {
	if _, err := source.Key(); err != nil {
		return err
	}
	if _, err := target.Key(); err != nil {
		return err
	}
	if source.Workspace != target.Workspace || source.Tenant != target.Tenant || source.Employee != target.Employee || source.Requester != target.Requester {
		return ErrAudience
	}
	if view != Front && view != Execution && view != Maintenance {
		return ErrAudience
	}
	if b.audience != nil {
		return b.audience(ctx, source, target, view)
	}
	if source != target {
		return ErrAudience
	}
	return nil
}
func Digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func OwnerKey(scope work.Scope) string {
	scope.Scene = ""
	raw, _ := json.Marshal(scope)
	return Digest(raw)
}
func (b *Builder) Admit(ctx context.Context, scope work.Scope, s Source) (Source, error) {
	if err := b.authorize(ctx, scope, Front); err != nil {
		return Source{}, err
	}
	key, err := scope.Key()
	if err != nil {
		return Source{}, err
	}
	if s.ID == "" || s.Actor != scope.Requester || s.Text == "" || s.Withdrawn {
		return Source{}, work.ErrScope
	}
	if s.At.IsZero() {
		s.At = time.Now().UTC()
	}
	err = b.db.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "sources", key, s.ID)
		if e == nil {
			var old Source
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			if old.ID != s.ID || old.Actor != s.Actor || old.Text != s.Text {
				return storage.ErrConflict
			}
			if old.Withdrawn {
				return ErrRevoked
			}
			s = old
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		raw, e := json.Marshal(s)
		if e != nil {
			return e
		}
		_, e = tx.Put(ctx, storage.Document{Namespace: "sources", Scope: key, ID: s.ID, Body: raw})
		return e
	})
	return s, err
}
func (b *Builder) Source(ctx context.Context, scope work.Scope, id string) (Source, Ref, error) {
	if err := b.authorize(ctx, scope, Front); err != nil {
		return Source{}, Ref{}, err
	}
	key, err := scope.Key()
	if err != nil {
		return Source{}, Ref{}, err
	}
	d, err := b.db.Get(ctx, "sources", key, id)
	if err != nil {
		return Source{}, Ref{}, err
	}
	var s Source
	if err = json.Unmarshal(d.Body, &s); err != nil {
		return s, Ref{}, err
	}
	if s.Withdrawn || s.Actor != scope.Requester || s.ID != id {
		return Source{}, Ref{}, ErrRevoked
	}
	return s, Ref{ID: id, Revision: d.Revision, SHA256: Digest(d.Body)}, nil
}
func (b *Builder) Revoke(ctx context.Context, scope work.Scope, id string) error {
	if err := b.authorize(ctx, scope, Maintenance); err != nil {
		return err
	}
	key, err := scope.Key()
	if err != nil {
		return err
	}
	return b.db.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "sources", key, id)
		if e != nil {
			return e
		}
		var s Source
		if e = json.Unmarshal(d.Body, &s); e != nil {
			return e
		}
		s.Withdrawn = true
		d.Body, e = json.Marshal(s)
		if e != nil {
			return e
		}
		_, e = tx.Put(ctx, d)
		return e
	})
}
func (b *Builder) Build(ctx context.Context, scope work.Scope, view View, currentID, taskID string) (Frame, error) {
	frame := Frame{View: view, Scope: scope, Sources: []Source{}, Refs: []Ref{}, Tasks: []work.Task{}, OwnerFiles: []storage.File{}, Gaps: []string{}}
	if view != Front && view != Execution && view != Maintenance {
		return frame, errors.New("context: unknown view")
	}
	if err := b.authorize(ctx, scope, view); err != nil {
		return frame, err
	}
	key, err := scope.Key()
	if err != nil {
		return frame, err
	}
	frame.OwnerEpoch, err = b.ownerEpoch(ctx, scope)
	if err != nil {
		return frame, err
	}
	if currentID != "" {
		s, _, e := b.Source(ctx, scope, currentID)
		if e != nil {
			return frame, e
		}
		frame.Current = &s
	}
	if view == Execution {
		task, e := b.tasks.Get(ctx, scope, taskID)
		if e != nil {
			return frame, e
		}
		frame.Tasks = []work.Task{task}
		seen := map[string]bool{}
		for _, source := range task.Sources {
			if seen[source.Receipt] {
				continue
			}
			s, ref, e := b.Source(ctx, scope, source.Receipt)
			if e != nil {
				return frame, e
			}
			if s.Actor != source.Actor || s.Text != source.Text {
				return frame, ErrRevoked
			}
			frame.Sources = append(frame.Sources, s)
			frame.Refs = append(frame.Refs, ref)
			seen[source.Receipt] = true
		}
	} else {
		docs, e := b.db.List(ctx, "sources", key)
		if e != nil {
			return frame, e
		}
		for _, d := range docs {
			var s Source
			if e = json.Unmarshal(d.Body, &s); e != nil {
				return frame, e
			}
			if s.Withdrawn || s.Actor != scope.Requester {
				continue
			}
			frame.Sources = append(frame.Sources, s)
		}
		sort.Slice(frame.Sources, func(i, j int) bool {
			if frame.Sources[i].At.Equal(frame.Sources[j].At) {
				return frame.Sources[i].ID < frame.Sources[j].ID
			}
			return frame.Sources[i].At.Before(frame.Sources[j].At)
		})
		if len(frame.Sources) > 20 {
			frame.Sources = frame.Sources[len(frame.Sources)-20:]
			frame.Gaps = append(frame.Gaps, "older sources omitted as whole records")
		}
		if frame.Current != nil {
			found := false
			for _, s := range frame.Sources {
				if s.ID == currentID {
					found = true
				}
			}
			if !found {
				frame.Sources = append(frame.Sources, *frame.Current)
			}
		}
		for _, s := range frame.Sources {
			_, ref, e := b.Source(ctx, scope, s.ID)
			if e != nil {
				return frame, e
			}
			frame.Refs = append(frame.Refs, ref)
		}
		if view == Front {
			frame.Tasks, e = b.tasks.List(ctx, scope)
			if e != nil {
				return frame, e
			}
		}
		for _, name := range []string{"context", "memory", "tone", "attention"} {
			path := "owners/" + OwnerKey(scope) + "/" + name + ".md"
			d, e := b.md.Read(ctx, path)
			if errors.Is(e, fs.ErrNotExist) {
				frame.Gaps = append(frame.Gaps, name+" not written")
				continue
			}
			if e != nil {
				return frame, e
			}
			role := name
			if role == "context" {
				role = "summary"
			}
			meta, e := b.db.Get(ctx, "owner-documents", OwnerKey(scope), role)
			if e == nil {
				var doc struct {
					Path, SHA256 string
					OwnerEpoch   uint64
					Invalidated  bool
					Evidence     []struct {
						Scope work.Scope
						Ref   Ref
						Quote string
					}
				}
				if e = json.Unmarshal(meta.Body, &doc); e != nil {
					return frame, e
				}
				if doc.Invalidated || doc.OwnerEpoch != frame.OwnerEpoch || doc.Path != path || doc.SHA256 != d.SHA256 || len(doc.Evidence) == 0 {
					frame.Gaps = append(frame.Gaps, name+" projection pending or invalidated")
					continue
				}
				valid := true
				refs := []ScopedRef{}
				for _, ev := range doc.Evidence {
					if ev.Quote == "" || b.CheckAudience(ctx, ev.Scope, scope, view) != nil {
						valid = false
						break
					}
					raw, ref, e := b.Source(ctx, ev.Scope, ev.Ref.ID)
					if e != nil || ref != ev.Ref || !strings.Contains(raw.Text, ev.Quote) {
						valid = false
						break
					}
					refs = append(refs, ScopedRef{Scope: ev.Scope, Ref: ref})
				}
				if !valid {
					frame.Gaps = append(frame.Gaps, name+" evidence revoked or unavailable")
					continue
				}
				frame.OwnerEvidence = append(frame.OwnerEvidence, refs...)
				frame.OwnerProjections = append(frame.OwnerProjections, OwnerProjection{Path: d.Path, SHA256: d.SHA256, Evidence: refs})
			} else if !errors.Is(e, storage.ErrNotFound) {
				return frame, e
			} else {
				frame.Gaps = append(frame.Gaps, name+" metadata unavailable")
				continue
			}
			frame.OwnerFiles = append(frame.OwnerFiles, d)
		}
	}
	return frame, b.Revalidate(ctx, frame)
}
func (b *Builder) Revalidate(ctx context.Context, f Frame) error {
	if err := b.authorize(ctx, f.Scope, f.View); err != nil {
		return err
	}
	for _, ref := range f.Refs {
		_, now, err := b.Source(ctx, f.Scope, ref.ID)
		if err != nil {
			return err
		}
		if now != ref {
			return ErrRevoked
		}
	}
	for _, ev := range f.OwnerEvidence {
		if err := b.CheckAudience(ctx, ev.Scope, f.Scope, f.View); err != nil {
			return err
		}
		_, ref, e := b.Source(ctx, ev.Scope, ev.Ref.ID)
		if e != nil {
			return e
		}
		if ref != ev.Ref {
			return ErrRevoked
		}
	}
	for _, file := range f.OwnerFiles {
		matched := false
		for _, projection := range f.OwnerProjections {
			if projection.Path != file.Path || projection.SHA256 != file.SHA256 || Digest([]byte(file.Body)) != file.SHA256 || len(projection.Evidence) == 0 {
				continue
			}
			for _, ev := range projection.Evidence {
				if err := b.CheckAudience(ctx, ev.Scope, f.Scope, f.View); err != nil {
					return err
				}
				_, now, err := b.Source(ctx, ev.Scope, ev.Ref.ID)
				if err != nil {
					return err
				}
				if now != ev.Ref {
					return ErrRevoked
				}
			}
			matched = true
			break
		}
		if !matched {
			return ErrAudience
		}
	}
	epoch, err := b.ownerEpoch(ctx, f.Scope)
	if err != nil {
		return err
	}
	if epoch != f.OwnerEpoch {
		return ErrRevoked
	}

	return nil
}

func (b *Builder) ownerEpoch(ctx context.Context, scope work.Scope) (uint64, error) {
	d, err := b.db.Get(ctx, "owner-epochs", OwnerKey(scope), "revocation")
	if errors.Is(err, storage.ErrNotFound) {
		return 0, nil
	}
	return d.Revision, err
}

// ResetOwner revokes all frozen views for this owner across scenes. Updating a
// Markdown projection alone does not withdraw the previously authorized copy.
func (b *Builder) ResetOwner(ctx context.Context, scope work.Scope) error {
	if err := b.authorize(ctx, scope, Maintenance); err != nil {
		return err
	}
	if _, err := scope.Key(); err != nil {
		return err
	}
	return b.db.Transact(ctx, func(tx storage.Tx) error {
		d, err := tx.Get(ctx, "owner-epochs", OwnerKey(scope), "revocation")
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		d.Body = []byte(`{"revoked":true}`)
		_, err = tx.Put(ctx, d)
		return err
	})
}

func (b *Builder) OwnerEpoch(ctx context.Context, scope work.Scope) (uint64, error) {
	if err := b.authorize(ctx, scope, Maintenance); err != nil {
		return 0, err
	}
	return b.ownerEpoch(ctx, scope)
}

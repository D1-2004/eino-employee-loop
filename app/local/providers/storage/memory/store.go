// Package memory supplies independent, process-local reference storage. It is
// useful for embedding and conformance; it does not survive process death.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/kernel/workflow"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	"github.com/cloudwego/eino/adk"
)

type recordKey struct{ namespace, scope, id string }
type checkpointKey struct{ scope, version, id string }
type Store struct {
	mu          sync.Mutex
	closed      bool
	documents   map[recordKey]storage.Document
	events      map[recordKey]storage.Event
	sequence    int64
	checkpoints map[checkpointKey][]byte
	leases      map[workflow.Key]*lease
	sessions    map[workflow.Key][]byte
}

func New() *Store {
	return &Store{documents: make(map[recordKey]storage.Document), events: make(map[recordKey]storage.Event), checkpoints: make(map[checkpointKey][]byte), leases: make(map[workflow.Key]*lease), sessions: make(map[workflow.Key][]byte)}
}

var _ storage.State = (*Store)(nil)

func valid(namespace, scope, id string) error {
	if namespace == "" || scope == "" || id == "" || len(namespace) > 128 || len(scope) > 4096 || len(id) > 1024 {
		return errors.New("memory: complete bounded namespace/scope/id required")
	}
	return nil
}
func copyDoc(d storage.Document) storage.Document {
	d.Body = append(json.RawMessage(nil), d.Body...)
	return d
}
func copyEvent(e storage.Event) storage.Event {
	e.Body = append(json.RawMessage(nil), e.Body...)
	return e
}
func (s *Store) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed {
		return fs.ErrClosed
	}
	return nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.leases) > 0 {
		return storage.ErrBusy
	}
	s.closed = true
	return nil
}
func (s *Store) Get(ctx context.Context, n, scope, id string) (storage.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return storage.Document{}, e
	}
	return get(s.documents, n, scope, id)
}
func get(docs map[recordKey]storage.Document, n, scope, id string) (storage.Document, error) {
	if e := valid(n, scope, id); e != nil {
		return storage.Document{}, e
	}
	d, ok := docs[recordKey{n, scope, id}]
	if !ok {
		return storage.Document{Namespace: n, Scope: scope, ID: id}, storage.ErrNotFound
	}
	return copyDoc(d), nil
}
func (s *Store) Put(ctx context.Context, d storage.Document) (uint64, error) {
	var rev uint64
	err := s.Transact(ctx, func(tx storage.Tx) error { var e error; rev, e = tx.Put(ctx, d); return e })
	return rev, err
}
func (s *Store) List(ctx context.Context, n, scope string) ([]storage.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	if e := valid(n, scope, "list"); e != nil {
		return nil, e
	}
	out := []storage.Document{}
	for k, d := range s.documents {
		if k.namespace == n && k.scope == scope {
			out = append(out, copyDoc(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *Store) Events(ctx context.Context, n, scope string, after int64, limit int) ([]storage.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	if e := valid(n, scope, "events"); e != nil {
		return nil, e
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("memory: event page limit must be 1..1000")
	}
	out := []storage.Event{}
	for k, e := range s.events {
		if k.namespace == n && k.scope == scope && e.Sequence > after {
			out = append(out, copyEvent(e))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type transaction struct {
	documents map[recordKey]storage.Document
	events    map[recordKey]storage.Event
	sequence  int64
	active    bool
}

func (s *Store) Transact(ctx context.Context, fn func(storage.Tx) error) error {
	if fn == nil {
		return errors.New("memory: transaction callback required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return e
	}
	tx := &transaction{documents: make(map[recordKey]storage.Document, len(s.documents)), events: make(map[recordKey]storage.Event, len(s.events)), sequence: s.sequence, active: true}
	for k, d := range s.documents {
		tx.documents[k] = copyDoc(d)
	}
	for k, e := range s.events {
		tx.events[k] = copyEvent(e)
	}
	defer func() { tx.active = false }()
	if e := fn(tx); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	s.documents, s.events, s.sequence = tx.documents, tx.events, tx.sequence
	return nil
}
func (t *transaction) check(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if !t.active {
		return fs.ErrClosed
	}
	return nil
}
func (t *transaction) Get(ctx context.Context, n, scope, id string) (storage.Document, error) {
	if e := t.check(ctx); e != nil {
		return storage.Document{}, e
	}
	return get(t.documents, n, scope, id)
}
func (t *transaction) Put(ctx context.Context, d storage.Document) (uint64, error) {
	if e := t.check(ctx); e != nil {
		return 0, e
	}
	if e := valid(d.Namespace, d.Scope, d.ID); e != nil {
		return 0, e
	}
	if len(d.Body) > 8<<20 || !json.Valid(d.Body) {
		return 0, errors.New("memory: bounded JSON document required")
	}
	k := recordKey{d.Namespace, d.Scope, d.ID}
	old, exists := t.documents[k]
	if (d.Revision == 0 && exists) || (d.Revision > 0 && (!exists || old.Revision != d.Revision)) || d.Revision >= 1<<63-1 {
		return 0, storage.ErrConflict
	}
	d.Revision++
	t.documents[k] = copyDoc(d)
	return d.Revision, nil
}
func (t *transaction) Append(ctx context.Context, e storage.Event) (int64, error) {
	if err := t.check(ctx); err != nil {
		return 0, err
	}
	if err := valid(e.Namespace, e.Scope, e.ID); err != nil {
		return 0, err
	}
	if e.Kind == "" || len(e.Body) > 8<<20 || !json.Valid(e.Body) {
		return 0, errors.New("memory: bounded typed event required")
	}
	k := recordKey{e.Namespace, e.Scope, e.ID}
	if old, ok := t.events[k]; ok {
		if old.Kind != e.Kind || string(old.Body) != string(e.Body) {
			return 0, storage.ErrConflict
		}
		return old.Sequence, nil
	}
	t.sequence++
	e.Sequence = t.sequence
	e.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	t.events[k] = copyEvent(e)
	return e.Sequence, nil
}

type checkpoints struct {
	store          *Store
	scope, version string
}

func (s *Store) CheckpointStore(scope, version string) (adk.CheckPointStore, error) {
	if e := valid("checkpoint", scope, version); e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fs.ErrClosed
	}
	return &checkpoints{s, scope, version}, nil
}
func (c *checkpoints) Get(ctx context.Context, id string) ([]byte, bool, error) {
	s := c.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, false, e
	}
	if e := valid("checkpoint", c.scope, id); e != nil {
		return nil, false, e
	}
	b, ok := s.checkpoints[checkpointKey{c.scope, c.version, id}]
	return append([]byte(nil), b...), ok, nil
}
func (c *checkpoints) Set(ctx context.Context, id string, b []byte) error {
	s := c.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return e
	}
	if e := valid("checkpoint", c.scope, id); e != nil {
		return e
	}
	if len(b) == 0 || len(b) > 64<<20 {
		return errors.New("memory: checkpoint size must be 1 byte..64 MiB")
	}
	s.checkpoints[checkpointKey{c.scope, c.version, id}] = append([]byte(nil), b...)
	return nil
}
func (c *checkpoints) Delete(ctx context.Context, id string) error {
	s := c.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return e
	}
	if e := valid("checkpoint", c.scope, id); e != nil {
		return e
	}
	delete(s.checkpoints, checkpointKey{c.scope, c.version, id})
	return nil
}
func (s *Store) Acquire(ctx context.Context, key workflow.Key) (workflow.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return nil, e
	}
	for _, v := range []string{key.Workspace, key.Tenant, key.Employee, key.Scene, key.Lane, key.Subject} {
		if v == "" {
			return nil, workflow.ErrDenied
		}
	}
	if s.leases[key] != nil {
		return nil, workflow.ErrBusy
	}
	l := &lease{store: s, key: key}
	s.leases[key] = l
	return l, nil
}

type lease struct {
	store    *Store
	key      workflow.Key
	loaded   bool
	revision uint64
}

func (l *lease) Load(ctx context.Context) (workflow.Snapshot, error) {
	s := l.store
	s.mu.Lock()
	defer s.mu.Unlock()
	var out workflow.Snapshot
	if e := s.check(ctx); e != nil {
		return out, e
	}
	if s.leases[l.key] != l {
		return out, workflow.ErrBusy
	}
	raw := s.sessions[l.key]
	if raw != nil {
		if e := json.Unmarshal(raw, &out); e != nil {
			return out, e
		}
		if out.Key != l.key || out.Revision == 0 {
			return out, workflow.ErrConflict
		}
	}
	l.loaded = true
	l.revision = out.Revision
	return out, nil
}
func (l *lease) Save(ctx context.Context, v workflow.Snapshot) error {
	s := l.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return e
	}
	if s.leases[l.key] != l || !l.loaded {
		return workflow.ErrBusy
	}
	if v.Key != l.key || v.Revision != l.revision {
		return workflow.ErrConflict
	}
	v.Revision++
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	s.sessions[l.key] = raw
	l.revision = v.Revision
	return nil
}
func (l *lease) Release() error {
	s := l.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leases[l.key] == l {
		delete(s.leases, l.key)
	}
	return nil
}

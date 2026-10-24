package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/D1-2004/eino-employee-loop/employee/kernel/workflow"
	"github.com/gofrs/flock"
)

var _ workflow.Store = (*Store)(nil)

// Acquire uses an OS advisory lock. The kernel releases it on process death;
// locks are never stolen by age, PID guesses or a second active writer.
func (s *Store) Acquire(ctx context.Context, key workflow.Key) (workflow.Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, v := range []string{key.Workspace, key.Tenant, key.Employee, key.Scene, key.Lane, key.Subject} {
		if v == "" {
			return nil, workflow.ErrDenied
		}
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	id := hash(raw)
	dir := filepath.Join(s.dir, "locks")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "session-"+id+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	lock := flock.New(path)
	ok, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if !ok {
		_ = lock.Close()
		return nil, workflow.ErrBusy
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = lock.Close()
		return nil, errors.New("sqlite: store closed")
	}
	s.active++
	s.mu.Unlock()
	return &sessionLease{store: s, key: key, id: id, lock: lock}, nil
}

type sessionLease struct {
	mu       sync.Mutex
	store    *Store
	key      workflow.Key
	id       string
	lock     *flock.Flock
	loaded   bool
	revision uint64
}

func (l *sessionLease) Load(ctx context.Context) (workflow.Snapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out workflow.Snapshot
	if l.lock == nil {
		return out, workflow.ErrBusy
	}
	d, err := l.store.Get(ctx, "workflow-session", l.id, "state")
	if errors.Is(err, ErrNotFound) {
		l.loaded = true
		l.revision = 0
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(d.Body, &out); err != nil {
		return out, err
	}
	if out.Key != l.key || out.Revision != d.Revision || out.Revision == 0 {
		return out, workflow.ErrConflict
	}
	l.loaded = true
	l.revision = d.Revision
	return out, nil
}
func (l *sessionLease) Save(ctx context.Context, snapshot workflow.Snapshot) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lock == nil || !l.loaded {
		return workflow.ErrBusy
	}
	if snapshot.Key != l.key || snapshot.Revision != l.revision {
		return workflow.ErrConflict
	}
	snapshot.Revision++
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	revision, err := l.store.Put(ctx, Document{Namespace: "workflow-session", Scope: l.id, ID: "state", Revision: l.revision, Body: raw})
	if errors.Is(err, ErrConflict) {
		return workflow.ErrConflict
	}
	if err != nil {
		return err
	}
	l.revision = revision
	return nil
}
func (l *sessionLease) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	l.store.mu.Lock()
	l.store.active--
	l.store.mu.Unlock()
	return err
}

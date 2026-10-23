// Package filestore implements a local, crash-safe workflow session Store.
// Locks are never automatically stolen; this is not a distributed lease.
package filestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/D1-2004/eino-employee-loop/workflow"
)

// Store uses a private directory owned by this local assembly.
type Store struct{ Dir string }

func (s Store) Acquire(ctx context.Context, key workflow.Key) (workflow.Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Dir == "" {
		return nil, errors.New("filestore: missing private directory")
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	base := filepath.Join(s.Dir, hex.EncodeToString(hash[:]))
	file, err := os.OpenFile(base+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil, workflow.ErrBusy
	}
	if err != nil {
		return nil, err
	}
	// A diagnostic PID is not authority to remove another process's lock.
	if _, err = fmt.Fprintf(file, "pid=%d\n", os.Getpid()); err != nil {
		_ = file.Close()
		_ = os.Remove(base + ".lock")
		return nil, err
	}
	return &lease{base: base, key: key, lock: file}, nil
}

type lease struct {
	mu       sync.Mutex
	base     string
	key      workflow.Key
	lock     *os.File
	revision uint64
	loaded   bool
}

func (l *lease) Load(ctx context.Context) (workflow.Snapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out workflow.Snapshot
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if l.lock == nil {
		return out, workflow.ErrBusy
	}
	file, err := os.Open(l.base + ".json")
	if errors.Is(err, os.ErrNotExist) {
		l.loaded, l.revision = true, 0
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer file.Close()
	d := json.NewDecoder(io.LimitReader(file, 64<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&out); err != nil {
		return out, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return out, errors.New("filestore: trailing or oversized state")
	}
	if out.Key != l.key || out.Revision == 0 {
		return out, workflow.ErrConflict
	}
	l.loaded, l.revision = true, out.Revision
	return out, nil
}

func (l *lease) Save(ctx context.Context, snapshot workflow.Snapshot) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
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
	if len(raw) >= 64<<20 {
		return errors.New("filestore: session exceeds 64 MiB; archive whole completed wakes")
	}
	temp, err := os.CreateTemp(filepath.Dir(l.base), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), l.base+".json"); err != nil {
		return err
	}
	// Sync the directory entry before reporting a durable checkpoint.
	dir, err := os.Open(filepath.Dir(l.base))
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	l.revision = snapshot.Revision
	return nil
}

func (l *lease) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	return errors.Join(err, os.Remove(l.base+".lock"))
}

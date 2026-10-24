package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"strings"
	"sync"

	"github.com/D1-2004/eino-employee-loop/storage"
)

type Artifacts struct {
	mu     sync.Mutex
	closed bool
	files  map[string]storage.File
}

func NewArtifacts() *Artifacts { return &Artifacts{files: make(map[string]storage.File)} }

var _ storage.Artifacts = (*Artifacts)(nil)

func validPath(name string) error {
	if !fs.ValidPath(name) || name == "." || !strings.HasSuffix(name, ".md") || len(name) > 1024 || strings.HasPrefix(name, ".locks/") {
		return errors.New("memory: relative .md path required")
	}
	return nil
}
func (a *Artifacts) check(ctx context.Context, name string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if a.closed {
		return fs.ErrClosed
	}
	return validPath(name)
}
func (a *Artifacts) Close() error { a.mu.Lock(); defer a.mu.Unlock(); a.closed = true; return nil }
func (a *Artifacts) Read(ctx context.Context, name string) (storage.File, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.check(ctx, name); e != nil {
		return storage.File{}, e
	}
	d, ok := a.files[name]
	if !ok {
		return storage.File{Path: name}, fs.ErrNotExist
	}
	return d, nil
}
func (a *Artifacts) Write(ctx context.Context, name, expected, body string) (storage.File, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.check(ctx, name); e != nil {
		return storage.File{}, e
	}
	if len(body) > 8<<20 {
		return storage.File{}, errors.New("memory: file exceeds 8 MiB")
	}
	old, exists := a.files[name]
	if (!exists && expected != "") || (exists && (expected == "" || old.SHA256 != expected)) {
		return storage.File{}, storage.ErrArtifactConflict
	}
	sum := sha256.Sum256([]byte(body))
	d := storage.File{Path: name, Body: body, SHA256: hex.EncodeToString(sum[:])}
	a.files[name] = d
	return d, nil
}

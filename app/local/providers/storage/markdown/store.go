// Package markdown manages private, human-readable workspace files. SQLite
// remains the authority when these files are projections of business state.
package markdown

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	"github.com/gofrs/flock"
)

var ErrConflict = storage.ErrArtifactConflict

const maxBytes = 8 << 20

type Store struct {
	root   *os.Root
	dir    string
	mu     sync.RWMutex
	closed bool
}
type Document = storage.File

var _ storage.Artifacts = (*Store)(nil)

func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("markdown: private workspace required")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	if err = root.MkdirAll(".locks", 0700); err != nil {
		_ = root.Close()
		return nil, err
	}
	return &Store{root: root, dir: absolute}, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.root.Close()
}
func valid(name string) error {
	if !fs.ValidPath(name) || name == "." || !strings.HasSuffix(name, ".md") || len(name) > 1024 || strings.HasPrefix(name, ".locks/") {
		return errors.New("markdown: relative .md path required")
	}
	return nil
}
func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func (s *Store) Read(ctx context.Context, name string) (Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return Document{}, fs.ErrClosed
	}
	return s.read(ctx, name)
}
func (s *Store) read(ctx context.Context, name string) (Document, error) {
	d := Document{Path: name}
	if err := ctx.Err(); err != nil {
		return d, err
	}
	if err := valid(name); err != nil {
		return d, err
	}
	f, err := s.root.Open(name)
	if err != nil {
		return d, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return d, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return d, errors.New("markdown: bounded regular file required")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return d, err
	}
	if len(raw) > maxBytes {
		return d, errors.New("markdown: file exceeds 8 MiB")
	}
	d.Body = string(raw)
	d.SHA256 = digest(raw)
	return d, nil
}

// Write uses a content-hash CAS and an OS lock shared by cooperating writers.
// An empty expected hash is create-only. os.Root prevents escaping via symlinks.
func (s *Store) Write(ctx context.Context, name, expected, body string) (Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return Document{}, fs.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	if err := valid(name); err != nil {
		return Document{}, err
	}
	if len(body) > maxBytes {
		return Document{}, errors.New("markdown: file exceeds 8 MiB")
	}
	lockName := filepath.Join(s.dir, ".locks", digest([]byte(name))+".lock")
	f, err := os.OpenFile(lockName, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Document{}, err
	}
	if err = f.Close(); err != nil {
		return Document{}, err
	}
	lock := flock.New(lockName)
	ok, err := lock.TryLock()
	if err != nil {
		return Document{}, err
	}
	defer lock.Close()
	if !ok {
		return Document{}, ErrConflict
	}
	current, err := s.read(ctx, name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Document{}, err
	}
	if errors.Is(err, fs.ErrNotExist) {
		if expected != "" {
			return Document{}, ErrConflict
		}
	} else if expected == "" || current.SHA256 != expected {
		return Document{}, ErrConflict
	}
	dir := path.Dir(name)
	if err = s.root.MkdirAll(dir, 0700); err != nil {
		return Document{}, err
	}
	nonce := make([]byte, 12)
	if _, err = rand.Read(nonce); err != nil {
		return Document{}, err
	}
	temp := path.Join(dir, ".eel-"+hex.EncodeToString(nonce)+".tmp")
	target, err := s.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Document{}, err
	}
	defer s.root.Remove(temp)
	if _, err = io.WriteString(target, body); err != nil {
		_ = target.Close()
		return Document{}, err
	}
	if err = target.Sync(); err != nil {
		_ = target.Close()
		return Document{}, err
	}
	if err = target.Close(); err != nil {
		return Document{}, err
	}
	if err = ctx.Err(); err != nil {
		return Document{}, err
	}
	if err = s.root.Rename(temp, name); err != nil {
		return Document{}, err
	}
	directory, err := s.root.Open(dir)
	if err != nil {
		return Document{}, err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return Document{}, err
	}
	if closeErr != nil {
		return Document{}, closeErr
	}
	return s.read(ctx, name)
}

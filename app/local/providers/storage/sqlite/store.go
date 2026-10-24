// Package sqlite provides local transactional state, Eino checkpoints and
// workflow sessions. Business effects remain in the calling Host transaction.
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	_ "modernc.org/sqlite"
)

var (
	ErrConflict = storage.ErrConflict
	ErrNotFound = storage.ErrNotFound
	ErrBusy     = storage.ErrBusy
)

const maxDocumentBytes = 8 << 20

type Store struct {
	db     *sql.DB
	dir    string
	mu     sync.Mutex
	closed bool
	active int
}

// Open uses a filesystem database, WAL, foreign keys, bounded busy waiting and
// immediate write transactions. No transaction is held during model/tool I/O.
func Open(ctx context.Context, dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("sqlite: private state directory required")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(absolute, "state.sqlite")
	if info, e := os.Lstat(path); e == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, errors.New("sqlite: state file must be regular")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	for _, p := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(FULL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	s := &Store{db: db, dir: absolute}
	if err = s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active > 0 {
		return ErrBusy
	}
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}
func (s *Store) Path() string { return filepath.Join(s.dir, "state.sqlite") }
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	schema := []string{
		`CREATE TABLE IF NOT EXISTS eel_schema(version INTEGER PRIMARY KEY CHECK(version=1), applied_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS documents(namespace TEXT NOT NULL, scope TEXT NOT NULL, id TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>0), body BLOB NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(namespace,scope,id))`,
		`CREATE TABLE IF NOT EXISTS eino_checkpoints(scope TEXT NOT NULL, version TEXT NOT NULL, id TEXT NOT NULL, body BLOB NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(scope,version,id))`,
		`CREATE TABLE IF NOT EXISTS events(sequence INTEGER PRIMARY KEY AUTOINCREMENT, namespace TEXT NOT NULL, scope TEXT NOT NULL, id TEXT NOT NULL, kind TEXT NOT NULL, body BLOB NOT NULL, created_at TEXT NOT NULL, UNIQUE(namespace,scope,id))`,
	}
	for _, stmt := range schema {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	var version sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(version) FROM eel_schema`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 != 1 {
		return errors.New("sqlite: unsupported schema version")
	}
	if !version.Valid {
		if _, err = tx.ExecContext(ctx, `INSERT INTO eel_schema(version,applied_at) VALUES(1,?)`, stamp()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func stamp() string           { return time.Now().UTC().Format(time.RFC3339Nano) }
func hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func validKey(namespace, scope, id string) error {
	if namespace == "" || scope == "" || id == "" || len(namespace) > 128 || len(scope) > 4096 || len(id) > 1024 {
		return errors.New("sqlite: complete bounded namespace/scope/id required")
	}
	return nil
}

type Document = storage.Document
type Event = storage.Event
type Tx struct{ tx *sql.Tx }

func (s *Store) Transaction(ctx context.Context, fn func(*Tx) error) error {
	if fn == nil {
		return errors.New("sqlite: transaction callback required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(&Tx{tx}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Get(ctx context.Context, namespace, scope, id string) (Document, error) {
	return get(ctx, s.db, namespace, scope, id)
}
func (t *Tx) Get(ctx context.Context, namespace, scope, id string) (Document, error) {
	return get(ctx, t.tx, namespace, scope, id)
}

type reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func get(ctx context.Context, q reader, namespace, scope, id string) (Document, error) {
	d := Document{Namespace: namespace, Scope: scope, ID: id}
	if err := validKey(namespace, scope, id); err != nil {
		return d, err
	}
	err := q.QueryRowContext(ctx, `SELECT revision,body FROM documents WHERE namespace=? AND scope=? AND id=?`, namespace, scope, id).Scan(&d.Revision, &d.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// Put inserts at revision zero, otherwise requires exactly the stored revision.
func (s *Store) Put(ctx context.Context, d Document) (uint64, error) {
	var rev uint64
	err := s.Transaction(ctx, func(t *Tx) error { var e error; rev, e = t.Put(ctx, d); return e })
	return rev, err
}
func (t *Tx) Put(ctx context.Context, d Document) (uint64, error) {
	if err := validKey(d.Namespace, d.Scope, d.ID); err != nil {
		return 0, err
	}
	if len(d.Body) > maxDocumentBytes || !json.Valid(d.Body) {
		return 0, errors.New("sqlite: bounded JSON document required")
	}
	if d.Revision == 0 {
		r, err := t.tx.ExecContext(ctx, `INSERT INTO documents(namespace,scope,id,revision,body,updated_at) VALUES(?,?,?,1,?,?) ON CONFLICT(namespace,scope,id) DO NOTHING`, d.Namespace, d.Scope, d.ID, []byte(d.Body), stamp())
		if err != nil {
			return 0, err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n != 1 {
			return 0, ErrConflict
		}
		return 1, nil
	}
	if d.Revision >= 1<<63-1 {
		return 0, ErrConflict
	}
	r, err := t.tx.ExecContext(ctx, `UPDATE documents SET revision=revision+1,body=?,updated_at=? WHERE namespace=? AND scope=? AND id=? AND revision=?`, []byte(d.Body), stamp(), d.Namespace, d.Scope, d.ID, d.Revision)
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, ErrConflict
	}
	return d.Revision + 1, nil
}
func (s *Store) List(ctx context.Context, namespace, scope string) ([]Document, error) {
	if err := validKey(namespace, scope, "list"); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,revision,body FROM documents WHERE namespace=? AND scope=? ORDER BY id`, namespace, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d := Document{Namespace: namespace, Scope: scope}
		if err = rows.Scan(&d.ID, &d.Revision, &d.Body); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Append is idempotent only when the same scoped event ID has identical data.
func (t *Tx) Append(ctx context.Context, e Event) (int64, error) {
	if err := validKey(e.Namespace, e.Scope, e.ID); err != nil {
		return 0, err
	}
	if e.Kind == "" || len(e.Body) > maxDocumentBytes || !json.Valid(e.Body) {
		return 0, errors.New("sqlite: bounded typed event required")
	}
	r, err := t.tx.ExecContext(ctx, `INSERT INTO events(namespace,scope,id,kind,body,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(namespace,scope,id) DO NOTHING`, e.Namespace, e.Scope, e.ID, e.Kind, []byte(e.Body), stamp())
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		return r.LastInsertId()
	}
	var sequence int64
	var kind string
	var body []byte
	if err = t.tx.QueryRowContext(ctx, `SELECT sequence,kind,body FROM events WHERE namespace=? AND scope=? AND id=?`, e.Namespace, e.Scope, e.ID).Scan(&sequence, &kind, &body); err != nil {
		return 0, err
	}
	if kind != e.Kind || string(body) != string(e.Body) {
		return 0, ErrConflict
	}
	return sequence, nil
}
func (s *Store) Events(ctx context.Context, namespace, scope string, after int64, limit int) ([]Event, error) {
	if err := validKey(namespace, scope, "events"); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("sqlite: event page limit must be 1..1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,id,kind,body,created_at FROM events WHERE namespace=? AND scope=? AND sequence>? ORDER BY sequence LIMIT ?`, namespace, scope, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e := Event{Namespace: namespace, Scope: scope}
		if err = rows.Scan(&e.Sequence, &e.ID, &e.Kind, &e.Body, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Transact preserves SQLite atomicity through the neutral transaction port.
func (s *Store) Transact(ctx context.Context, fn func(storage.Tx) error) error {
	if fn == nil {
		return errors.New("sqlite: transaction callback required")
	}
	return s.Transaction(ctx, func(tx *Tx) error { return fn(tx) })
}

var _ storage.State = (*Store)(nil)

// Package checkpoint provides an Eino adk/compose CheckPointStore backed by SQLite.
// Local default for TurnLoop / Runner resume; same Get/Set/Delete contract works on any KV.
package checkpoint

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store implements adk.CheckPointStore and adk.CheckPointDeleter on a SQLite table.
type Store struct {
	db *sql.DB
}

const ddl = `
CREATE TABLE IF NOT EXISTS checkpoints(
  id TEXT PRIMARY KEY,
  blob BLOB NOT NULL,
  updated_at BIGINT NOT NULL
);
`

// Open opens (or creates) a SQLite file and migrates the checkpoints table.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db}
	if _, err := db.Exec(ddl); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("checkpoint ddl: %w", err)
	}
	return s, nil
}

// Attach reuses an existing *sql.DB (e.g. store.SQLStore.DB()) and migrates the table.
func Attach(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(ddl); err != nil {
		return nil, fmt.Errorf("checkpoint ddl: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Get(ctx context.Context, id string) ([]byte, bool, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT blob FROM checkpoints WHERE id=?`, id).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return blob, true, nil
}

func (s *Store) Set(ctx context.Context, id string, blob []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checkpoints(id, blob, updated_at) VALUES(?,?,?)
		 ON CONFLICT(id) DO UPDATE SET blob=excluded.blob, updated_at=excluded.updated_at`,
		id, blob, time.Now().UnixMilli())
	return err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM checkpoints WHERE id=?`, id)
	return err
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the underlying handle (nil-safe for Attach-owned DBs the caller closes separately).
func (s *Store) DB() *sql.DB { return s.db }

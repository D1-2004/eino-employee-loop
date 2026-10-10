// Package local provides an Eino adk/compose CheckPointStore backed by SQLite.
// Local default for TurnLoop / Runner resume; same Get/Set/Delete contract works on any KV.
package local

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Checkpoint implements adk.CheckPointStore and adk.CheckPointDeleter on a SQLite table.
type Checkpoint struct {
	db *sql.DB
}

const checkpointDDL = `
CREATE TABLE IF NOT EXISTS checkpoints(
  id TEXT PRIMARY KEY,
  blob BLOB NOT NULL,
  updated_at BIGINT NOT NULL
);
`

// Open opens (or creates) a SQLite file and migrates the checkpoints table.
func OpenCheckpoint(path string) (*Checkpoint, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Checkpoint{db: db}
	if _, err := db.Exec(checkpointDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("checkpoint ddl: %w", err)
	}
	return s, nil
}

// Attach reuses an existing *sql.DB (e.g. store.SQLStore.DB()) and migrates the table.
func AttachCheckpoint(db *sql.DB) (*Checkpoint, error) {
	if _, err := db.Exec(checkpointDDL); err != nil {
		return nil, fmt.Errorf("checkpoint ddl: %w", err)
	}
	return &Checkpoint{db: db}, nil
}

func (s *Checkpoint) Get(ctx context.Context, id string) ([]byte, bool, error) {
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

func (s *Checkpoint) Set(ctx context.Context, id string, blob []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checkpoints(id, blob, updated_at) VALUES(?,?,?)
		 ON CONFLICT(id) DO UPDATE SET blob=excluded.blob, updated_at=excluded.updated_at`,
		id, blob, time.Now().UnixMilli())
	return err
}

func (s *Checkpoint) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM checkpoints WHERE id=?`, id)
	return err
}

func (s *Checkpoint) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the underlying handle (nil-safe for Attach-owned DBs the caller closes separately).
func (s *Checkpoint) DB() *sql.DB { return s.db }

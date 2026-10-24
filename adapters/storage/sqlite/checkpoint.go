package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/cloudwego/eino/adk"
)

// Checkpoints is an Eino CheckPointStore bound by the Host to one trusted scope
// and code version. Model-provided checkpoint IDs cannot cross that namespace.
type Checkpoints struct {
	store          *Store
	scope, version string
}

func (s *Store) Checkpoints(scope, version string) (*Checkpoints, error) {
	if err := validKey("eino-checkpoint", scope, version); err != nil {
		return nil, err
	}
	return &Checkpoints{s, scope, version}, nil
}
func (c *Checkpoints) Get(ctx context.Context, id string) ([]byte, bool, error) {
	if err := validKey("eino-checkpoint", c.scope, id); err != nil {
		return nil, false, err
	}
	var data []byte
	err := c.store.db.QueryRowContext(ctx, `SELECT body FROM eino_checkpoints WHERE scope=? AND version=? AND id=?`, c.scope, c.version, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return data, err == nil, err
}
func (c *Checkpoints) Set(ctx context.Context, id string, data []byte) error {
	if err := validKey("eino-checkpoint", c.scope, id); err != nil {
		return err
	}
	if len(data) == 0 || len(data) > 64<<20 {
		return errors.New("sqlite: checkpoint size must be 1 byte..64 MiB")
	}
	_, err := c.store.db.ExecContext(ctx, `INSERT INTO eino_checkpoints(scope,version,id,body,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(scope,version,id) DO UPDATE SET body=excluded.body,updated_at=excluded.updated_at`, c.scope, c.version, id, data, stamp())
	return err
}
func (c *Checkpoints) Delete(ctx context.Context, id string) error {
	if err := validKey("eino-checkpoint", c.scope, id); err != nil {
		return err
	}
	_, err := c.store.db.ExecContext(ctx, `DELETE FROM eino_checkpoints WHERE scope=? AND version=? AND id=?`, c.scope, c.version, id)
	return err
}

// CheckpointStore binds native checkpoints to a trusted scope and version.
func (s *Store) CheckpointStore(scope, version string) (adk.CheckPointStore, error) {
	return s.Checkpoints(scope, version)
}

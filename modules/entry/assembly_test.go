package entry

import (
	"context"
	"errors"
	einotools "github.com/D1-2004/eino-employee-loop/adapters/services/eino"
	localservices "github.com/D1-2004/eino-employee-loop/adapters/services/local"
	"github.com/D1-2004/eino-employee-loop/adapters/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/adapters/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/modules/chat"
	"github.com/D1-2004/eino-employee-loop/ports/storage"
	"path/filepath"
)

type job = chat.Job

func openDefault(ctx context.Context, opts Options) (*Entry, error) {
	if opts.DefaultStorage == nil {
		opts.DefaultStorage = func(ctx context.Context, d StorageDependencies) (storage.State, storage.Artifacts, error) {
			db, err := sqlite.Open(ctx, filepath.Join(d.Directory, "state"))
			if err != nil {
				return nil, nil, err
			}
			files, err := markdown.Open(filepath.Join(d.Directory, "workspace"))
			if err != nil {
				return nil, nil, errors.Join(err, db.Close())
			}
			return db, files, nil
		}
	}
	if opts.BuildServices == nil {
		opts.BuildServices = localservices.NewLocal
	}
	if opts.PrepareTools == nil {
		opts.PrepareTools = einotools.Build
	}
	return Open(ctx, opts)
}

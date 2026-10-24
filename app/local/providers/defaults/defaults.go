// Package defaults supplies the reference local Provider assembly for CLI and
// server hosts. It does not start workers or create another Task authority.
package defaults

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/delivery"
	localexec "github.com/D1-2004/eino-employee-loop/app/local/providers/execution"
	einotools "github.com/D1-2004/eino-employee-loop/app/local/providers/services/eino"
	localservices "github.com/D1-2004/eino-employee-loop/app/local/providers/services/local"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/employee/entry"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
)

// Configure fills absent defaults without replacing explicitly supplied
// factories or services. Models, identity and authorization policy remain the
// caller's choice. Resources are opened by Entry, then closed in its lifecycle.
func Configure(opts entry.Options) entry.Options {
	if opts.DefaultStorage == nil {
		opts.DefaultStorage = func(ctx context.Context, d entry.StorageDependencies) (storage.State, storage.Artifacts, error) {
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
	if opts.DefaultExecutor == nil {
		opts.DefaultExecutor = func(_ context.Context, d entry.ExecutorDependencies) (execution.Port, error) {
			native := &localexec.Native{State: d.State, Artifacts: d.Artifacts, Tasks: d.Tasks, Context: d.Context, Runner: d.Runner}
			return &localexec.Local{DB: d.State, Owner: d.Owner, Authorize: d.Authorize, Run: native.Execute, InterruptRun: native.Cancel}, nil
		}
	}
	if opts.DefaultDelivery == nil {
		opts.DefaultDelivery = func(_ context.Context, d entry.DeliveryDependencies) (interaction.Port, error) {
			return delivery.Local{Files: d.Artifacts}, nil
		}
	}
	if opts.BuildServices == nil {
		opts.BuildServices = localservices.NewLocal
	}
	if opts.PrepareTools == nil {
		opts.PrepareTools = einotools.Build
	}
	return opts
}

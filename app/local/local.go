// Package main is the default local reference application. It opens SQLite,
// Markdown, native Eino, local execution and one business aggregate. Remote
// capabilities replace individual ports; the default needs no module registry.
package main

import (
	"context"
	"errors"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/defaults"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/entry"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	host "github.com/D1-2004/eino-employee-loop/employee/ports/services"
	"github.com/cloudwego/eino/callbacks"
)

type Identity struct {
	Workspace, Tenant, Employee, Owner string
	Scenes                             []string
}

// DefaultProfile supplies local behavior, never identity or platform authority.
// Callers change Revision when changing the accepted configuration.
func DefaultProfile(id Identity, modelName string) entry.Profile {
	return entry.Profile{
		Version: "local-el-profile/1", ID: "local-" + id.Employee, Revision: "local-default/1", Workspace: id.Workspace, Tenant: id.Tenant, Employee: id.Employee, Owner: id.Owner, Name: "Local Employee", Backend: "standalone", ModelName: modelName, Scenes: append([]string(nil), id.Scenes...), HostTools: true, MaxModelCalls: 16, MaxRuns: 4,
		OwnerLoops: []string{"attention", "memory", "dreaming", "tone", "reflection", "summary"}, DeliveryLoops: []string{"expression", "package"},
	}
}

type Options struct {
	Directory         string
	Profile           entry.Profile
	Models            entry.Models
	Services          host.Services
	Delivery          interaction.Port
	ResolveDelivery   func(context.Context, interaction.Intent) (interaction.Intent, error)
	AuthorizeDelivery func(context.Context, interaction.Intent) error
	// An externally supplied atomic executor needs an immutable binding identity.
	Executor    execution.Port
	ExecutorRef entry.ModuleRef
	Callbacks   []callbacks.Handler
	Plugins     *entry.Registry
	Now         func() time.Time
}
type Runtime struct {
	*entry.Entry
	Services host.Services
}

func Open(ctx context.Context, opts Options) (*Runtime, error) {
	if opts.Profile.Backend != "standalone" {
		return nil, errors.New("localapp: standalone profile required; use entry.Open for complete service backends")
	}
	assembled := entry.Options{Directory: opts.Directory, Profile: opts.Profile, Models: opts.Models, Services: opts.Services, Delivery: opts.Delivery, ResolveDelivery: opts.ResolveDelivery, AuthorizeDelivery: opts.AuthorizeDelivery, Callbacks: opts.Callbacks, Plugins: opts.Plugins, Now: opts.Now}
	assembled = defaults.Configure(assembled)
	if opts.Executor != nil {
		if opts.ExecutorRef.Name == "" || opts.ExecutorRef.Version == "" {
			return nil, errors.New("localapp: exact executor name/version required")
		}
		selected := opts.Profile.Modules.Executor
		if selected != (entry.ModuleRef{}) && selected != opts.ExecutorRef {
			return nil, errors.New("localapp: executor differs from profile binding")
		}
		registry := entry.NewModuleRegistry()
		if err := registry.RegisterExecutor(opts.ExecutorRef, func(context.Context, entry.ExecutorDependencies) (execution.Port, error) { return opts.Executor, nil }); err != nil {
			return nil, err
		}
		assembled.Modules = registry
		assembled.Profile.Modules.Executor = opts.ExecutorRef
	}
	runtime, err := entry.Open(ctx, assembled)
	if err != nil {
		return nil, err
	}
	services, err := runtime.HostServices(ctx)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	return &Runtime{Entry: runtime, Services: services}, nil
}

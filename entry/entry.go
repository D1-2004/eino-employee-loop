// Package entry is the explicit application entry. It selects one versioned
// business backend; channels never own a second Task/Goal state machine.
package entry

import (
	"context"
	"encoding/json"
	"errors"
	native "github.com/D1-2004/eino-employee-loop/agent"
	"github.com/D1-2004/eino-employee-loop/ownerloop"
	"path/filepath"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/interaction"
	"github.com/D1-2004/eino-employee-loop/work"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
)

type Profile struct {
	Modules                                                                             ModuleSelection
	Plugins                                                                             []PluginRef
	Version, ID, Revision, Workspace, Tenant, Employee, Owner, Name, Backend, ModelName string
	Scenes                                                                              []string
	Instructions                                                                        string
	OwnerLoops                                                                          []string
	DeliveryLoops                                                                       []string
	MaxModelCalls, MaxRuns                                                              int
}

func (p Profile) Scope(scene string) work.Scope {
	return work.Scope{Workspace: p.Workspace, Tenant: p.Tenant, Employee: p.Employee, Scene: scene, Requester: p.Owner}
}
func (p Profile) Key() string {
	raw, _ := json.Marshal([]string{p.Workspace, p.Tenant, p.Employee, p.Owner, p.ID})
	return contextbuilder.Digest(raw)
}
func (p Profile) Validate() error {
	if p.Version != "local-el-profile/1" || p.ID == "" || p.Revision == "" || p.Workspace == "" || p.Tenant == "" || p.Employee == "" || p.Owner == "" || p.Backend == "" || p.ModelName == "" || len(p.Scenes) == 0 || p.MaxModelCalls < 1 || p.MaxModelCalls > 128 || p.MaxRuns < 1 || p.MaxRuns > 512 {
		return errors.New("entry: invalid immutable profile")
	}
	for _, group := range []struct {
		names   []string
		allowed map[string]bool
	}{{p.OwnerLoops, nil}, {p.DeliveryLoops, map[string]bool{"expression": true, "package": true}}} {
		seen := map[string]bool{}
		for _, name := range group.names {
			if name == "" || seen[name] || (group.allowed != nil && !group.allowed[name]) {
				return errors.New("entry: unknown or duplicate loop")
			}
			seen[name] = true
		}
	}
	pluginNames := map[string]bool{}
	for _, ref := range p.Plugins {
		if !validPlugin(ref) || pluginNames[ref.Name] {
			return errors.New("entry: exact unique plugins required")
		}
		pluginNames[ref.Name] = true
	}
	seen := map[string]bool{}
	for _, s := range p.Scenes {
		if s == "" || seen[s] {
			return errors.New("entry: explicit unique scenes required")
		}
		seen[s] = true
	}
	return nil
}

type Input struct {
	ID, Scene, Actor, Text, Origin string
	Native                         json.RawMessage
}
type Admission struct {
	ID, State, Backend, ProfileRevision string
	Replay                              bool
}
type ModelRequest struct {
	Role, Receipt, ModelName string
	Scope                    work.Scope
	Profile                  Profile
}
type Models func(context.Context, ModelRequest) (model.ToolCallingChatModel, error)
type Backend interface {
	Start(context.Context) error
	Submit(context.Context, Input) (Admission, error)
	RunOnce(context.Context) (bool, error)
	Resume(context.Context, string, Input, string) error
	Pending(context.Context, string) (map[string][]string, error)
	Tasks(context.Context, work.Scope) ([]work.Task, error)
	Deliveries(context.Context, work.Scope) ([]interaction.Record, error)
	Close() error
}
type Options struct {
	Modules              *ModuleRegistry
	AudiencePolicy       contextbuilder.AudiencePolicy
	RequiredCapabilities []Capability
	Directory            string
	Now                  func() time.Time
	Profile              Profile
	Models               Models
	Delivery             interaction.Port
	Callbacks            []callbacks.Handler
	Plugins              *Registry
	// Private DTO adapters register their complete business backend here; never
	// underneath the standalone Execution Bridge as another business Task API.
	Backends map[string]func(context.Context, Options) (Backend, error)
}
type Entry struct {
	backend      Backend
	capabilities map[Capability]bool
	profile      Profile
	mu           sync.Mutex
	closed       bool
	life         context.Context
	cancel       context.CancelFunc
	ops          sync.WaitGroup
	done         chan struct{}
	closeErr     error
	stops        []func()
}

func Open(ctx context.Context, opts Options) (*Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The caller may reuse its configuration buffers for a later revision.
	// Admission and permission checks retain the exact snapshot opened here.
	opts.Profile.Scenes = append([]string(nil), opts.Profile.Scenes...)
	opts.Profile.OwnerLoops = append([]string(nil), opts.Profile.OwnerLoops...)
	opts.Profile.DeliveryLoops = append([]string(nil), opts.Profile.DeliveryLoops...)
	opts.Profile.Plugins = append([]PluginRef(nil), opts.Profile.Plugins...)
	opts.Callbacks = append([]callbacks.Handler(nil), opts.Callbacks...)
	opts.RequiredCapabilities = append([]Capability(nil), opts.RequiredCapabilities...)

	if err := opts.Profile.Validate(); err != nil {
		return nil, err
	}
	if opts.Directory == "" {
		return nil, errors.New("entry: private directory required")
	}
	dir, err := filepath.Abs(opts.Directory)
	if err != nil {
		return nil, err
	}
	opts.Directory = dir
	if err := opts.Modules.validate(opts.Profile.Modules); err != nil {
		return nil, err
	}
	if _, err := opts.Plugins.Resolve(opts.Profile.Plugins, native.Config{}); err != nil {
		return nil, err
	}
	var b Backend
	if opts.Profile.Backend == "standalone" {
		b, err = openStandalone(ctx, opts)
	} else {
		factory := opts.Backends[opts.Profile.Backend]
		if factory == nil {
			return nil, errors.New("entry: backend is not registered; no fallback")
		}
		b, err = factory(ctx, opts)
	}
	if err != nil {
		if !nilModule(b) {
			err = errors.Join(err, b.Close())
		}
		return nil, err
	}
	if nilModule(b) {
		return nil, errors.New("entry: backend unavailable")
	}
	caps, err := backendCapabilities(b, opts)
	if err != nil {
		_ = b.Close()
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	return &Entry{backend: b, profile: opts.Profile, capabilities: caps, life: life, cancel: cancel, done: make(chan struct{})}, nil
}

// operation joins the caller's cancellation with Entry shutdown. It registers
// under the same mutex used by Close, so no operation can race resource teardown.
func (e *Entry) operation(ctx context.Context) (context.Context, func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, nil, errors.New("entry: closed")
	}
	e.ops.Add(1)
	c, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.life, cancel)
	return c, func() { stop(); cancel(); e.ops.Done() }, nil
}
func (e *Entry) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("entry: closed")
	}
	c, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.life, cancel)
	if err := e.backend.Start(c); err != nil {
		stop()
		cancel()
		return err
	}
	e.stops = append(e.stops, func() { stop(); cancel() })
	return nil
}
func (e *Entry) Submit(ctx context.Context, in Input) (Admission, error) {
	c, done, err := e.operation(ctx)
	if err != nil {
		return Admission{}, err
	}
	defer done()
	return e.backend.Submit(c, in)
}
func (e *Entry) RunOnce(ctx context.Context) (bool, error) {
	c, done, err := e.operation(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	return e.backend.RunOnce(c)
}
func (e *Entry) Tasks(ctx context.Context, scene string) ([]work.Task, error) {
	c, done, err := e.operation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return e.backend.Tasks(c, e.profile.Scope(scene))
}
func (e *Entry) Deliveries(ctx context.Context, scene string) ([]interaction.Record, error) {
	c, done, err := e.operation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return e.backend.Deliveries(c, e.profile.Scope(scene))
}

// Errors exposes owned-worker failures instead of silently discarding them.
func (e *Entry) Errors() <-chan error {
	if b, ok := e.backend.(interface{ Errors() <-chan error }); ok {
		return b.Errors()
	}
	return nil
}
func (e *Entry) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		<-e.done
		return e.closeErr
	}
	e.closed = true
	e.cancel()
	for _, stop := range e.stops {
		stop()
	}
	e.mu.Unlock()
	e.ops.Wait()
	err := e.backend.Close()
	e.mu.Lock()
	e.closeErr = err
	close(e.done)
	e.mu.Unlock()
	return err
}

// Drain is bounded; a wait or unknown outcome remains persisted for a later
// input/event instead of spinning until it looks complete.
func (e *Entry) Drain(ctx context.Context, limit int) error {
	if limit < 1 || limit > 10000 {
		return errors.New("entry: invalid drain ceiling")
	}
	for i := 0; i < limit; i++ {
		progress, err := e.RunOnce(ctx)
		if err != nil {
			return err
		}
		if !progress {
			return nil
		}
	}
	return errors.New("entry: drain ceiling reached")
}

func (e *Entry) Resume(ctx context.Context, id string, answer Input, target string) error {
	c, done, err := e.operation(ctx)
	if err != nil {
		return err
	}
	defer done()
	return e.backend.Resume(c, id, answer, target)
}
func (e *Entry) Pending(ctx context.Context, scene string) (map[string][]string, error) {
	c, done, err := e.operation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return e.backend.Pending(c, scene)
}

func (e *Entry) Schedule(ctx context.Context, scene string, in work.Schedule) (work.Schedule, error) {
	if !e.capabilities[CapabilitySchedule] {
		return work.Schedule{}, ErrUnsupported
	}
	c, done, err := e.operation(ctx)
	if err != nil {
		return work.Schedule{}, err
	}
	defer done()
	b, ok := e.backend.(interface {
		Schedule(context.Context, work.Scope, work.Schedule) (work.Schedule, error)
	})
	if !ok {
		return work.Schedule{}, errors.New("entry: schedule unsupported by backend")
	}
	return b.Schedule(c, e.profile.Scope(scene), in)
}
func (e *Entry) Signal(ctx context.Context, in Input, condition string) error {
	if !e.capabilities[CapabilitySignal] {
		return ErrUnsupported
	}
	c, done, err := e.operation(ctx)
	if err != nil {
		return err
	}
	defer done()
	b, ok := e.backend.(interface {
		Signal(context.Context, Input, string) error
	})
	if !ok {
		return errors.New("entry: signal unsupported by backend")
	}
	return b.Signal(c, in, condition)
}
func (e *Entry) OwnerDocuments(ctx context.Context, scene string) ([]ownerloop.Document, error) {
	if !e.capabilities[CapabilityOwner] {
		return nil, ErrUnsupported
	}
	c, done, err := e.operation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	b, ok := e.backend.(interface {
		OwnerDocuments(context.Context, work.Scope) ([]ownerloop.Document, error)
	})
	if !ok {
		return nil, errors.New("entry: owner context unsupported by backend")
	}
	return b.OwnerDocuments(c, e.profile.Scope(scene))
}

// Retry releases an explicitly selected recovery hold without granting a new
// physical model budget or changing any accepted source/configuration.
func (e *Entry) Retry(ctx context.Context, id string) error {
	if !e.capabilities[CapabilityRetry] {
		return ErrUnsupported
	}
	c, done, err := e.operation(ctx)
	if err != nil {
		return err
	}
	defer done()
	b, ok := e.backend.(interface {
		Retry(context.Context, string) error
	})
	if !ok {
		return errors.New("entry: retry unsupported by backend")
	}
	return b.Retry(c, id)
}

func (e *Entry) Failures(ctx context.Context, scene string) ([]Failure, error) {
	if !e.capabilities[CapabilityDiagnostics] {
		return nil, ErrUnsupported
	}
	c, done, err := e.operation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	b, ok := e.backend.(interface {
		Failures(context.Context, work.Scope) ([]Failure, error)
	})
	if !ok {
		return nil, ErrUnsupported
	}
	return b.Failures(c, e.profile.Scope(scene))
}

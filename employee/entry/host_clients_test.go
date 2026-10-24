package entry

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	host "github.com/D1-2004/eino-employee-loop/employee/ports/services"
)

type hostClientProbe struct {
	calls       atomic.Int32
	wait        func(context.Context)
	toolReceipt string
}

func (p *hostClientProbe) called(ctx context.Context) {
	p.calls.Add(1)
	if p.wait != nil {
		p.wait(ctx)
	}
}
func (p *hostClientProbe) GetConfig(ctx context.Context, _ host.Call) (host.ConfigSnapshot, error) {
	p.called(ctx)
	return host.ConfigSnapshot{ID: "remote", Version: "immutable-1", Value: json.RawMessage(`{"private":"read-result"}`)}, nil
}
func (p *hostClientProbe) GetMessages(ctx context.Context, _ host.Call, _ host.MessageRequest) (host.Messages, error) {
	p.called(ctx)
	return host.Messages{}, nil
}
func (p *hostClientProbe) ReadResource(ctx context.Context, _ host.Call, _ host.ResourceRef) (host.Resource, error) {
	p.called(ctx)
	return host.Resource{Content: []byte("read-result")}, nil
}
func (p *hostClientProbe) DiscoverTools(ctx context.Context, _ host.Call) ([]host.Tool, error) {
	p.called(ctx)
	return nil, nil
}
func (p *hostClientProbe) CallTool(ctx context.Context, _ host.Call, _ host.ToolCall) (host.ToolResult, error) {
	p.called(ctx)
	return host.ToolResult{Content: json.RawMessage(`{"private":"read-result"}`), Receipt: p.toolReceipt}, nil
}
func (p *hostClientProbe) ListFiles(ctx context.Context, _ host.Call) (host.Files, error) {
	p.called(ctx)
	return host.Files{}, nil
}
func (p *hostClientProbe) services() host.Services {
	return host.Services{Config: p, Messages: p, Resources: p, Tools: p, Files: p}
}

func TestExportedServiceOverridesCannotBeUsedAfterEntryClose(t *testing.T) {
	ctx := context.Background()
	provider := &hostClientProbe{}
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(nil), Services: provider.services()})
	if err != nil {
		t.Fatal(err)
	}
	services, err := e.HostServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	call := host.Call{Scope: testProfile().Scope("dm")}
	for name, invoke := range map[string]func() error{
		"config":         func() error { _, err := services.Config.GetConfig(ctx, call); return err },
		"messages":       func() error { _, err := services.Messages.GetMessages(ctx, call, host.MessageRequest{}); return err },
		"resources":      func() error { _, err := services.Resources.ReadResource(ctx, call, host.ResourceRef{}); return err },
		"files":          func() error { _, err := services.Files.ListFiles(ctx, call); return err },
		"discover_tools": func() error { _, err := services.Tools.DiscoverTools(ctx, call); return err },
		"call_tool":      func() error { _, err := services.Tools.CallTool(ctx, call, host.ToolCall{}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := invoke(); err == nil {
				t.Fatal("borrowed provider remained callable through a closed Entry")
			}
		})
	}
	if provider.calls.Load() != 0 {
		t.Fatal("closed Entry crossed the provider boundary", provider.calls.Load())
	}
}

func TestExportedServiceCallIsCancelledAndDrainedBeforeEntryClose(t *testing.T) {
	ctx := context.Background()
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	provider := &hostClientProbe{wait: func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		// A read provider may finish successfully after cancellation. The Entry
		// must suppress these bytes while still waiting for the call to settle.
	}}
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(nil), Services: provider.services()})
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); _ = e.Close() })
	services, err := e.HostServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		value host.ConfigSnapshot
		err   error
	}
	result := make(chan outcome, 1)
	go func() {
		v, err := services.Config.GetConfig(ctx, host.Call{Scope: testProfile().Scope("dm")})
		result <- outcome{v, err}
	}()
	awaitHostSignal(t, started, "provider call to start")
	closed := make(chan error, 1)
	go func() { closed <- e.Close() }()
	awaitHostSignal(t, cancelled, "Entry cancellation to reach provider")
	select {
	case err := <-closed:
		t.Fatal("Close returned while the provider was still settling", err)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) || len(got.value.Value) != 0 {
			t.Fatal("cancelled read released provider bytes", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("service call did not settle")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after provider settled")
	}
}

func awaitHostSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for", description)
	}
}

func TestExportedToolPreservesCommittedReceiptAfterCancellation(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	provider := &hostClientProbe{toolReceipt: "actual-provider-commit", wait: func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	}}
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(nil), Services: provider.services()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	services, err := e.HostServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		result host.ToolResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		r, err := services.Tools.CallTool(callCtx, host.Call{Scope: testProfile().Scope("dm")}, host.ToolCall{ID: "commit", CallID: "actual-native-call", Arguments: json.RawMessage(`{}`)})
		finished <- outcome{r, err}
	}()
	awaitHostSignal(t, started, "effect provider call to start")
	cancel()
	select {
	case got := <-finished:
		if got.result.Receipt != "actual-provider-commit" {
			t.Fatal("cancellation erased durable effect evidence", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("effect call did not settle")
	}
}

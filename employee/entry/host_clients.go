package entry

import (
	"context"
	"errors"

	host "github.com/D1-2004/eino-employee-loop/employee/ports/services"
)

// Service clients are borrowed, but calls made through this Entry belong to its
// lifetime. Use the same cancellation/join boundary as Submit and RunOnce.
func serviceRead[T any](e *Entry, ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	c, done, err := e.operation(ctx)
	if err != nil {
		return zero, err
	}
	defer done()
	result, err := fn(c)
	if canceled := c.Err(); canceled != nil {
		return zero, errors.Join(err, canceled)
	}
	return result, err
}

type configClient struct {
	entry    *Entry
	provider host.ConfigProvider
}

func (c configClient) GetConfig(ctx context.Context, call host.Call) (host.ConfigSnapshot, error) {
	return serviceRead(c.entry, ctx, func(ctx context.Context) (host.ConfigSnapshot, error) { return c.provider.GetConfig(ctx, call) })
}

type messageClient struct {
	entry    *Entry
	provider host.MessageProvider
}

func (c messageClient) GetMessages(ctx context.Context, call host.Call, q host.MessageRequest) (host.Messages, error) {
	return serviceRead(c.entry, ctx, func(ctx context.Context) (host.Messages, error) { return c.provider.GetMessages(ctx, call, q) })
}

type resourceClient struct {
	entry    *Entry
	provider host.ResourceProvider
}

func (c resourceClient) ReadResource(ctx context.Context, call host.Call, q host.ResourceRef) (host.Resource, error) {
	return serviceRead(c.entry, ctx, func(ctx context.Context) (host.Resource, error) { return c.provider.ReadResource(ctx, call, q) })
}

type fileClient struct {
	entry    *Entry
	provider host.FileProvider
}

func (c fileClient) ListFiles(ctx context.Context, call host.Call) (host.Files, error) {
	return serviceRead(c.entry, ctx, func(ctx context.Context) (host.Files, error) { return c.provider.ListFiles(ctx, call) })
}

type toolClient struct {
	entry    *Entry
	provider host.ToolProvider
}

func (c toolClient) DiscoverTools(ctx context.Context, call host.Call) ([]host.Tool, error) {
	return serviceRead(c.entry, ctx, func(ctx context.Context) ([]host.Tool, error) { return c.provider.DiscoverTools(ctx, call) })
}
func (c toolClient) CallTool(ctx context.Context, call host.Call, q host.ToolCall) (host.ToolResult, error) {
	work, done, err := c.entry.operation(ctx)
	if err != nil {
		return host.ToolResult{}, err
	}
	defer done()
	result, err := c.provider.CallTool(work, call, q)
	if canceled := work.Err(); canceled != nil {
		// Keep a genuine committed receipt even when shutdown cancels its caller;
		// no successful readonly output is released after cancellation.
		return host.ToolResult{Receipt: result.Receipt}, errors.Join(err, canceled)
	}
	return result, err
}
func (e *Entry) serviceClients(s host.Services) host.Services {
	out := host.Services{}
	if s.Config != nil {
		out.Config = configClient{e, s.Config}
	}
	if s.Messages != nil {
		out.Messages = messageClient{e, s.Messages}
	}
	if s.Resources != nil {
		out.Resources = resourceClient{e, s.Resources}
	}
	if s.Files != nil {
		out.Files = fileClient{e, s.Files}
	}
	if s.Tools != nil {
		out.Tools = toolClient{e, s.Tools}
	}
	return out
}

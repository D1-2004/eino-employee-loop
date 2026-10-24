// Package host defines narrow, transport-independent service capabilities.
// Call is a locator, never an authorization grant. The host checks each use.
package services

import (
	"context"
	"encoding/json"
	"errors"

	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/D1-2004/eino-employee-loop/ports/storage"
)

var (
	ErrInvalid     = errors.New("host: invalid request or provider result")
	ErrUnsupported = errors.New("host: capability unsupported")
	ErrChanged     = errors.New("host: immutable resource changed")
)

// Binding is opaque host-owned data. Only Authorize interprets it. Credentials
// must not be persisted in this value or sent to models.
type Call struct {
	Scope                        work.Scope
	SourceReceipt, TaskID, RunID string
	Binding                      json.RawMessage
}

type ConfigSnapshot struct {
	ID, Version string
	Value       json.RawMessage
}
type ConfigProvider interface {
	GetConfig(context.Context, Call) (ConfigSnapshot, error)
}

type MessageRequest struct {
	IDs []string `json:"message_ids"`
}

// Complete describes the exact requested IDs in the admitted local source
// ledger, not completeness of the provider's conversation history.
type MessageCoverage struct {
	Source     string
	Complete   bool
	MissingIDs []string
}
type Messages struct {
	Items    []contextbuilder.Source
	Coverage MessageCoverage
}
type MessageProvider interface {
	GetMessages(context.Context, Call, MessageRequest) (Messages, error)
}

// ResourceRef binds bytes to one Task and Run. Version is an immutable content
// version, independent of access URLs or platform resource credentials.
type ResourceRef struct {
	TaskID  string `json:"task_id"`
	RunID   string `json:"run_id"`
	Path    string `json:"path"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}
type Resource struct {
	Ref     ResourceRef
	Content []byte
}
type ResourceProvider interface {
	ReadResource(context.Context, Call, ResourceRef) (Resource, error)
}
type Files struct {
	Items    []ResourceRef
	Complete bool
}
type FileProvider interface {
	ListFiles(context.Context, Call) (Files, error)
}

type Tool struct {
	ID, Description string
	Schema          json.RawMessage
	Effect          bool
}
type ToolCall struct {
	ID, CallID string
	Arguments  json.RawMessage
}

// Effectful providers must deduplicate by trusted Call identity plus CallID and
// return the committed durable receipt, including when cancellation follows a
// successful commit. An error or absent receipt never proves no effect occurred.
type ToolResult struct {
	Content json.RawMessage
	Receipt string
}
type ToolProvider interface {
	DiscoverTools(context.Context, Call) ([]Tool, error)
	CallTool(context.Context, Call, ToolCall) (ToolResult, error)
}

// Services can be replaced independently. Delivery and execution deliberately
// remain interaction.Port and execution.Port; this package creates no outbox.
type Services struct {
	Config    ConfigProvider
	Messages  MessageProvider
	Resources ResourceProvider
	Tools     ToolProvider
	Files     FileProvider
}

type SourceReader interface {
	Source(context.Context, work.Scope, string) (contextbuilder.Source, contextbuilder.Ref, error)
}
type TaskReader interface {
	Get(context.Context, work.Scope, string) (work.Task, error)
}
type ArtifactReader interface {
	Read(context.Context, string) (storage.File, error)
}

type Bindings struct {
	Authorize func(context.Context, Call) error
	Config    func(context.Context, Call) (ConfigSnapshot, error)
	Sources   SourceReader
	Tasks     TaskReader
	Artifacts ArtifactReader
	// Overrides replaces individual groups before default tools are bound.
	// Default tools call these final providers, not hidden local counterparts.
	Overrides Services
}

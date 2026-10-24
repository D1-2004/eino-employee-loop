// Package storage defines transactional state and artifact capabilities used by
// business modules. Adapters must preserve atomic CAS, detached values and
// fenced workflow ownership; generic CRUD alone is insufficient.
package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/D1-2004/eino-employee-loop/employee/kernel/workflow"
	"github.com/cloudwego/eino/adk"
)

var (
	ErrConflict         = errors.New("storage: revision conflict")
	ErrNotFound         = errors.New("storage: record not found")
	ErrBusy             = errors.New("storage: active operation")
	ErrArtifactConflict = errors.New("storage: artifact content revision changed")
)

type Document struct {
	Namespace, Scope, ID string
	Revision             uint64
	Body                 json.RawMessage
}

type Event struct {
	Sequence                   int64
	Namespace, Scope, ID, Kind string
	Body                       json.RawMessage
	CreatedAt                  string
}

// Tx is valid only during its Transact callback. A failed callback must leave
// documents, receipts and events unchanged.
type Tx interface {
	Get(context.Context, string, string, string) (Document, error)
	Put(context.Context, Document) (uint64, error)
	Append(context.Context, Event) (int64, error)
}

type State interface {
	Get(context.Context, string, string, string) (Document, error)
	Put(context.Context, Document) (uint64, error)
	List(context.Context, string, string) ([]Document, error)
	Events(context.Context, string, string, int64, int) ([]Event, error)
	Transact(context.Context, func(Tx) error) error
	CheckpointStore(string, string) (adk.CheckPointStore, error)
	Acquire(context.Context, workflow.Key) (workflow.Lease, error)
	Close() error
}

type File struct{ Path, Body, SHA256 string }

type Artifacts interface {
	Read(context.Context, string) (File, error)
	Write(context.Context, string, string, string) (File, error)
	Close() error
}

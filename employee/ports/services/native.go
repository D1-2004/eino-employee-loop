package services

import (
	"context"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
)

// NativeToolBindings gives a native adapter only its scoped catalog journal.
type NativeToolBindings struct {
	State     storage.State
	Authorize func(context.Context, Call) error
}
type NativeTools func(context.Context, NativeToolBindings, ToolProvider, Call, string, func(context.Context) error) ([]*kernel.Function, error)

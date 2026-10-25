package task

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

var ErrOperationUnknown = errors.New("task: original operation outcome unknown")

// Operation preserves the native call identity and complete original arguments.
// Kind is dispatch, control or answer. A changed operation under the same key
// must conflict in the Host's durable ledger, never create another business Task.
type Operation struct {
	Scope                                           work.Scope
	Source                                          work.Source
	Call, Kind, TaskID, Action, Instruction, WaitID string
	GoalRevision                                    uint64
	Definition                                      work.Definition
	ExtraSources                                    []work.Source
}

func (o Operation) Key() string {
	raw, _ := json.Marshal([]any{o.Scope, o.Source.Receipt, o.Call})
	return "employee-task-operation:" + contract.Digest(raw)
}
func (o Operation) SHA256() string { raw, _ := json.Marshal(o); return contract.Digest(raw) }

// NotStarted is authoritative absence for this exact immutable operation, not
// a missing task/search result. Every state binds Scope, original key and SHA.
type OperationResult struct {
	State                       string
	Scope                       work.Scope
	OperationKey, RequestSHA256 string
	Receipt                     work.Receipt
}
type Operations interface {
	LookupOperation(context.Context, Operation) (OperationResult, error)
	ExecuteOperation(context.Context, Operation) (OperationResult, error)
}

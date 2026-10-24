// Package work owns the local Task/Goal/Run/Wait aggregate. Execution exit is
// a fact; only a source- and revision-checked Review changes business completion.
package work

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrScope   = errors.New("work: scope or current source mismatch")
	ErrState   = errors.New("work: incompatible task state")
	ErrStale   = errors.New("work: stale goal/run/review")
	ErrBudget  = errors.New("work: run budget exhausted")
	ErrUnknown = errors.New("work: execution outcome unknown; lookup required")
)

type Scope struct{ Workspace, Tenant, Employee, Scene, Requester string }

func (s Scope) Key() (string, error) {
	for _, v := range []string{s.Workspace, s.Tenant, s.Employee, s.Scene, s.Requester} {
		if strings.TrimSpace(v) == "" {
			return "", ErrScope
		}
	}
	raw, _ := json.Marshal(s)
	return string(raw), nil
}

type Source struct{ Receipt, Actor, Text string }
type State string

const (
	Ready     State = "ready"
	Running   State = "running"
	Waiting   State = "waiting"
	Completed State = "completed"
	Failed    State = "failed"
	Paused    State = "paused"
	Stopped   State = "stopped"
)

type Definition struct {
	Configuration     json.RawMessage
	Goal, Instruction string
	Constraints       []string
	MaxRuns           int
}
type Goal struct {
	Revision uint64
	State    State
	Summary  string
}
type RunState string

const (
	RunQueued     RunState = "queued"
	RunPreparing  RunState = "preparing"
	RunUnknown    RunState = "unknown"
	RunActive     RunState = "running"
	RunSuspended  RunState = "suspended"
	RunSucceeded  RunState = "succeeded"
	RunFailed     RunState = "failed"
	RunCancelling RunState = "cancelling"
	RunStopped    RunState = "stopped"
)

type Run struct {
	ID, SubmitKey, ExecutionID string
	GoalRevision               uint64
	State                      RunState
	Instruction, Output        string
	ExitProven                 bool
	Artifacts                  []Artifact
	Failure                    string
}
type Artifact struct {
	Path, SHA256 string
	Bytes        int64
}
type Wait struct {
	ID, Kind, Prompt, Condition, Answer string
	NativeTarget, ExecutionID           string
	Due                                 time.Time
	Since                               time.Time
	Resolved                            bool
	SourceReceipt                       string
}
type Pending struct {
	Kind, Instruction string
	Source            Source
}
type Task struct {
	Deliveries           []DeliveryFact
	ID                   string
	Scope                Scope
	Revision             uint64
	Definition           Definition
	Sources              []Source
	Goal                 Goal
	State                State
	Runs                 []Run
	Waits                []Wait
	NeedsReview          bool
	Pending              *Pending
	CreatedAt, UpdatedAt time.Time
}

func (t Task) CurrentRun() *Run {
	if len(t.Runs) == 0 {
		return nil
	}
	r := t.Runs[len(t.Runs)-1]
	return &r
}

type Receipt struct {
	ID, TaskID, RunID          string
	TaskRevision, GoalRevision uint64
	State                      State
}
type ExecutionFact struct {
	RunID, ExecutionID string
	State              RunState
	Output, Failure    string
	ExitProven         bool
	Artifacts          []Artifact
}
type Review struct {
	TaskRevision, GoalRevision            uint64
	RunID, Kind, Summary, NextInstruction string
	Waits                                 []Wait
}

func stableID(prefix string, v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return prefix + hex.EncodeToString(h[:12])
}
func validateSource(scope Scope, source Source, call string) error {
	if _, err := scope.Key(); err != nil {
		return err
	}
	if source.Actor != scope.Requester || source.Receipt == "" || strings.TrimSpace(source.Text) == "" || call == "" {
		return ErrScope
	}
	return nil
}
func nextRun(t *Task, instruction string) error {
	if len(t.Runs) >= t.Definition.MaxRuns {
		return ErrBudget
	}
	id := fmt.Sprintf("%s-run-%d", t.ID, len(t.Runs)+1)
	t.Runs = append(t.Runs, Run{ID: id, SubmitKey: stableID("submit-", []any{t.Scope, id, t.Goal.Revision}), GoalRevision: t.Goal.Revision, State: RunQueued, Instruction: instruction})
	t.State = Running
	t.Goal.State = Running
	t.NeedsReview = false
	return nil
}

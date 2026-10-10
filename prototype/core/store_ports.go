// Store ports of the prototype runtime: TaskStore, MemoryStore, KV.
// MemoryStore is the loop's contract port; TaskStore and KV are the
// prototype's own (the module's Task Manager boundary is
// contract.TaskManager). Concrete backends live in assemblies (e.g. local/).
package core

import (
	"context"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
)

var (
	ErrQuota    = contract.ErrQuota
	ErrNotFound = contract.ErrNotFound
	ErrBadQuote = contract.ErrBadQuote
)

const (
	StQueued    = contract.TaskQueued
	StRunning   = contract.TaskRunning
	StDone      = contract.TaskDone
	StFailed    = contract.TaskFailed
	StCancelled = contract.TaskCancelled
)

func ActiveStatus(s string) bool { return contract.TaskActive(s) }
func NowMs() int64               { return time.Now().UnixMilli() }

type (
	Fact         = contract.Fact
	MemoryLimits = contract.MemoryLimits
	MemoryStore  = contract.MemoryStore
)

// Task is a durable unit of background work created by dispatch_task.
type Task struct {
	ID        string `json:"task_ref"`
	Tenant    string `json:"-"`
	Scene     string `json:"scene"`
	SourceRef string `json:"source_ref"`
	Requester string `json:"requester"`
	Goal      string `json:"goal"`
	Prompt    string `json:"-"`
	Status    string `json:"status"`
	IdemKey   string `json:"idem_key"`
	Receipt   string `json:"receipt"`
	Result    string `json:"result,omitempty"`
	CreatedMs int64  `json:"created_ms"`
	UpdatedMs int64  `json:"updated_ms"`
	DoneMs    int64  `json:"done_ms,omitempty"`
	Attempts  int    `json:"attempts"`
}

// TaskStore persists the prototype's tasks and their event history.
type TaskStore interface {
	// Create inserts the task unless an active task (or one finished within
	// dedupWindow) with the same idempotency key exists in the scene; then it
	// returns that task and created=false.
	Create(ctx context.Context, t *Task, dedupWindow time.Duration) (cur *Task, created bool, err error)
	Get(ctx context.Context, tenant, id string) (*Task, error)
	// Find lists scene tasks newest first; query is a case-insensitive
	// substring of the goal ("" = all).
	Find(ctx context.Context, tenant, scene, query string, limit int) ([]*Task, error)
	// Transition is a compare-and-set on status.
	Transition(ctx context.Context, tenant, id string, from []string, to, result string) (bool, error)
	AddEvent(ctx context.Context, tenant, taskID, kind, detail string) error
	// FailOrphans marks queued/running tasks failed (e.g. after a crash).
	FailOrphans(ctx context.Context, tenant, reason string) (int, error)
	Close() error
}

// KV is short-lived coordination state (dedup keys, locks, counters).
type KV interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, val string, ttl time.Duration) error
	SetNX(ctx context.Context, key, val string, ttl time.Duration) (bool, error)
	Del(ctx context.Context, key string) error
	Lock(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
	Unlock(ctx context.Context, key, token string) (bool, error)
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Close() error
}

// Evidence scopes are machine ids; prompts render them via the i18n key
// "evidence.<scope>".
const (
	ScopeUserSaid     = contract.ScopeUserSaid
	ScopeEmployeeSaid = contract.ScopeEmployeeSaid
	ScopeHostDigest   = contract.ScopeHostDigest
)

var DefaultLimits = contract.DefaultMemoryLimits

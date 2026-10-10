// Package store defines the storage interfaces of the employee runtime (TaskStore, MemoryStore, KV)
// and local/cloud-shaped implementations: SQL (SQLite or PostgreSQL via one dialect-aware impl),
// in-process KV and Redis KV. Every method takes a tenant so multi-tenant migration is a no-op.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrQuota    = errors.New("memory quota exceeded")
	ErrNotFound = errors.New("not found")
	ErrBadQuote = errors.New("quote is not a verbatim substring of the speaker's message")
)

const (
	StQueued    = "queued"
	StRunning   = "running"
	StDone      = "done"
	StFailed    = "failed"
	StCancelled = "cancelled"
)

func ActiveStatus(s string) bool { return s == StQueued || s == StRunning }
func NowMs() int64              { return time.Now().UnixMilli() }

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

type TaskStore interface {
	// Create inserts the task unless an active task (or one finished within dedupWindow) with the same
	// idempotency key exists in the scene; then it returns that task and created=false.
	Create(ctx context.Context, t *Task, dedupWindow time.Duration) (cur *Task, created bool, err error)
	Get(ctx context.Context, tenant, id string) (*Task, error)
	// Find lists scene tasks newest first. query is a case-insensitive substring of goal ('' = all).
	Find(ctx context.Context, tenant, scene, query string, limit int) ([]*Task, error)
	// Transition is a compare-and-set on status. Returns false when the current status is not in from.
	Transition(ctx context.Context, tenant, id string, from []string, to, result string) (bool, error)
	AddEvent(ctx context.Context, tenant, taskID, kind, detail string) error
	// FailOrphans marks queued/running tasks (e.g. after a crash) failed with reason; returns count.
	FailOrphans(ctx context.Context, tenant, reason string) (int, error)
	Close() error
}

type Fact struct {
	ID            string `json:"id"`
	Tenant        string `json:"-"`
	Scene         string `json:"scene"`
	Subject       string `json:"subject"`  // person the fact belongs to (audience=me) or "" for scene
	Audience      string `json:"audience"` // me | scene
	Kind          string `json:"kind"`     // fact|decision|preference|pattern|pitfall|... | digest
	Text          string `json:"text"`
	Quote         string `json:"quote"`        // verbatim excerpt of the speaker's message
	Speaker       string `json:"speaker"`      // who said it
	SpeakerRole   string `json:"speaker_role"` // user | employee | host
	EvidenceScope string `json:"evidence_scope"`
	SourceRef     string `json:"source_ref"`
	SaidMs        int64  `json:"said_ms"`
	CreatedMs     int64  `json:"created_ms"`
	Status        string `json:"status"` // active | archived
	ArchivedBy    string `json:"archived_by,omitempty"`
	TextHash      string `json:"-"`
}

// Evidence scope strings follow the frozen cases: employee speech proves what was said, not execution.
const (
	ScopeUserSaid     = "user_said; the user's own statement (preference/instruction), not proof of any execution"
	ScopeEmployeeSaid = "saying_only; employee speech proves what was said, not business execution or delivery"
	ScopeHostDigest   = "host_compaction; verbatim index of archived facts, no new claim"
)

type MemoryLimits struct {
	MaxFactBytes     int // per fact
	MaxActivePerBag  int // active facts per (tenant,scene,subject,audience)
	MaxBytesPerBag   int
	CompactKeepNewer int // compaction keeps this many newest non-pinned facts
}

var DefaultLimits = MemoryLimits{MaxFactBytes: 600, MaxActivePerBag: 120, MaxBytesPerBag: 24 * 1024, CompactKeepNewer: 60}

type MemoryStore interface {
	// Put validates provenance (quote must be a substring of originalMsg when originalMsg != ""),
	// dedups identical text and enforces quotas (ErrQuota).
	Put(ctx context.Context, f *Fact, originalMsg string) (cur *Fact, created bool, err error)
	Search(ctx context.Context, tenant, scene, subject, audience, query string, limit int) ([]*Fact, error)
	Recent(ctx context.Context, tenant, scene, subject, audience string, limit int) ([]*Fact, error)
	// Archive marks facts archived (never deletes or rewrites text). Used by forget and compaction.
	Archive(ctx context.Context, tenant string, ids []string, by string) (int, error)
	// Compact archives the oldest active non-pinned facts of a bag and writes ONE digest fact that
	// lists the originals verbatim (truncated) - no model summarisation, no rewriting.
	Compact(ctx context.Context, tenant, scene, subject, audience string) (archived int, digestID string, err error)
	CountActive(ctx context.Context, tenant, scene, subject, audience string) (n int, bytes int, err error)
	GetFact(ctx context.Context, tenant, id string) (*Fact, error)
	Close() error
}

type KV interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, val string, ttl time.Duration) error
	SetNX(ctx context.Context, key, val string, ttl time.Duration) (bool, error) // dedup keys
	Del(ctx context.Context, key string) error
	Lock(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
	Unlock(ctx context.Context, key, token string) (bool, error)
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Close() error
}

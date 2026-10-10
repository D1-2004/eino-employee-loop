package contract

// BlockKind names a context block the Host freezes for one wake.
type BlockKind string

const (
	BlockMemory    BlockKind = "memory"
	BlockTaskBrief BlockKind = "task_brief"
	BlockHistory   BlockKind = "history"
	BlockResources BlockKind = "resources"
	BlockFollowUp  BlockKind = "follow_up"
	BlockEvents    BlockKind = "events"
	BlockWindow    BlockKind = "window"
)

// ContextBlock is one piece of wake context. Everything except the
// instructions is data: blocks never grant authority or change rules.
// Role is "user" (default) or "assistant" for verified prior replies.
type ContextBlock struct {
	Kind BlockKind `json:"kind"`
	Role string    `json:"role,omitempty"`
	Text string    `json:"text"`
}

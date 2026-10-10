package core

import "context"

// Inbound is one wake signal from a channel (message, reaction, or other adapter event).
type Inbound struct {
	ConvID  string
	MsgID   string
	Speaker string
	Text    string
	AtMs    int64
	// Kind: "message" (default/empty) | "reaction" | adapter-defined
	Kind string
	// Reaction is an optional signal name when Kind=reaction (opaque to Core).
	Reaction string
	// TargetMsgID is the message that received the reaction, when applicable.
	TargetMsgID string
	// OperatorID is the actor id when the adapter provides one.
	OperatorID string
}

// IMProvider bridges a messaging channel into the employee Runtime.
// Prefer a native event stream when the channel offers one; poll is acceptable for demos.
type IMProvider interface {
	Subscribe(ctx context.Context, convID string) (<-chan Inbound, error)
	// Send delivers a user-facing reply. Prefer rich text when the channel supports it.
	Send(ctx context.Context, convID string, text string) error
	Close() error
}

// ReadMarker is an optional IM capability: mark the conversation read after handling.
type ReadMarker interface {
	MarkRead(ctx context.Context, convID, msgID string) error
	ClearRedPoint(ctx context.Context, convID string) error
}

// HistoryLoader is an optional IM capability: pull recent channel history to seed local context.
type HistoryLoader interface {
	FetchHistory(ctx context.Context, convID string, limit int) ([]Inbound, error)
}

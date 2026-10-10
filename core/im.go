package core

import "context"

// Inbound is one user-visible message from a conversation.
type Inbound struct {
	ConvID  string
	MsgID   string
	Speaker string
	Text    string
	AtMs    int64
}

// IMProvider bridges an IM channel into the employee Runtime.
// DWS has no native subscribe; the DWS provider implements Subscribe via poll.
type IMProvider interface {
	Subscribe(ctx context.Context, convID string) (<-chan Inbound, error)
	// Send delivers a user-facing reply. Implementations should prefer rich text
	// (DingTalk markdown) and must not attach an AI badge when sending as a human profile.
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

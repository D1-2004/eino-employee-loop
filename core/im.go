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
	Send(ctx context.Context, convID string, text string) error
	Close() error
}

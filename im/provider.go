// Package im defines the conversation Provider abstraction used by the local demo
// (stdin) and the DWS consumer (DingTalk Workspace CLI poll + send).
package im

import "context"

// Inbound is one user-visible message from a conversation.
type Inbound struct {
	ConvID  string
	MsgID   string
	Speaker string
	Text    string
	AtMs    int64
}

// Provider bridges an IM channel into the employee Runtime.
// DWS has no native subscribe; DWSProvider implements Subscribe via poll.
type Provider interface {
	Subscribe(ctx context.Context, convID string) (<-chan Inbound, error)
	Send(ctx context.Context, convID string, text string) error
	Close() error
}

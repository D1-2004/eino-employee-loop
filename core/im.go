package core

import (
	"context"
	"strings"
)

// Inbound is one wake signal from a channel (message or reaction).
type Inbound struct {
	ConvID  string
	MsgID   string
	Speaker string
	Text    string
	AtMs    int64
	// Kind: "message" (default/empty) | "reaction"
	Kind string
	// Reaction is the emoji name (e.g. "打叉") when Kind=reaction.
	Reaction string
	// TargetMsgID is the message that received the reaction.
	TargetMsgID string
	// OperatorID is the reactor's openDingTalkId when known.
	OperatorID string
}

// IMProvider bridges an IM channel into the employee Runtime.
// Prefer a native event stream (DWS: `dws event +listen-im`); poll is fallback.
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

// NegativeReaction reports whether an emoji reaction is a correction / reject signal (❌ / 打叉).
func NegativeReaction(name, text string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	t := strings.ToLower(strings.TrimSpace(text))
	for _, x := range []string{"打叉", "拒绝", "弱", "鄙视", "wrong", "sayno", "thumbsdown", "dislike"} {
		if n == x || t == x {
			return true
		}
	}
	blob := name + text
	for _, sub := range []string{"❌", "叉", "不对", "错了"} {
		if strings.Contains(blob, sub) {
			return true
		}
	}
	return false
}

package core

import "context"

// Msg is one durable conversation log entry (nearby dialogue source).
type Msg struct {
	Seq       int64  `json:"seq"`
	Role      string `json:"role"` // user | assistant | host
	Speaker   string `json:"speaker"`
	Text      string `json:"text"`
	Kind      string `json:"kind"` // msg | ack | reply | dispatch_reply | result_reply | imported
	SourceRef string `json:"source_ref"`
	AtMs      int64  `json:"at_ms"`
}

// Conversation is the Host dialogue log. Implementations live under providers/store.
type Conversation interface {
	Append(ctx context.Context, tenant, scene string, m Msg) (Msg, error)
	Recent(ctx context.Context, tenant, scene string, beforeSeq int64, n int) ([]Msg, error)
}

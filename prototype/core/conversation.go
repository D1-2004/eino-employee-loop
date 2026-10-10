package core

import (
	"context"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// Msg is one durable conversation log entry (nearby dialogue source). The
// prototype records these kinds besides the loop's msg, reaction and reply:
// ack, dispatch_reply, result_reply and imported.
type Msg = contract.Msg

// Conversation is the prototype's dialogue log. Unlike contract.Conversation
// it appends every entry (the prototype records several entries per source
// ref and kind). Implementations live in assemblies (e.g. local/).
type Conversation interface {
	Append(ctx context.Context, tenant, scene string, m Msg) (Msg, error)
	Recent(ctx context.Context, tenant, scene string, beforeSeq int64, n int) ([]Msg, error)
}

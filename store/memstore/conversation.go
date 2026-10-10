package memstore

import (
	"context"
	"sync"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// Conversation is an in-memory contract.Conversation: one append-only log
// per (tenant, scene) with a per-log sequence starting at 1.
type Conversation struct {
	mu   sync.Mutex
	logs map[string][]contract.Msg
}

// NewConversation returns an empty conversation log.
func NewConversation() *Conversation { return &Conversation{logs: map[string][]contract.Msg{}} }

var _ contract.Conversation = (*Conversation)(nil)

// Append implements contract.Conversation; it assigns the next Seq and
// returns the stored message for a repeated (SourceRef, Kind).
func (c *Conversation) Append(_ context.Context, tenant, scene string, msg contract.Msg) (contract.Msg, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := tenant + "|" + scene
	log := c.logs[key]
	if msg.SourceRef != "" {
		for _, m := range log {
			if m.SourceRef == msg.SourceRef && m.Kind == msg.Kind {
				return m, nil
			}
		}
	}
	msg.Seq = int64(len(log)) + 1
	c.logs[key] = append(log, msg)
	return msg, nil
}

// Recent implements contract.Conversation: up to n newest messages with
// Seq < beforeSeq (0 = no bound), oldest first.
func (c *Conversation) Recent(_ context.Context, tenant, scene string, beforeSeq int64, n int) ([]contract.Msg, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []contract.Msg
	for _, msg := range c.logs[tenant+"|"+scene] {
		if beforeSeq == 0 || msg.Seq < beforeSeq {
			out = append(out, msg)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return append([]contract.Msg(nil), out...), nil
}

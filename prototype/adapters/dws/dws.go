// Package dws adapts the gateway/dws CLI client to the prototype's
// core.IMProvider. All provider logic lives in gateway/dws; this package
// only converts gateway Messages to core.Inbound.
package dws

import (
	"context"
	"time"

	gw "github.com/D1-2004/eino-employee-loop/gateway/dws"
	"github.com/D1-2004/eino-employee-loop/prototype/core"
)

// Inbound is the prototype's inbound wake signal.
type Inbound = core.Inbound

// DWSConfig configures the provider (see gateway/dws.Config).
type DWSConfig struct {
	Profile           string
	DWSBin            string
	PollEvery         time.Duration
	Limit             int
	SeenPath          string
	SelfNames         []string
	SelfIDs           []string
	Kind              string
	CatchUp           time.Duration
	Transport         string
	AllowPollFallback bool
	StreamRestartMin  time.Duration
}

// DWSProvider is a core.IMProvider over gateway/dws.Client.
type DWSProvider struct{ c *gw.Client }

var (
	_ core.IMProvider    = (*DWSProvider)(nil)
	_ core.ReadMarker    = (*DWSProvider)(nil)
	_ core.HistoryLoader = (*DWSProvider)(nil)
)

// NewDWSProvider returns a provider for cfg.
func NewDWSProvider(cfg DWSConfig) *DWSProvider {
	return &DWSProvider{c: gw.NewClient(gw.Config{
		Profile: cfg.Profile, Bin: cfg.DWSBin, PollEvery: cfg.PollEvery, Limit: cfg.Limit,
		SeenPath: cfg.SeenPath, SelfNames: cfg.SelfNames, SelfIDs: cfg.SelfIDs, Kind: cfg.Kind,
		CatchUp: cfg.CatchUp, Transport: cfg.Transport, AllowPollFallback: cfg.AllowPollFallback,
		StreamRestartMin: cfg.StreamRestartMin,
	})}
}

// Client returns the underlying gateway client.
func (p *DWSProvider) Client() *gw.Client { return p.c }

// Subscribe implements core.IMProvider.
func (p *DWSProvider) Subscribe(ctx context.Context, convID string) (<-chan Inbound, error) {
	src, err := p.c.Subscribe(ctx, convID)
	if err != nil {
		return nil, err
	}
	out := make(chan Inbound, cap(src))
	go func() {
		defer close(out)
		for m := range src {
			select {
			case out <- toInbound(m):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Send implements core.IMProvider.
func (p *DWSProvider) Send(ctx context.Context, convID, text string) error {
	_, err := p.c.Send(ctx, convID, text)
	return err
}

// MarkRead implements core.ReadMarker.
func (p *DWSProvider) MarkRead(ctx context.Context, convID, msgID string) error {
	return p.c.MarkRead(ctx, convID, msgID)
}

// ClearRedPoint implements core.ReadMarker.
func (p *DWSProvider) ClearRedPoint(ctx context.Context, convID string) error {
	return p.c.ClearRedPoint(ctx, convID)
}

// FetchHistory implements core.HistoryLoader.
func (p *DWSProvider) FetchHistory(ctx context.Context, convID string, limit int) ([]Inbound, error) {
	msgs, err := p.c.FetchHistory(ctx, convID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Inbound, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toInbound(m))
	}
	return out, nil
}

// Close implements core.IMProvider.
func (p *DWSProvider) Close() error { return p.c.Close() }

// NegativeReaction reports whether a reaction is a correction signal.
func NegativeReaction(name, text string) bool { return gw.DefaultLexicon().Negative(name, text) }

func toInbound(m gw.Message) Inbound {
	in := Inbound{
		ConvID: m.ConversationID, MsgID: m.MessageID, Speaker: m.SenderName, Text: m.Text,
		AtMs: m.AtMs, OperatorID: m.SenderID,
	}
	if m.Kind == gw.MessageReaction {
		in.Kind, in.Reaction, in.TargetMsgID = string(gw.MessageReaction), m.Reaction, m.TargetMessageID
	}
	return in
}

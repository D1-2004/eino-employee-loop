package im

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StdinProvider reads lines from stdin as inbound messages and prints outbound to stdout.
// Used by cmd/demo for local testing without DingTalk.
type StdinProvider struct {
	In       io.Reader
	Out      io.Writer
	ConvID   string
	Speaker  string
	mu       sync.Mutex
	closed   atomic.Bool
	ch       chan Inbound
	seq      int64
}

func NewStdinProvider(convID, speaker string) *StdinProvider {
	if convID == "" {
		convID = "stdin"
	}
	if speaker == "" {
		speaker = "you"
	}
	return &StdinProvider{
		In: os.Stdin, Out: os.Stdout,
		ConvID: convID, Speaker: speaker,
	}
}

func (p *StdinProvider) Subscribe(ctx context.Context, convID string) (<-chan Inbound, error) {
	if convID != "" {
		p.ConvID = convID
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch != nil {
		return p.ch, nil
	}
	p.ch = make(chan Inbound, 8)
	go p.readLoop(ctx)
	return p.ch, nil
}

func (p *StdinProvider) readLoop(ctx context.Context) {
	defer close(p.ch)
	sc := bufio.NewScanner(p.In)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		if p.closed.Load() {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Prompt on Out so interactive demos feel natural.
		fmt.Fprint(p.Out, "> ")
		if !sc.Scan() {
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "/quit" || line == "/exit" {
			return
		}
		id := atomic.AddInt64(&p.seq, 1)
		msg := Inbound{
			ConvID:  p.ConvID,
			MsgID:   fmt.Sprintf("stdin-%d", id),
			Speaker: p.Speaker,
			Text:    line,
			AtMs:    time.Now().UnixMilli(),
		}
		select {
		case p.ch <- msg:
		case <-ctx.Done():
			return
		}
	}
}

func (p *StdinProvider) Send(_ context.Context, _ string, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	_, err := fmt.Fprintf(p.Out, "  [bot] %s\n", text)
	return err
}

func (p *StdinProvider) Close() error {
	p.closed.Store(true)
	return nil
}

package dws

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dingtalk-fde/eino-employee-loop/core"
)

// Regression: Subscribe used to hold p.mu across pollOnce, which also Lock()'s — deadlock.
func TestSubscribeDoesNotDeadlockOnSeed(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-dws.sh")
	script := "#!/bin/sh\necho '{\"messages\":[{\"messageId\":\"m1\",\"sender\":\"u\",\"senderId\":\"s\",\"text\":\"hi\",\"createTime\":\"2026-10-10 12:00:00\"}]}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewDWSProvider(DWSConfig{
		DWSBin:    fake,
		Kind:      "group",
		SeenPath:  filepath.Join(dir, "seen.json"),
		PollEvery: time.Hour,
		CatchUp:   time.Minute,
		Transport: "poll",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	var ch <-chan Inbound
	var err error
	go func() {
		ch, err = p.Subscribe(ctx, "cidTEST")
		close(done)
	}()
	select {
	case <-done:
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if ch == nil {
			t.Fatal("nil channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe deadlocked (did not return within 2s)")
	}
	_ = p.Close()
}

func TestParseStreamEventReactionAndMessage(t *testing.T) {
	line := `{"type":"user_im_message_reaction_group","event_id":"e1","message_id":"m9","operator":"冬翔","operator_open_dingtalk_id":"u1","reaction_name":"打叉","reaction_text":"","operation_type":"add","event_time":1710000000000}`
	in, ok := parseStreamEvent(line, "cidX")
	if !ok || in.Kind != "reaction" || in.Reaction != "打叉" || in.TargetMsgID != "m9" || in.Speaker != "冬翔" {
		t.Fatalf("%+v ok=%v", in, ok)
	}
	if !core.NegativeReaction(in.Reaction, in.Text) {
		t.Fatal("打叉 should be negative")
	}
	msg := `{"type":"user_im_message_receive_group","message_id":"m2","sender":"冬翔","sender_open_dingtalk_id":"u1","content":"Hi","event_time":1710000001000}`
	in2, ok2 := parseStreamEvent(msg, "cidX")
	if !ok2 || in2.Kind != "message" || in2.Text != "Hi" {
		t.Fatalf("%+v ok=%v", in2, ok2)
	}
}

package dws

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeBin(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-dws.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Regression: Subscribe used to hold the mutex across pollOnce, which also
// locks it.
func TestSubscribeDoesNotDeadlockOnSeed(t *testing.T) {
	bin := fakeBin(t, `echo '{"messages":[{"messageId":"m1","sender":"u","senderId":"s","text":"hi","createTime":"2026-10-10 12:00:00"}]}'`+"\n")
	c := NewClient(Config{Bin: bin, Kind: KindGroup, SeenPath: filepath.Join(t.TempDir(), "seen.json"),
		PollEvery: time.Hour, CatchUp: time.Minute, Transport: TransportPoll})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	var ch <-chan Message
	var err error
	go func() {
		ch, err = c.Subscribe(ctx, "cidTEST")
		close(done)
	}()
	select {
	case <-done:
		if err != nil || ch == nil {
			t.Fatalf("subscribe: %v %v", ch, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe deadlocked")
	}
	_ = c.Close()
}

func TestSendReturnsProviderIDAndHistoryIsOldestFirst(t *testing.T) {
	bin := fakeBin(t, `case "$*" in
*messages-send*) echo '{"data":{"openMsgId":"sent-1"}}';;
*) echo '{"messages":[{"messageId":"m2","senderId":"b","sender":"Bob","text":"second"},{"messageId":"m1","senderId":"a","sender":"Ann","text":"first"},{"messageId":"m0","text":"  "}]}';;
esac
`)
	c := NewClient(Config{Bin: bin, Kind: KindDM})
	id, err := c.Send(context.Background(), "peer", "hello")
	if err != nil || id != "sent-1" {
		t.Fatalf("send id=%q err=%v", id, err)
	}
	if id, err := c.Send(context.Background(), "peer", "  "); err != nil || id != "" {
		t.Fatalf("empty send id=%q err=%v", id, err)
	}
	hist, err := c.FetchHistory(context.Background(), "peer", 5)
	if err != nil || len(hist) != 2 || hist[0].MessageID != "m1" || hist[1].Text != "second" || !hist[0].Single || hist[0].SenderID != "a" {
		t.Fatalf("history %+v err %v", hist, err)
	}
}

func TestParseStreamEventReactionAndMessage(t *testing.T) {
	// The reaction name is a provider identifier written as a JSON escape.
	line := `{"type":"user_im_message_reaction_group","event_id":"e1","message_id":"m9","operator":"Ann","operator_open_dingtalk_id":"u1","reaction_name":"\u6253\u53c9","reaction_text":"","operation_type":"add","event_time":1710000000000}`
	m, ok, err := parseStreamEvent(line, "cidX", false)
	if err != nil || !ok || m.Kind != MessageReaction || m.Reaction != "\u6253\u53c9" || m.TargetMessageID != "m9" || m.SenderName != "Ann" || m.SenderID != "u1" {
		t.Fatalf("%+v ok=%v err=%v", m, ok, err)
	}
	if !DefaultLexicon().Negative(m.Reaction, m.Text) {
		t.Fatalf("%q is a negative reaction in the default configuration", m.Reaction)
	}
	removed := `{"type":"user_im_message_reaction_group","event_id":"e2","message_id":"m9","reaction_name":"\u6253\u53c9","operation_type":"remove"}`
	if _, ok, _ := parseStreamEvent(removed, "cidX", false); ok {
		t.Fatal("removals are ignored")
	}
	msg := `{"type":"user_im_message_receive_group","message_id":"m2","sender":"Bob","sender_open_dingtalk_id":"u1","content":"Hi","event_time":1710000001000}`
	m2, ok2, _ := parseStreamEvent(msg, "cidX", false)
	if !ok2 || m2.Kind != MessageText || m2.Text != "Hi" || m2.ConversationID != "cidX" || m2.Single {
		t.Fatalf("%+v ok=%v", m2, ok2)
	}
}

func TestParseStreamEventAddressingFields(t *testing.T) {
	line := `{"type":"im_message","data":{"message_id":"m3","sender_open_dingtalk_id":"u2","content":"look","conversation_id":"cidY",` +
		`"conversation_type":"2","at_users":[{"open_dingtalk_id":"bot"},"other"],"is_in_at_list":true,` +
		`"quote_message_id":"m1","quote_sender_open_dingtalk_id":"bot"}}`
	m, ok, err := parseStreamEvent(line, "cidX", false)
	if err != nil || !ok || m.ConversationID != "cidY" || len(m.MentionIDs) != 2 || m.MentionIDs[0] != "bot" ||
		!m.MentionsSelf || m.ReplyToMessageID != "m1" || m.ReplyToSenderID != "bot" || m.Single {
		t.Fatalf("%+v ok=%v err=%v", m, ok, err)
	}
	single := `{"type":"im_message","message_id":"m4","sender_open_dingtalk_id":"u2","content":"hi","conversation_type":1}`
	if m, ok, _ := parseStreamEvent(single, "cidX", false); !ok || !m.Single {
		t.Fatalf("single chat: %+v", m)
	}
	if _, _, err := parseStreamEvent("{", "cidX", false); err == nil {
		t.Fatal("bad json is reported")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 10, 5, 0, 0, 0, time.UTC)
	if d := parseRetryAfter("rate limited. Retry After: 518s", now); d != 523*time.Second {
		t.Fatalf("retry after: %v", d)
	}
	if d := parseRetryAfter("subscription attempt cooldown until 2026-10-10T05:10:00Z.", now); d != 10*time.Minute+5*time.Second {
		t.Fatalf("cooldown: %v", d)
	}
	if d := parseRetryAfter("ok", now); d != 0 {
		t.Fatalf("none: %v", d)
	}
}

func TestLexiconMatchesExactNamesByDefault(t *testing.T) {
	l := DefaultLexicon()
	for _, name := range []string{"thumbsdown", " Thumbs Down ", "\u274c", "\U0001f44e"} {
		if !l.Negative(name, "") {
			t.Errorf("%q is negative", name)
		}
	}
	for _, name := range []string{"thumbsup", "not wrong at all", "\u53c9\u70e7", "wrong!"} {
		if l.Negative(name, "") || l.Negative("", name) {
			t.Errorf("%q is not an exact negative identifier", name)
		}
	}
	custom := NewLexicon(LexiconConfig{NegativeNames: []string{"nope"}, NegativeSubstrings: []string{"bad"}})
	if !custom.Negative("NOPE", "") || !custom.Negative("", "too bad") || custom.Negative("thumbsdown", "") {
		t.Fatal("a configured lexicon replaces the default")
	}
}

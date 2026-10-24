package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	loop "github.com/D1-2004/eino-employee-loop/kernel/decision"
	"github.com/D1-2004/eino-employee-loop/kernel/decision/hosttest"
	lt "github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
)

type fakeChannel struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeChannel) Listen(context.Context, func(Message)) error { return nil }

func (f *fakeChannel) Send(ctx context.Context, m Message, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m.ConversationID+"|"+text)
	hosttest.Committed(ctx)
	return "sent-" + m.MessageID, nil
}

// count is the number of sent messages: the effects of the chat Host.
func (f *fakeChannel) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func dm(id, text string) Message {
	return Message{ConversationID: "c1", MessageID: id, SenderID: "u1", SenderName: "Ann", Text: text, Direct: true}
}

func TestAddressedMessageIsAnsweredThroughTheReplyTool(t *testing.T) {
	ch := &fakeChannel{}
	model := lt.New(lt.Tools(lt.Call{ID: "c1", Name: "reply", Args: map[string]any{"text": "4"}}))
	e := &Employee{ID: "e1", Name: "Eel", Model: model, Channel: ch, Locale: "zh-CN"}
	out, err := e.Answer(context.Background(), dm("m1", "2+2?"))
	if err != nil || out.Kind != loop.Reply || len(ch.sent) != 1 || ch.sent[0] != "c1|4" || out.Receipts[0].ID != "sent-m1" {
		t.Fatalf("out=%+v err=%v sent=%v", out, err, ch.sent)
	}
	system, user := model.Requests[0][0].Content, model.Requests[0][1].Content
	if !strings.Contains(system, "Eel") || !strings.Contains(system, "reply") || !strings.Contains(user, `"message_id":"m1"`) {
		t.Fatalf("prompt: %q / %q", system, user)
	}
}

func TestGroupMessageWithoutMentionIsOnlyRemembered(t *testing.T) {
	ch := &fakeChannel{}
	model := lt.New(lt.Text("ok"))
	e := &Employee{ID: "e1", Model: model, Channel: ch}
	if _, err := e.Answer(context.Background(), Message{ConversationID: "g1", MessageID: "m1", SenderID: "u1", SenderName: "Ann", Text: "lunch at noon"}); err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 0 || len(ch.sent) != 0 {
		t.Fatal("an observed message does not wake the foreground")
	}
	mention := Message{ConversationID: "g1", MessageID: "m2", SenderID: "u2", SenderName: "Bob", Text: "when is lunch?", Mentioned: true}
	out, err := e.Answer(context.Background(), mention)
	if err != nil || out.Reply != "ok" || !strings.Contains(model.Requests[0][1].Content, "Ann: lunch at noon") {
		t.Fatalf("history reaches the next wake: out=%+v err=%v", out, err)
	}
}

func TestOneReplyPerWake(t *testing.T) {
	ch := &fakeChannel{}
	model := lt.New(
		lt.Tools(lt.Call{ID: "c1", Name: "reply", Args: map[string]any{"text": "a"}}, lt.Call{ID: "c2", Name: "reply", Args: map[string]any{"text": "b"}}),
	)
	out, err := (&Employee{ID: "e1", Model: model, Channel: ch}).Answer(context.Background(), dm("m1", "hi"))
	if err != nil || len(ch.sent) != 1 || out.Reply != "a" {
		t.Fatalf("out=%+v err=%v sent=%v", out, err, ch.sent)
	}
}

func TestParseDWSEvent(t *testing.T) {
	line := `{"type":"im_message","data":{"message_id":"m3","sender":"Ann","sender_open_dingtalk_id":"u2","content":"look","conversation_id":"cidY","conversation_type":"1","is_in_at_list":false}}`
	m, ok, err := parseDWSEvent([]byte(line))
	if err != nil || !ok || m.MessageID != "m3" || m.SenderID != "u2" || !m.Direct || m.Text != "look" || m.ConversationID != "cidY" {
		t.Fatalf("m=%+v ok=%v err=%v", m, ok, err)
	}
	// The flat record of a live o2o subscription (2026-10-11).
	flat := `{"type":"user_im_message_receive_o2o_all","event_id":"e1","timestamp":1791692922537,"message_id":"msgA==","conversation_id":"cidB=","sender":"Director","sender_open_dingtalk_id":"DnG0","content":"5 + 6?","create_time":"2026-10-11 12:28:42","event_time":1791692922077}`
	m, ok, err = parseDWSEvent([]byte(flat))
	if err != nil || !ok || m.MessageID != "msgA==" || m.SenderID != "DnG0" || m.Text != "5 + 6?" || m.At.UnixMilli() != 1791692922077 {
		t.Fatalf("flat m=%+v ok=%v err=%v", m, ok, err)
	}
	if _, ok, _ := parseDWSEvent([]byte(`{"type":"im_reaction","data":{"content":"x"}}`)); ok {
		t.Fatal("non-message events are skipped")
	}
	if sentID([]byte(`{"data":{"openMsgId":"sent-1"}}`)) != "sent-1" {
		t.Fatal("sent id")
	}
}

func TestDWSIgnoresItsOwnEcho(t *testing.T) {
	d := &DWS{sent: map[string]bool{"c1|17": true}}
	if !d.own(Message{ConversationID: "c1", SenderID: "self", Text: "17"}) || d.self != "self" {
		t.Fatal("the echo of a sent text is ours and reveals self")
	}
	if !d.own(Message{ConversationID: "c2", SenderID: "self", Text: "other"}) || d.own(Message{ConversationID: "c1", SenderID: "u1", Text: "17"}) {
		t.Fatal("later messages from self are ours, other senders are not")
	}
}

func TestTraceUsesTheEmployeeLoopShape(t *testing.T) {
	var batch struct {
		Batch []struct {
			Type string         `json:"type"`
			Body map[string]any `json:"body"`
		} `json:"batch"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &batch)
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`{"successes":[],"errors":[]}`))
	}))
	defer srv.Close()
	step := lt.Tools(lt.Call{ID: "c1", Name: "reply", Args: map[string]any{"text": "hi"}})
	step.Message.ResponseMeta.Usage = &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}
	e := &Employee{ID: "e1", Model: lt.New(step), ModelName: "m", Channel: &fakeChannel{}, Tracer: &Tracer{BaseURL: srv.URL, PublicKey: "pk", SecretKey: "sk", Environment: "local"}}
	if _, err := e.Answer(context.Background(), dm("m9", "hello")); err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, item := range batch.Batch {
		types[item.Type]++
	}
	if types["trace-create"] != 1 || types["generation-create"] != 1 || types["tool-create"] != 1 {
		t.Fatalf("batch types %v", types)
	}
	trace := batch.Batch[0].Body
	meta, _ := trace["metadata"].(map[string]any)
	if trace["name"] != "employee_loop" || trace["sessionId"] != "c1" || meta["employee_loop_engine"] != "eino" || meta["employee_contract_event"] != "message.addressed" || meta["employee_decision"] != "reply" {
		t.Fatalf("trace %v", trace)
	}
	gen := batch.Batch[1].Body
	if gen["name"] != "employee_model" || !strings.Contains(mustJSON(gen["input"]), "m9") {
		t.Fatalf("generation %v", gen)
	}
	// Langfuse v3 counts tokens from usageDetails only.
	if mustJSON(gen["usageDetails"]) != `{"input":5,"output":2,"total":7}` {
		t.Fatalf("usage %v", gen["usageDetails"])
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestLocalePacksHaveTheSameKeys(t *testing.T) {
	packsOnce.Do(loadPacks)
	for tag, pack := range packs {
		for key := range packs["en"] {
			if pack[key] == "" {
				t.Errorf("%s lacks %s", tag, key)
			}
		}
		if len(pack) != len(packs["en"]) {
			t.Errorf("%s has %d keys, en has %d", tag, len(pack), len(packs["en"]))
		}
	}
}

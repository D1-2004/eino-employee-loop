package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	loop "github.com/D1-2004/eino-employee-loop/employee/kernel/decision"
	lt "github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
)

func fakeDWS(t *testing.T, response, status string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake CLI")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "dws")
	argv := filepath.Join(dir, "argv")
	for name, data := range map[string]string{"reply.json": response, "status.json": status} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> '` + argv + `'
case "$2" in
+conversation-info) printf '%s\n' '{"success":true,"result":{"conversationInfo":{"openConversationId":"c1"}}}' ;;
+messages-reply) cat '` + filepath.Join(dir, "reply.json") + `' ;;
+messages-query-send-status) cat '` + filepath.Join(dir, "status.json") + `' ;;
*) echo 'unsupported command' >&2; exit 3 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return bin, argv
}

func TestDWSReplyArgsCommonCLISubset(t *testing.T) {
	got := strings.Join(replyArgs("c1", dm("m1", "?"), "answer"), " ")
	want := "chat +messages-reply --group c1 --message-id m1 --content answer --ai-tag=false --uuid eel-m1 -y -f json --ref-sender u1"
	if got != want {
		t.Fatalf("got %s", got)
	}
	for _, bad := range []string{"--as ", "--open-dingtalk-id "} {
		if strings.Contains(got, bad) {
			t.Fatal(got)
		}
	}
}

func TestDWSReplyThroughHostAndVerifiedAsyncReceipt(t *testing.T) {
	bin, argv := fakeDWS(t, `{"identity":"user","sourceMessageId":"m1","conversationId":"c1","result":{"openTaskId":"task1"}}`, `{"success":true,"openTaskId":"task1","result":{"status":"SUCCESS","openMessageId":"sent1","openConversationId":"c1"},"messageRef":{"openMessageId":"sent1","openConversationId":"c1"}}`)
	d := &DWS{Bin: bin, Profile: "corp:employee"}
	e := &Employee{ID: "employee", Channel: d}
	h := &chatHost{employee: e, message: dm("m1", "hi")}
	call := replyCall("c1", "hello")
	result, err := h.Execute(context.Background(), loop.Identity{}, call)
	if err != nil || result.Receipt != "sent1" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	again, err := h.Execute(context.Background(), loop.Identity{}, call)
	if err != nil || again.Receipt != result.Receipt {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	raw, _ := os.ReadFile(argv)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("reply was repeated: %s", raw)
	}
	if lines[0] != strings.Join(replyArgs("c1", dm("m1", "hi"), "hello"), " ")+" --profile corp:employee" {
		t.Fatal(lines[0])
	}
	if lines[1] != "chat +messages-query-send-status --open-task-id task1 -f json --profile corp:employee" {
		t.Fatal(lines[1])
	}
	// The same words from a peer cannot cause the account to learn the peer as self.
	if d.own(Message{MessageID: "peer1", SenderID: "peer", Text: "hello", ConversationID: "c1"}) {
		t.Fatal("text is not identity proof")
	}
	if !d.own(Message{MessageID: "sent1", SenderID: "self", Text: "hello", ConversationID: "c1"}) {
		t.Fatal("confirmed echo not suppressed")
	}
	if !d.first("m1") || d.first("m1") {
		t.Fatal("two stream duplicates not suppressed")
	}
}

func TestDWSMissingDirectConversationUsesSameProfile(t *testing.T) {
	bin, argv := fakeDWS(t, `{"success":true,"deliveryStatus":"SUCCESS","messageId":"sent1","conversationId":"c1"}`, "")
	d := &DWS{Bin: bin, Profile: "corp:employee"}
	m := dm("m1", "hi")
	m.ConversationID = ""
	id, err := d.Send(context.Background(), m, "hello")
	if err != nil || id != "sent1" {
		t.Fatalf("id=%s err=%v", id, err)
	}
	raw, _ := os.ReadFile(argv)
	if !strings.HasPrefix(string(raw), "chat +conversation-info --open-dingtalk-id u1 -f json --profile corp:employee\n") || !strings.Contains(string(raw), "--group c1") {
		t.Fatal(string(raw))
	}
	if !d.own(Message{MessageID: id, SenderID: "self", ConversationID: "c1"}) {
		t.Fatal("looked-up direct conversation echo")
	}
}

func TestDWSRefusesUnverifiedReceipts(t *testing.T) {
	cases := []string{
		`{"_mock":true,"status":"SUCCESS","messageId":"sent1","conversationId":"c1"}`,
		`{}`, `{"success":true,"deliveryStatus":"unknown","conversationId":"c1"}`,
		`{"success":false,"status":"SUCCESS","messageId":"sent1","conversationId":"c1"}`,
		`{"success":true,"status":"SUCCESS","referencedMessage":{"messageId":"m1","conversationId":"c1"}}`,
		`{"status":"SUCCESS","messageId":"sent1","conversationId":"wrong"}`,
		`{"status":"SUCCESS","messageId":"m1","conversationId":"c1"}`,
		`{"identity":"bot","status":"SUCCESS","messageId":"sent1","conversationId":"c1"}`,
		`{"sourceMessageId":"other","status":"SUCCESS","messageId":"sent1","conversationId":"c1"}`,
		`{"dryRun":true,"status":"SUCCESS","messageId":"sent1","conversationId":"c1"}`,
		`{"status":"SUCCESS","messageId":"sent1","conversationId":"c1","messageRef":{"openMessageId":"other"}}`,
		`{"openTaskId":"task1"}`,
	}
	for i, response := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			bin, _ := fakeDWS(t, response, `{"openTaskId":"task1","status":"PENDING"}`)
			d := &DWS{Bin: bin}
			if id, err := d.Send(context.Background(), dm("m1", "hi"), "hello"); err == nil || id != "" {
				t.Fatalf("id=%s err=%v", id, err)
			}
			if len(d.sent) != 0 {
				t.Fatal("unconfirmed reply recorded as sent")
			}
		})
	}
}

func TestDWSUnknownReceiptDoesNotBecomeSuccessfulWake(t *testing.T) {
	bin, _ := fakeDWS(t, `{"success":true,"deliveryStatus":"unknown","referencedMessage":{"messageId":"m1","conversationId":"c1"}}`, "")
	e := &Employee{ID: "e1", Channel: &DWS{Bin: bin}, Model: lt.New(lt.Text("hello"))}
	out, err := e.Answer(context.Background(), dm("m1", "hi"))
	if err == nil || len(out.Receipts) > 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestDWSSeenWindowIsBounded(t *testing.T) {
	d := &DWS{}
	for i := 0; i < 4200; i++ {
		if !d.first(fmt.Sprint(i)) {
			t.Fatal(i)
		}
	}
	if len(d.seen) != 4096 || len(d.seenOrder) != 4096 || d.first("4199") || !d.first("0") {
		t.Fatalf("seen=%d order=%d", len(d.seen), len(d.seenOrder))
	}
}

func TestDWSParsesSingletonStatusResult(t *testing.T) {
	r, err := parseDWSReceipt([]byte(`{"openTaskId":"task1","result":[{"status":"SUCCESS","openMessageId":"sent1","openConversationId":"c1"}],"messageRef":{"openMessageId":"sent1","openConversationId":"c1"}}`))
	if err != nil || r.status != "SUCCESS" || r.id != "sent1" {
		t.Fatalf("receipt=%+v err=%v", r, err)
	}
}

func TestDWSReportsCLIStderr(t *testing.T) {
	bin, _ := fakeDWS(t, "", "")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'unknown flag: --as' >&2\nexit 3\n"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := (&DWS{Bin: bin}).Send(context.Background(), dm("m1", "hi"), "hello")
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --as") {
		t.Fatalf("err=%v", err)
	}
}

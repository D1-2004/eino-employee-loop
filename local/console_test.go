package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// consoleEmployee wires a console over input to an employee with a scripted
// model and a debug log.
func consoleEmployee(t *testing.T, script *Script, input io.Reader, bodies bool) (*Employee, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, logs bytes.Buffer
	return &Employee{
		ID: "e1", Name: "Eel", Locale: "zh-CN", Model: script, Permanent: script.Permanent, ModelName: "script", Source: "console",
		Channel:   &Console{In: input, Out: &out, User: "Ann", Employee: "Eel", Echo: true},
		Log:       slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		LogBodies: bodies,
	}, &out, &logs
}

func TestConsoleRunsTheScriptedConversation(t *testing.T) {
	script, err := LoadScript("testdata/console.json")
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open("testdata/console.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	e, out, logs := consoleEmployee(t, script, input, false)
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("run ends cleanly at EOF: %v", err)
	}

	want := `[dm] Ann: 你好
[dm] Eel: 你好！有什么可以帮你？
[group] Bob: 今天午饭改到 12 点半
[group] (not addressed: Eel keeps it as history)
[group] Bob: @Eel 午饭几点？
[group] Eel: 午饭改到 12 点半了。
[dm] Ann: 收到，不用回
[dm] (Eel stays quiet: 对方说不用回复)
`
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
	// The observed group message woke nothing and reached the @ wake as history.
	if script.Calls() != 3 {
		t.Fatalf("model calls %d, want 3", script.Calls())
	}
	group := script.Requests[1][1].Content
	if !strings.Contains(group, "Bob: 今天午饭改到 12 点半") || !strings.Contains(group, `"direct":false`) {
		t.Fatalf("group wake prompt: %s", group)
	}
	if dm := script.Requests[2][1].Content; !strings.Contains(dm, "Eel（你）: 你好！有什么可以帮你？") {
		t.Fatalf("the direct conversation keeps its own history: %s", dm)
	}

	log := logs.String()
	for _, line := range []string{
		"msg=local.route conversation=console-dm",
		"event=message.addressed class=business mode=triggered lane=foreground session=console-dm/interactive",
		"event=message.observed class=business mode=listening lane=background session=console-group/observation",
		"attempt=1 messages=2 roles=system:1,user:1 tools=reply,stay_quiet",
		"finish=tool_calls tool_calls=reply",
		"finish=stop text_chars=12",
		"msg=local.tool tool=reply call=call_1_1 args=text",
		"decision=reply tools=reply receipts=1 model_calls=1",
		"decision=reply tools=host_final_text",
		"msg=local.tool tool=stay_quiet call=call_3_1 args=reason",
		"decision=quiet tools=stay_quiet",
	} {
		if !strings.Contains(log, line) {
			t.Errorf("debug log lacks %q", line)
		}
	}
	// The final text is logged like a tool call, with the receipt of the reply.
	if !regexp.MustCompile(`msg=local\.tool tool=host_final_text args=text elapsed=\S+ receipt=console-\w+-3-reply terminal=reply\n`).MatchString(log) {
		t.Error("debug log lacks the final text's local.tool line")
	}
	for _, body := range []string{"你好", "午饭", "不用回"} {
		if strings.Contains(log, body) {
			t.Errorf("debug log shows the body %q without LogBodies", body)
		}
	}
	if t.Failed() {
		t.Log(log)
	}
}

func TestConsoleDebugLogShowsBodiesOnRequest(t *testing.T) {
	script, err := LoadScript("testdata/console.json")
	if err != nil {
		t.Fatal(err)
	}
	e, _, logs := consoleEmployee(t, script, strings.NewReader("你好\n"), true)
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"text=你好", "request=", "response=", "arguments=", "reply=你好！有什么可以帮你？"} {
		if !strings.Contains(logs.String(), body) {
			t.Errorf("debug log lacks %q:\n%s", body, logs)
		}
	}
}

func TestConsoleReportsAScriptThatRanOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one.json")
	if err := os.WriteFile(path, []byte(`[{"text": "one"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := LoadScript(path)
	if err != nil {
		t.Fatal(err)
	}
	e, out, _ := consoleEmployee(t, script, strings.NewReader("a\nb\n"), false)
	err = e.Run(context.Background())
	if err == nil || err.Error() != "console: 1 of 2 wakes failed" {
		t.Fatalf("run error %v", err)
	}
	if !strings.Contains(out.String(), "[dm] (Eel failed: model script "+path+" ran out after 1 responses: looptest: script exhausted)") {
		t.Fatalf("output:\n%s", out)
	}
	if script.Calls() != 2 {
		t.Fatalf("an exhausted script is not retried: %d calls", script.Calls())
	}
}

func TestConsoleStopsWithItsContext(t *testing.T) {
	stdin, w := io.Pipe()
	defer w.Close()
	e, _, _ := consoleEmployee(t, &Script{}, stdin, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the console waits on its input after the context ended")
	}
}

func TestConsoleLineSyntax(t *testing.T) {
	c := &Console{User: "Ann"}
	for line, want := range map[string]Message{
		"hello":           {ConversationID: ConsoleDirect, SenderName: "Ann", Text: "hello", Direct: true},
		"@Bob: when?":     {ConversationID: ConsoleGroup, SenderName: "Bob", Text: "when?", Mentioned: true},
		"@小王：几点？":         {ConversationID: ConsoleGroup, SenderName: "小王", Text: "几点？", Mentioned: true},
		"#Bob: lunch":     {ConversationID: ConsoleGroup, SenderName: "Bob", Text: "lunch"},
		"@ see http://x":  {ConversationID: ConsoleGroup, SenderName: "Ann", Text: "see http://x", Mentioned: true},
		"note: dm colons": {ConversationID: ConsoleDirect, SenderName: "Ann", Text: "note: dm colons", Direct: true},
	} {
		m, ok := c.message(line)
		if !ok || m.ConversationID != want.ConversationID || m.SenderName != want.SenderName || m.SenderID != want.SenderName ||
			m.Text != want.Text || m.Direct != want.Direct || m.Mentioned != want.Mentioned || m.MessageID == "" {
			t.Errorf("%q: %+v", line, m)
		}
	}
	for _, line := range []string{"", "   ", "@", "#Bob:"} {
		if _, ok := c.message(line); ok {
			t.Errorf("%q is not a message", line)
		}
	}
}

func TestLoadScriptRejectsAmbiguousResponses(t *testing.T) {
	for _, doc := range []string{
		`[{"text": "a", "tool_calls": [{"name": "reply", "arguments": {"text": "b"}}]}]`,
		`[{}]`,
		`[{"tool_calls": [{"arguments": {}}]}]`,
		`[{"txt": "typo"}]`,
		`[{"text": "a"}] [{"text": "b"}]`,
		`[{"text": "a"}]]`,
	} {
		path := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadScript(path); err == nil {
			t.Errorf("%s loads", doc)
		}
	}
}

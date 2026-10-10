package local

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/prototype/core"
)

// The Host greeting reply follows Config.Locale, while greeting detection
// matches every loaded locale.
func TestGreetingReplyFollowsLocale(t *testing.T) {
	cases := []struct {
		locale, in, want string
	}{
		{"en", "Hi", "Hi, I'm here. Just tell me what you need."},
		{"en", "你好", "Hi, I'm here. Just tell me what you need."},
		{"zh-CN", "你好", "你好，我在。有事直接说。"},
		{"zh-CN", "谢谢", "不客气。"},
	}
	for _, c := range cases {
		t.Run(c.locale+"/"+c.in, func(t *testing.T) {
			ctx := context.Background()
			stack, err := Open(ctx, StackOpts{DBPath: filepath.Join(t.TempDir(), "loc.db"), Executor: "mock", Scene: "loc", Locale: c.locale})
			if err != nil {
				t.Fatal(err)
			}
			defer stack.Close()
			var replies []string
			cfg := core.DefaultConfig("test", "loc", spinProvider{m: &spinModel{}}, core.DemoCase(nil, c.locale, ""))
			cfg.Locale = c.locale
			cfg.Ack = false
			cfg.OnOutbound = func(_ context.Context, kind, text string) {
				if kind == "reply" {
					replies = append(replies, text)
				}
			}
			rt, err := core.NewRuntime(ctx, cfg, stack.SQL, stack.SQL, stack.KV, stack.Conv, core.NewStats())
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Stop()
			rt.UserMessage(ctx, "someone", c.in, 0)
			if !rt.WaitIdle(false, 5*time.Second) {
				t.Fatal("timeout")
			}
			if len(replies) != 1 || replies[0] != c.want {
				t.Fatalf("replies=%q want %q", replies, c.want)
			}
		})
	}
}

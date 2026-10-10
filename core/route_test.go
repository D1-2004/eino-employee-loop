package core

import (
	"strings"
	"testing"
)

func TestHostDirectGreeting(t *testing.T) {
	for _, in := range []string{"Hi", "你好", "你好呀", "hello", "早"} {
		got, ok := hostDirectReply(in)
		if !ok || got == "" {
			t.Fatalf("%q: expected Host direct reply, got %q ok=%v", in, got, ok)
		}
		for _, bad := range []string{"天气", "钓鱼", "任务", "记忆", "进度"} {
			if strings.Contains(got, bad) {
				t.Fatalf("%q reply leaked %q: %s", in, bad, got)
			}
		}
	}
}

func TestHostDirectArithmetic(t *testing.T) {
	cases := map[string]string{"1+1": "2", "1＋1": "2", "3*3": "9", "10/2": "5", "1+1=": "2"}
	for in, want := range cases {
		got, ok := hostDirectReply(in)
		if !ok || got != want {
			t.Fatalf("%q: got %q ok=%v want %q", in, got, ok, want)
		}
	}
}

func TestVagueFragment(t *testing.T) {
	if !isVagueFragment("好吧 模型请求的事件") {
		t.Fatal("expected vague")
	}
	if isVagueFragment("帮我查一下杭州天气") {
		t.Fatal("actionable should not be vague")
	}
	if isVagueFragment("Hi") {
		t.Fatal("greeting not vague")
	}
}

func TestHostRouteHintVague(t *testing.T) {
	h := hostRouteHint("好吧 模型请求的事件")
	if h == "" || !strings.Contains(h, "ask_human") {
		t.Fatalf("hint=%q", h)
	}
}

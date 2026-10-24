package channel

import (
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/modules/entry"
)

func TestReplayBindsCapturedIdentityAndPreservesOriginal(t *testing.T) {
	opts := ReplayOptions{Owner: "owner", Scenes: []string{"scene"}, NativeOwner: "native-owner", NativeScenes: map[string]string{"native-scene": "scene"}}
	raw := `{"type":"message","data":{"message_id":"m1","conversation_id":"native-scene","sender_open_dingtalk_id":"native-owner","content":"  exact text\nwith newline  "}}`
	in, ok, err := DecodeReplay([]byte(raw), opts)
	if err != nil || !ok || in.ID != "m1" || in.Scene != "scene" || in.Actor != "owner" || in.Text != "  exact text\nwith newline  " || string(in.Native) != raw || in.Origin != "local-dws-capture-replay" {
		t.Fatalf("input=%+v ok=%v err=%v", in, ok, err)
	}
	for _, bad := range []string{
		strings.Replace(raw, "native-owner", "someone-else", 1),
		strings.Replace(raw, "native-scene", "outside-scene", 1),
		strings.Replace(raw, `"message_id":"m1"`, `"message_id":""`, 1),
		strings.Replace(raw, `"message_id":"m1"`, `"message_id":"m1","message_id":"m2"`, 1),
	} {
		if _, _, err := DecodeReplay([]byte(bad), opts); err == nil {
			t.Fatalf("accepted invalid capture: %s", bad)
		}
	}
}

func TestReplayKeepsDuplicateAndReportsPartialAdmission(t *testing.T) {
	raw := `{"version":"local-dws-replay/1","id":"m1","scene":"scene","actor":"owner","text":"hello"}`
	var got []entry.Input
	count, err := Replay(strings.NewReader(raw+"\n"+raw+"\n{"), ReplayOptions{Owner: "owner", Scenes: []string{"scene"}}, func(in entry.Input) error { got = append(got, in); return nil })
	if count != 2 || len(got) != 2 || got[0].ID != got[1].ID || err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("count=%d got=%+v err=%v", count, got, err)
	}
}

func TestReplayRejectsUnknownVersionAndActor(t *testing.T) {
	opts := ReplayOptions{Owner: "owner", Scenes: []string{"scene"}}
	for _, raw := range []string{
		`{"version":"future/2","id":"m1","scene":"scene","actor":"owner","text":"x"}`,
		`{"version":"local-dws-replay/1","id":"m1","scene":"scene","actor":"other","text":"x"}`,
		`{"version":"local-dws-replay/1","id":"m1","scene":"scene","actor":"owner","text":"x","admin":true}`,
		`{"version":"local-dws-replay/1","id":"m1","scene":"scene","actor":"owner","text":"x"} {}`,
	} {
		if _, _, err := DecodeReplay([]byte(raw), opts); err == nil {
			t.Fatalf("accepted: %s", raw)
		}
	}
	if _, ok, err := DecodeReplay([]byte(`{"type":"subscription.ready","data":{"id":"s1"}}`), opts); err != nil || ok {
		t.Fatalf("notification ok=%v err=%v", ok, err)
	}
}

package dws_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/gateway/dws"
)

var bg = context.Background()

func normalizer() (*dws.Normalizer, *dws.RecentIDs) {
	own := dws.NewRecentIDs(8)
	own.Add("bot-msg-1")
	return &dws.Normalizer{
		Scenes: dws.HashSceneResolver{Namespace: "test", Tenant: "t1"},
		Self:   dws.Identity{IDs: []string{"bot"}, Names: []string{"Helper"}},
		Own:    own, Locale: "en",
	}, own
}

func groupMsg(id, text string) dws.Message {
	return dws.Message{ConversationID: "cidG", MessageID: id, SenderID: "alice", SenderName: "Alice", Text: text,
		AtMs: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC).UnixMilli(), Kind: dws.MessageText}
}

func TestNormalizeRoutesAddressingToTypes(t *testing.T) {
	n, _ := normalizer()
	dm := groupMsg("d1", "hello")
	dm.ConversationID, dm.Single = "cidD", true
	mention := groupMsg("g2", "please check")
	mention.MentionIDs = []string{"carol", "bot"}
	flagged := groupMsg("g3", "please check")
	flagged.MentionsSelf = true
	textual := groupMsg("g4", "@Helper can you look")
	replyByID := groupMsg("g5", "no, the other one")
	replyByID.ReplyToMessageID = "bot-msg-1"
	replyBySender := groupMsg("g6", "thanks")
	replyBySender.ReplyToMessageID, replyBySender.ReplyToSenderID = "x", "bot"
	replyOther := groupMsg("g7", "agreed")
	replyOther.ReplyToMessageID, replyOther.ReplyToSenderID = "y", "bob"
	cases := []struct {
		name    string
		msg     dws.Message
		typ     string
		replyTo string
	}{
		{"direct message", dm, contract.TypeMessageAddressed, ""},
		{"mention id", mention, contract.TypeMessageAddressed, ""},
		{"provider mention flag", flagged, contract.TypeMessageAddressed, ""},
		{"textual mention", textual, contract.TypeMessageAddressed, ""},
		{"reply to own message", replyByID, contract.TypeMessageAddressed, "bot-msg-1"},
		{"reply to employee sender", replyBySender, contract.TypeMessageAddressed, "x"},
		{"plain group message", groupMsg("g1", "lunch?"), contract.TypeMessageObserved, ""},
		{"reply to someone else", replyOther, contract.TypeMessageObserved, "y"},
	}
	cat := contract.StandardCatalog()
	for _, c := range cases {
		e, ok, err := n.Normalize(bg, c.msg)
		if err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", c.name, ok, err)
		}
		if e.Type != c.typ || e.Ref(contract.RefReplyTo) != c.replyTo || e.ID != c.msg.MessageID || e.Source != dws.DefaultSource {
			t.Errorf("%s: %+v", c.name, e)
		}
		if e.Actor != (contract.Actor{ID: "alice", Kind: contract.ActorHuman, DisplayName: "Alice"}) || e.Locale != "en" || !e.OccurredAt.Equal(time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("%s: actor/locale/time %+v", c.name, e)
		}
		if _, err := cat.Route(e); err != nil {
			t.Errorf("%s: catalog rejects normalized event: %v", c.name, err)
		}
	}
}

func TestNormalizeMintsSceneIDsNeverTheConversationID(t *testing.T) {
	n, _ := normalizer()
	a, _, _ := n.Normalize(bg, groupMsg("g1", "x"))
	b, _, _ := n.Normalize(bg, groupMsg("g2", "y"))
	if a.Scene.ID == "" || a.Scene.ID == "cidG" || a.Scene.ID != b.Scene.ID {
		t.Fatalf("scene ids: %q %q", a.Scene.ID, b.Scene.ID)
	}
	if a.Scene.Locator != "cidG" || a.Scene.Provider != dws.Provider || a.Scene.Kind != contract.SceneGroup || a.Scene.Tenant != "t1" {
		t.Fatalf("scene ref: %+v", a.Scene)
	}
	other := groupMsg("g3", "z")
	other.ConversationID = "cidH"
	c, _, _ := n.Normalize(bg, other)
	if c.Scene.ID == a.Scene.ID {
		t.Fatal("different conversations share a scene")
	}
	ns := *n
	ns.Scenes = dws.HashSceneResolver{Namespace: "elsewhere"}
	if d, _, _ := ns.Normalize(bg, groupMsg("g1", "x")); d.Scene.ID == a.Scene.ID {
		t.Fatal("namespace must change scene ids")
	}
	raw := *n
	raw.Scenes = resolverFunc(func(conv dws.Conversation) contract.SceneRef { return contract.SceneRef{ID: conv.ID} })
	if _, _, err := raw.Normalize(bg, groupMsg("g1", "x")); !errors.Is(err, dws.ErrRawSceneID) {
		t.Fatalf("raw conversation id as scene: %v", err)
	}
}

type resolverFunc func(dws.Conversation) contract.SceneRef

func (f resolverFunc) ResolveScene(_ context.Context, c dws.Conversation) (contract.SceneRef, error) {
	return f(c), nil
}

func TestNormalizeReactionsAndSkips(t *testing.T) {
	n, own := normalizer()
	react := dws.Message{ConversationID: "cidG", MessageID: "r1", SenderID: "alice", SenderName: "Alice",
		Kind: dws.MessageReaction, Reaction: "thumbsdown", TargetMessageID: "bot-msg-1", AtMs: 1}
	e, ok, err := n.Normalize(bg, react)
	if err != nil || !ok || e.Type != contract.TypeReactionAdded || e.Ref(contract.RefReplyTo) != "bot-msg-1" || e.Text != "thumbsdown" {
		t.Fatalf("negative reaction on own message: %+v ok=%v err=%v", e, ok, err)
	}
	if _, err := contract.StandardCatalog().Route(e); err != nil {
		t.Fatal(err)
	}
	positive := react
	positive.Reaction = "thumbsup"
	foreign := react
	foreign.TargetMessageID = "someone-else"
	self := groupMsg("s1", "my own words")
	self.SenderID = "bot"
	selfByName := groupMsg("s2", "my own words")
	selfByName.SenderID, selfByName.SenderName = "", "Helper"
	for name, m := range map[string]dws.Message{
		"positive reaction": positive, "reaction on other message": foreign,
		"own message": self, "own message by name": selfByName, "empty text": groupMsg("e1", "  "),
	} {
		if _, ok, err := n.Normalize(bg, m); ok || err != nil {
			t.Errorf("%s: ok=%v err=%v", name, ok, err)
		}
	}
	own.Add("bot-msg-2")
	foreign.TargetMessageID = "bot-msg-2"
	if _, ok, _ := n.Normalize(bg, foreign); !ok {
		t.Fatal("ids recorded later are recognised")
	}
	custom := *n
	custom.Negative = func(name, _ string) bool { return name == "thumbsup" }
	if _, ok, _ := custom.Normalize(bg, positive); !ok {
		t.Fatal("injected classifier wins")
	}

	noSender := groupMsg("n1", "hi")
	noSender.SenderID = ""
	if _, _, err := n.Normalize(bg, noSender); !errors.Is(err, dws.ErrNoSender) {
		t.Fatalf("no sender: %v", err)
	}
	noID := groupMsg("", "hi")
	if _, _, err := n.Normalize(bg, noID); !errors.Is(err, dws.ErrNoMessageID) {
		t.Fatalf("no id: %v", err)
	}
}

func TestRecentIDsIsBounded(t *testing.T) {
	r := dws.NewRecentIDs(2)
	r.Add("a")
	r.Add("b")
	r.Add("a")
	r.Add("c")
	if r.Has("a") || !r.Has("b") || !r.Has("c") || r.Has("") {
		t.Fatal("keeps the last two ids")
	}
	var nilSet *dws.RecentIDs
	nilSet.Add("x")
	if nilSet.Has("x") {
		t.Fatal("nil set is empty")
	}
}

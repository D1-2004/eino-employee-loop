package memstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
)

func fact(text string) *contract.Fact {
	return &contract.Fact{Tenant: "t1", Scene: "s1", Subject: "alice", Audience: "me", Kind: "fact", Text: text, Quote: text,
		Speaker: "alice", SpeakerRole: "user", EvidenceScope: contract.ScopeUserSaid}
}

func TestMemoryProvenanceDedupAndQuota(t *testing.T) {
	ctx := context.Background()
	m := memstore.NewMemory(memstore.WithLimits(contract.MemoryLimits{MaxFactBytes: 10, MaxActivePerBag: 2, MaxBytesPerBag: 1 << 10, CompactKeepNewer: 1}))
	if _, _, err := m.Put(ctx, fact("tea"), "I like coffee"); !errors.Is(err, contract.ErrBadQuote) {
		t.Fatalf("quote must be verbatim: %v", err)
	}
	a, created, err := m.Put(ctx, fact("coffee"), "I like coffee")
	if err != nil || !created || a.Status != memstore.FactActive {
		t.Fatalf("put: %+v %v", a, err)
	}
	again, created, err := m.Put(ctx, fact("  COFFEE "), "")
	if err != nil || created || again.ID != a.ID {
		t.Fatalf("normalized text dedups: %+v %v", again, err)
	}
	long, _, err := m.Put(ctx, fact("a very long fact text"), "")
	if err != nil || len(long.Text) > 10 {
		t.Fatalf("text is truncated to MaxFactBytes: %+v %v", long, err)
	}
	if _, _, err := m.Put(ctx, fact("third"), ""); !errors.Is(err, contract.ErrQuota) {
		t.Fatalf("bag is full: %v", err)
	}
	scene := fact("shared")
	scene.Audience = "scene"
	sf, _, err := m.Put(ctx, scene, "")
	if err != nil || sf.Subject != "" {
		t.Fatalf("scene facts have no subject and their own bag: %+v %v", sf, err)
	}
}

func TestMemoryArchiveAndCompactNeverDelete(t *testing.T) {
	ctx := context.Background()
	b := memstore.Messages()
	m := memstore.NewMemory(memstore.WithMessages(b, "zh-CN"),
		memstore.WithLimits(contract.MemoryLimits{MaxFactBytes: 600, MaxActivePerBag: 10, MaxBytesPerBag: 1 << 10, CompactKeepNewer: 1}))
	var ids []string
	for _, text := range []string{"one", "two", "three"} {
		f, _, err := m.Put(ctx, fact(text), "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	pinned := fact("pinned")
	pinned.Kind = "preference"
	if _, _, err := m.Put(ctx, pinned, ""); err != nil {
		t.Fatal(err)
	}
	n, digestID, err := m.Compact(ctx, "t1", "s1", "alice", "me")
	if err != nil || n != 2 || digestID == "" {
		t.Fatalf("compact: n=%d id=%q err=%v", n, digestID, err)
	}
	digest, _ := m.GetFact(ctx, "t1", digestID)
	if digest.Kind != memstore.KindDigest || digest.EvidenceScope != contract.ScopeHostDigest ||
		!strings.Contains(digest.Text, "["+ids[0]+"] one") || !strings.Contains(digest.Text, "["+ids[1]+"] two") ||
		!strings.HasPrefix(digest.Text, strings.SplitN(b.Text("zh-CN", "memstore.memory.digest", "count", "2"), "\n", 2)[0]) {
		t.Fatalf("digest lists originals verbatim in the configured locale: %+v", digest)
	}
	for _, id := range ids[:2] {
		f, err := m.GetFact(ctx, "t1", id)
		if err != nil || f.Status != memstore.FactArchived || f.ArchivedBy != digestID {
			t.Fatalf("compacted originals stay readable: %+v %v", f, err)
		}
	}
	active, _ := m.Recent(ctx, "t1", "s1", "alice", "me", 10)
	if len(active) != 3 { // digest, pinned, newest
		t.Fatalf("active after compaction: %d", len(active))
	}
	if n, _ := m.Archive(ctx, "t1", []string{ids[2], ids[2]}, "forget:alice"); n != 1 {
		t.Fatalf("archive moves a fact once: %d", n)
	}
	if n, _ := m.Archive(ctx, "other", []string{digestID}, "x"); n != 0 {
		t.Fatal("archive is tenant fenced")
	}
	if _, err := m.GetFact(ctx, "other", digestID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("get is tenant fenced: %v", err)
	}
	hits, _ := m.Search(ctx, "t1", "s1", "alice", "me", "PINNED", 5)
	if len(hits) != 1 || hits[0].Text != "pinned" {
		t.Fatalf("search is a case-insensitive substring: %+v", hits)
	}
}

func TestConversationRecentBeforeSeq(t *testing.T) {
	ctx := context.Background()
	c := memstore.NewConversation()
	for _, text := range []string{"a", "b", "c", "d"} {
		if _, err := c.Append(ctx, "t1", "s1", contract.Msg{Role: "user", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Append(ctx, "t1", "s2", contract.Msg{Role: "user", Text: "other"}); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Recent(ctx, "t1", "s1", 4, 2)
	if len(got) != 2 || got[0].Text != "b" || got[1].Text != "c" || got[0].Seq != 2 {
		t.Fatalf("newest two before seq 4, oldest first: %+v", got)
	}
	all, _ := c.Recent(ctx, "t1", "s1", 0, 10)
	if len(all) != 4 {
		t.Fatalf("scene logs are separate: %+v", all)
	}
}

func TestConversationAppendIsIdempotentPerSourceRefAndKind(t *testing.T) {
	ctx := context.Background()
	c := memstore.NewConversation()
	first, err := c.Append(ctx, "t1", "s1", contract.Msg{Role: "user", Text: "hello", Kind: "msg", SourceRef: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Append(ctx, "t1", "s1", contract.Msg{Role: "user", Text: "hello", Kind: "msg", SourceRef: "m1"})
	if err != nil || again.Seq != first.Seq {
		t.Fatalf("a repeat returns the stored message: %+v %v", again, err)
	}
	if _, err := c.Append(ctx, "t1", "s1", contract.Msg{Role: "assistant", Text: "hi", Kind: "reply", SourceRef: "m1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Append(ctx, "t1", "s1", contract.Msg{Role: "host", Text: "note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Append(ctx, "t1", "s1", contract.Msg{Role: "host", Text: "note"}); err != nil {
		t.Fatal(err)
	}
	if log, _ := c.Recent(ctx, "t1", "s1", 0, 0); len(log) != 4 {
		t.Fatalf("other kinds and entries without a source ref are appended: %+v", log)
	}
}

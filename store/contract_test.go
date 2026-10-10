package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type backend struct {
	name string
	ts   TaskStore
	ms   MemoryStore
	kv   KV
}

func backends(t *testing.T) []backend {
	var bs []backend
	sq, err := OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	bs = append(bs, backend{"sqlite+memkv", sq, sq, NewMemKV()})
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		if dsn := os.Getenv("PG_DSN"); dsn != "" {
			pg, err := OpenPostgres(dsn)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = pg.db.Exec(`TRUNCATE tasks, task_events, memory_facts`)
			bs = append(bs, backend{"postgres+redis", pg, pg, NewRedisKV(addr, fmt.Sprintf("t%d:", time.Now().UnixNano()))})
		}
	}
	return bs
}

func TestContract(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			// --- tasks: idempotent create, CAS transition, find
			t1 := &Task{ID: "tsk_1", Tenant: "T", Scene: "S", Goal: "查杭州今天天气", IdemKey: "k1"}
			c, created, err := b.ts.Create(ctx, t1, time.Minute)
			if err != nil || !created || c.ID != "tsk_1" {
				t.Fatalf("create %v %v %v", c, created, err)
			}
			c2, created2, err := b.ts.Create(ctx, &Task{ID: "tsk_2", Tenant: "T", Scene: "S", Goal: "查杭州今天天气", IdemKey: "k1"}, time.Minute)
			if err != nil || created2 || c2.ID != "tsk_1" {
				t.Fatalf("dup must return tsk_1: %v %v %v", c2, created2, err)
			}
			if _, cr, _ := b.ts.Create(ctx, &Task{ID: "tsk_3", Tenant: "T2", Scene: "S", Goal: "x", IdemKey: "k1"}, time.Minute); !cr {
				t.Fatal("tenant isolation broken")
			}
			ok, _ := b.ts.Transition(ctx, "T", "tsk_1", []string{StQueued}, StRunning, "")
			ok2, _ := b.ts.Transition(ctx, "T", "tsk_1", []string{StQueued}, StRunning, "")
			if !ok || ok2 {
				t.Fatalf("CAS %v %v", ok, ok2)
			}
			ok, _ = b.ts.Transition(ctx, "T", "tsk_1", []string{StRunning}, StDone, "晴 25C")
			g, _ := b.ts.Get(ctx, "T", "tsk_1")
			if !ok || g.Status != StDone || g.Result != "晴 25C" || g.DoneMs == 0 || g.Attempts != 1 {
				t.Fatalf("done: %+v", g)
			}
			// inside window a done duplicate is returned; after terminal+window it would recreate (tested with 0 window)
			if _, cr, _ := b.ts.Create(ctx, &Task{ID: "tsk_4", Tenant: "T", Scene: "S", Goal: "查杭州今天天气", IdemKey: "k1"}, time.Minute); cr {
				t.Fatal("done-in-window should dedup")
			}
			if _, cr, _ := b.ts.Create(ctx, &Task{ID: "tsk_5", Tenant: "T", Scene: "S", Goal: "查杭州今天天气", IdemKey: "k1"}, 0); !cr {
				t.Fatal("zero window should recreate")
			}
			fs, _ := b.ts.Find(ctx, "T", "S", "天气", 10)
			if len(fs) != 2 {
				t.Fatalf("find %d", len(fs))
			}
			// concurrent dispatch race: exactly one winner
			var wg sync.WaitGroup
			var mu sync.Mutex
			wins := 0
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, cr, err := b.ts.Create(ctx, &Task{ID: fmt.Sprintf("race_%d", i), Tenant: "T", Scene: "R", Goal: "g", IdemKey: "race"}, time.Minute)
					if err != nil {
						t.Error(err)
					}
					if cr {
						mu.Lock()
						wins++
						mu.Unlock()
					}
				}(i)
			}
			wg.Wait()
			if wins != 1 {
				t.Fatalf("race winners=%d", wins)
			}
			// --- memory
			f := &Fact{Tenant: "T", Scene: "S", Subject: "u1", Audience: "me", Kind: "preference", Text: "回复用 markdown", Quote: "用markdown", Speaker: "冬翔", SpeakerRole: "user", EvidenceScope: ScopeUserSaid}
			if _, _, err := b.ms.Put(ctx, f, "和我对话最好用markdown格式"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := b.ms.Put(ctx, &Fact{Tenant: "T", Scene: "S", Subject: "u1", Audience: "me", Kind: "fact", Text: "bad", Quote: "不存在的话"}, "原话"); err != ErrBadQuote {
				t.Fatalf("quote check: %v", err)
			}
			_, cr, _ := b.ms.Put(ctx, &Fact{Tenant: "T", Scene: "S", Subject: "u1", Audience: "me", Kind: "preference", Text: "回复用  markdown"}, "")
			if cr {
				t.Fatal("dup fact must dedup (whitespace-normalised)")
			}
			b.ms.Put(ctx, &Fact{Tenant: "T", Scene: "S", Subject: "u1", Audience: "me", Kind: "fact", Text: "龙游的天气任务每天早上 9 点"}, "")
			hits, _ := b.ms.Search(ctx, "T", "S", "u1", "me", "龙游", 5)
			if len(hits) != 1 {
				t.Fatalf("search hits=%d", len(hits))
			}
			if h, _ := b.ms.Search(ctx, "T", "S", "u2", "me", "龙游", 5); len(h) != 0 {
				t.Fatal("private memory leaked across subjects")
			}
			// quota + archive compaction
			if sq, ok := b.ms.(*SQLStore); ok {
				sq.SetLimits(MemoryLimits{MaxFactBytes: 600, MaxActivePerBag: 12, MaxBytesPerBag: 1 << 20, CompactKeepNewer: 4})
				defer sq.SetLimits(DefaultLimits)
			}
			var qerr error
			for i := 0; i < 20 && qerr == nil; i++ {
				_, _, qerr = b.ms.Put(ctx, &Fact{Tenant: "T", Scene: "S", Subject: "u1", Audience: "me", Kind: "fact", Text: fmt.Sprintf("事实 %d", i)}, "")
			}
			if qerr != ErrQuota {
				t.Fatalf("expected quota, got %v", qerr)
			}
			n, did, err := b.ms.Compact(ctx, "T", "S", "u1", "me")
			if err != nil || n == 0 || did == "" {
				t.Fatalf("compact %d %q %v", n, did, err)
			}
			d, _ := b.ms.GetFact(ctx, "T", did)
			if d.Kind != "digest" || d.EvidenceScope != ScopeHostDigest {
				t.Fatalf("digest %+v", d)
			}
			// pinned preference survives; originals still retrievable by id (archive, not delete)
			act, _ := b.ms.Recent(ctx, "T", "S", "u1", "me", 100)
			pin := false
			for _, a := range act {
				if a.Kind == "preference" {
					pin = true
				}
			}
			if !pin {
				t.Fatal("pinned lost")
			}
			// --- kv
			if ok, _ := b.kv.SetNX(ctx, "dedup:a", "1", time.Minute); !ok {
				t.Fatal("setnx1")
			}
			if ok, _ := b.kv.SetNX(ctx, "dedup:a", "1", time.Minute); ok {
				t.Fatal("setnx2")
			}
			b.kv.Set(ctx, "ttl", "v", 80*time.Millisecond)
			time.Sleep(150 * time.Millisecond)
			if _, ok, _ := b.kv.Get(ctx, "ttl"); ok {
				t.Fatal("ttl")
			}
			tok, lk, _ := b.kv.Lock(ctx, "scene:S", time.Minute)
			_, lk2, _ := b.kv.Lock(ctx, "scene:S", time.Minute)
			if !lk || lk2 {
				t.Fatal("lock")
			}
			if u, _ := b.kv.Unlock(ctx, "scene:S", "wrong"); u {
				t.Fatal("unlock wrong token")
			}
			if u, _ := b.kv.Unlock(ctx, "scene:S", tok); !u {
				t.Fatal("unlock")
			}
			n1, _ := b.kv.Incr(ctx, "cnt", time.Minute)
			n2, _ := b.kv.Incr(ctx, "cnt", time.Minute)
			if n1 != 1 || n2 != 2 {
				t.Fatal("incr")
			}
		})
	}
}

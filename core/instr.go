package core

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Stats collects per-operation latencies of the storage layer (microseconds).
type Stats struct {
	mu sync.Mutex
	m  map[string][]float64
}

func NewStats() *Stats { return &Stats{m: map[string][]float64{}} }
func (s *Stats) rec(op string, t0 time.Time) {
	d := float64(time.Since(t0).Nanoseconds()) / 1000
	s.mu.Lock()
	s.m[op] = append(s.m[op], d)
	s.mu.Unlock()
}
func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64{}, v...)
	sort.Float64s(c)
	i := int(float64(len(c)-1) * p)
	return c[i]
}
func (s *Stats) Summary() map[string]map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[string]float64{}
	for k, v := range s.m {
		sum := 0.0
		for _, x := range v {
			sum += x
		}
		out[k] = map[string]float64{"n": float64(len(v)), "p50_us": pct(v, .5), "p95_us": pct(v, .95), "max_us": pct(v, 1), "mean_us": sum / float64(len(v))}
	}
	return out
}

type timedTasks struct {
	TaskStore
	s *Stats
}

func (t timedTasks) Create(ctx context.Context, x *Task, w time.Duration) (*Task, bool, error) {
	defer t.s.rec("task.create", time.Now())
	return t.TaskStore.Create(ctx, x, w)
}
func (t timedTasks) Get(ctx context.Context, a, b string) (*Task, error) {
	defer t.s.rec("task.get", time.Now())
	return t.TaskStore.Get(ctx, a, b)
}
func (t timedTasks) Find(ctx context.Context, a, b, c string, n int) ([]*Task, error) {
	defer t.s.rec("task.find", time.Now())
	return t.TaskStore.Find(ctx, a, b, c, n)
}
func (t timedTasks) Transition(ctx context.Context, a, b string, f []string, to, r string) (bool, error) {
	defer t.s.rec("task.transition", time.Now())
	return t.TaskStore.Transition(ctx, a, b, f, to, r)
}

type timedMem struct {
	MemoryStore
	s *Stats
}

func (t timedMem) Put(ctx context.Context, f *Fact, o string) (*Fact, bool, error) {
	defer t.s.rec("mem.put", time.Now())
	return t.MemoryStore.Put(ctx, f, o)
}
func (t timedMem) Search(ctx context.Context, a, b, c, d, q string, n int) ([]*Fact, error) {
	defer t.s.rec("mem.search", time.Now())
	return t.MemoryStore.Search(ctx, a, b, c, d, q, n)
}
func (t timedMem) Recent(ctx context.Context, a, b, c, d string, n int) ([]*Fact, error) {
	defer t.s.rec("mem.recent", time.Now())
	return t.MemoryStore.Recent(ctx, a, b, c, d, n)
}

type timedKV struct {
	KV
	s *Stats
}

func (t timedKV) SetNX(ctx context.Context, k, v string, ttl time.Duration) (bool, error) {
	defer t.s.rec("kv.setnx", time.Now())
	return t.KV.SetNX(ctx, k, v, ttl)
}
func (t timedKV) Get(ctx context.Context, k string) (string, bool, error) {
	defer t.s.rec("kv.get", time.Now())
	return t.KV.Get(ctx, k)
}

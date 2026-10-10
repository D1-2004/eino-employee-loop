package memstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
)

// Memory fact statuses and the kinds that survive compaction.
const (
	FactActive   = "active"
	FactArchived = "archived"
	KindDigest   = "digest"
)

// pinnedKinds are never folded into a digest by Compact.
var pinnedKinds = map[string]bool{"preference": true, "decision": true, KindDigest: true}

// Memory is an in-memory contract.MemoryStore. It never deletes or rewrites
// a fact: forget and compaction archive records, and the compaction digest
// lists the archived originals verbatim (no model summarisation).
type Memory struct {
	mu       sync.Mutex
	limits   contract.MemoryLimits
	messages *i18n.Bundle
	locale   string
	now      func() time.Time
	facts    map[string]*contract.Fact
	order    []string // insertion order of fact ids
	seq      int64
}

// MemoryOption configures a Memory.
type MemoryOption func(*Memory)

// WithLimits replaces contract.DefaultMemoryLimits.
func WithLimits(l contract.MemoryLimits) MemoryOption { return func(m *Memory) { m.limits = l } }

// WithMessages sets the bundle and locale that render the digest text.
func WithMessages(b *i18n.Bundle, locale string) MemoryOption {
	return func(m *Memory) { m.messages, m.locale = b, locale }
}

// WithClock injects the time source.
func WithClock(now func() time.Time) MemoryOption { return func(m *Memory) { m.now = now } }

// NewMemory returns an empty memory store.
func NewMemory(opts ...MemoryOption) *Memory {
	m := &Memory{limits: contract.DefaultMemoryLimits, locale: i18n.Fallback, now: time.Now, facts: map[string]*contract.Fact{}}
	for _, o := range opts {
		o(m)
	}
	if m.messages == nil {
		m.messages = Messages()
	}
	return m
}

var _ contract.MemoryStore = (*Memory)(nil)

func normHash(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func truncBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	r := []rune(s)
	for len(string(r)) > n {
		r = r[:len(r)-1]
	}
	return string(r)
}

func inBag(f *contract.Fact, tenant, scene, subject, audience string) bool {
	return f.Tenant == tenant && f.Scene == scene && f.Audience == audience && (subject == "*" || f.Subject == subject)
}

func copyFact(f *contract.Fact) *contract.Fact {
	cp := *f
	return &cp
}

// Put implements contract.MemoryStore.
func (m *Memory) Put(_ context.Context, f *contract.Fact, originalMsg string) (*contract.Fact, bool, error) {
	if f == nil {
		return nil, false, fmt.Errorf("memstore: nil fact")
	}
	in := copyFact(f)
	if strings.TrimSpace(in.Quote) == "" {
		in.Quote = in.Text
	}
	if originalMsg != "" && !strings.Contains(originalMsg, strings.TrimSpace(in.Quote)) {
		return nil, false, contract.ErrBadQuote
	}
	if strings.TrimSpace(in.Text) == "" {
		return nil, false, fmt.Errorf("memstore: empty fact text")
	}
	in.Text = truncBytes(in.Text, m.limits.MaxFactBytes)
	if in.Audience == "scene" {
		in.Subject = ""
	}
	in.TextHash = normHash(in.Text)
	m.mu.Lock()
	defer m.mu.Unlock()
	n, bytes := 0, 0
	for _, id := range m.order {
		cur := m.facts[id]
		if cur.Status != FactActive || !inBag(cur, in.Tenant, in.Scene, in.Subject, in.Audience) {
			continue
		}
		if cur.TextHash == in.TextHash {
			return copyFact(cur), false, nil
		}
		n++
		bytes += len(cur.Text)
	}
	if n >= m.limits.MaxActivePerBag || bytes+len(in.Text) > m.limits.MaxBytesPerBag {
		return nil, false, contract.ErrQuota
	}
	m.insertLocked(in)
	return copyFact(in), true, nil
}

func (m *Memory) insertLocked(f *contract.Fact) {
	m.seq++
	if f.ID == "" {
		f.ID = fmt.Sprintf("mem-%06d", m.seq)
	}
	f.Status, f.ArchivedBy = FactActive, ""
	f.CreatedMs = m.now().UnixMilli()
	m.facts[f.ID] = f
	m.order = append(m.order, f.ID)
}

// bag returns active facts of a bag, newest first.
func (m *Memory) bag(tenant, scene, subject, audience string) []*contract.Fact {
	var out []*contract.Fact
	for i := len(m.order) - 1; i >= 0; i-- {
		f := m.facts[m.order[i]]
		if f.Status == FactActive && inBag(f, tenant, scene, subject, audience) {
			out = append(out, f)
		}
	}
	return out
}

func limitCopy(fs []*contract.Fact, limit int) []*contract.Fact {
	if limit > 0 && len(fs) > limit {
		fs = fs[:limit]
	}
	out := make([]*contract.Fact, 0, len(fs))
	for _, f := range fs {
		out = append(out, copyFact(f))
	}
	return out
}

// Search implements contract.MemoryStore: a case-insensitive substring of
// the fact text or quote, newest first. An empty query returns Recent.
func (m *Memory) Search(_ context.Context, tenant, scene, subject, audience, query string, limit int) ([]*contract.Fact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(query))
	var hits []*contract.Fact
	for _, f := range m.bag(tenant, scene, subject, audience) {
		if q == "" || strings.Contains(strings.ToLower(f.Text), q) || strings.Contains(strings.ToLower(f.Quote), q) {
			hits = append(hits, f)
		}
	}
	return limitCopy(hits, limit), nil
}

// Recent implements contract.MemoryStore. Subject "*" matches any subject.
func (m *Memory) Recent(_ context.Context, tenant, scene, subject, audience string, limit int) ([]*contract.Fact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return limitCopy(m.bag(tenant, scene, subject, audience), limit), nil
}

// Archive implements contract.MemoryStore. It returns the number of facts
// that moved from active to archived.
func (m *Memory) Archive(_ context.Context, tenant string, ids []string, by string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, id := range ids {
		f, ok := m.facts[id]
		if !ok || f.Tenant != tenant || f.Status != FactActive {
			continue
		}
		f.Status, f.ArchivedBy = FactArchived, by
		n++
	}
	return n, nil
}

// Compact implements contract.MemoryStore. It keeps the newest
// CompactKeepNewer unpinned facts, archives the older ones and writes one
// digest fact listing them by id. The digest bypasses the quota because it
// replaces many facts.
func (m *Memory) Compact(_ context.Context, tenant, scene, subject, audience string) (int, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if audience == "scene" {
		subject = ""
	}
	newest := m.bag(tenant, scene, subject, audience)
	var cand []*contract.Fact
	for i := len(newest) - 1; i >= 0; i-- { // oldest first
		if !pinnedKinds[newest[i].Kind] {
			cand = append(cand, newest[i])
		}
	}
	keep := m.limits.CompactKeepNewer
	if keep < 0 {
		keep = 0
	}
	if len(cand) <= keep {
		return 0, "", nil
	}
	old := cand[:len(cand)-keep]
	lines := make([]string, 0, len(old))
	for _, f := range old {
		lines = append(lines, "["+f.ID+"] "+truncBytes(f.Text, 80))
	}
	text := m.messages.Text(m.locale, "memstore.memory.digest", "count", strconv.Itoa(len(old)), "lines", strings.Join(lines, "\n"))
	d := &contract.Fact{
		Tenant: tenant, Scene: scene, Subject: subject, Audience: audience, Kind: KindDigest,
		Speaker: "host", SpeakerRole: "host", EvidenceScope: contract.ScopeHostDigest, Text: text, Quote: text,
	}
	d.TextHash = normHash(d.Text)
	m.insertLocked(d)
	for _, f := range old {
		f.Status, f.ArchivedBy = FactArchived, d.ID
	}
	return len(old), d.ID, nil
}

// CountActive implements contract.MemoryStore.
func (m *Memory) CountActive(_ context.Context, tenant, scene, subject, audience string) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, bytes := 0, 0
	for _, f := range m.bag(tenant, scene, subject, audience) {
		n++
		bytes += len(f.Text)
	}
	return n, bytes, nil
}

// GetFact implements contract.MemoryStore. Archived facts stay readable.
func (m *Memory) GetFact(_ context.Context, tenant, id string) (*contract.Fact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.facts[id]
	if !ok || f.Tenant != tenant {
		return nil, contract.ErrNotFound
	}
	return copyFact(f), nil
}

// Close implements contract.MemoryStore.
func (m *Memory) Close() error { return nil }

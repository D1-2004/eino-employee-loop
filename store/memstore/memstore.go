// Package memstore is an in-memory reference implementation of the
// EventLog, SessionQueue, Journal and Outbox ports. It is concurrency-safe
// and suitable for tests, demos and single-process assemblies; durable
// deployments implement the same ports on PostgreSQL (see schema/).
package memstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// Store implements every port of this package on one mutex, so an admission
// and the cancellations it implies, and a completion and its deliveries,
// are atomic.
type Store struct {
	mu         sync.Mutex
	receipts   map[string]contract.EventReceipt // employee|source|event id
	receiptIDs map[string]string                // receipt id -> key
	windows    map[string]*contract.Window
	order      map[string]int64 // window id -> creation sequence
	results    map[string]contract.WindowResult
	open       map[string]string // employee|session -> open window id
	enqueued   map[string]string // employee|receipt id -> window id
	cancelled  map[string]int    // employee|receipt id -> windows cancelled
	journal    map[string]*journal
	outbox     map[string]contract.Delivery
	seq        int64
}

type journal struct {
	turns []contract.ModelTurn
	tools []contract.ToolRecord
}

// New returns an empty store.
func New() *Store {
	return &Store{
		receipts:   map[string]contract.EventReceipt{},
		receiptIDs: map[string]string{},
		windows:    map[string]*contract.Window{},
		order:      map[string]int64{},
		results:    map[string]contract.WindowResult{},
		open:       map[string]string{},
		enqueued:   map[string]string{},
		cancelled:  map[string]int{},
		journal:    map[string]*journal{},
		outbox:     map[string]contract.Delivery{},
	}
}

var (
	_ contract.EventLog     = (*Store)(nil)
	_ contract.SessionQueue = (*Store)(nil)
	_ contract.Journal      = (*Store)(nil)
	_ contract.Outbox       = (*Store)(nil)
)

func (s *Store) id(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%06d", prefix, s.seq)
}

func token() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------- EventLog

// Admit implements contract.EventLog.
func (s *Store) Admit(_ context.Context, employee string, adm contract.Admission, fingerprint string) (contract.EventReceipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := employee + "|" + adm.Event.Source + "|" + adm.Event.ID
	if r, ok := s.receipts[key]; ok {
		if r.Fingerprint != fingerprint {
			return contract.EventReceipt{}, false, contract.ErrConflict
		}
		return r, false, nil
	}
	r := contract.EventReceipt{
		ID: s.id("rcpt"), Employee: employee, Source: adm.Event.Source, EventID: adm.Event.ID, Type: adm.Event.Type,
		Class: adm.Spec.Class, Mode: adm.Spec.Mode, Lane: adm.Spec.Lane, Session: adm.Session,
		Fingerprint: fingerprint, AdmittedAt: time.Now(),
	}
	s.receipts[key] = r
	s.receiptIDs[r.ID] = key
	return r, true, nil
}

// MarkApplied implements contract.EventLog.
func (s *Store) MarkApplied(_ context.Context, employee, receiptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.receiptIDs[receiptID]
	if !ok || s.receipts[key].Employee != employee {
		return contract.ErrNotFound
	}
	r := s.receipts[key]
	r.Applied = true
	s.receipts[key] = r
	return nil
}

// Receipt implements contract.EventLog.
func (s *Store) Receipt(_ context.Context, employee, receiptID string) (contract.EventReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.receiptIDs[receiptID]
	if !ok || s.receipts[key].Employee != employee {
		return contract.EventReceipt{}, contract.ErrNotFound
	}
	return s.receipts[key], nil
}

// ------------------------------------------------------------ SessionQueue

func openKey(employee string, ref contract.SessionRef) string { return employee + "|" + ref.String() }

// Enqueue implements contract.SessionQueue.
func (s *Store) Enqueue(_ context.Context, employee string, adm contract.Admission, receiptID string, now time.Time) (contract.Window, error) {
	if adm.Session == nil {
		return contract.Window{}, fmt.Errorf("memstore: %s is a host-lane event", adm.Event.Type)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rkey := employee + "|" + receiptID
	if id, ok := s.enqueued[rkey]; ok {
		// Idempotent per receipt: the item and its cancellations committed.
		return *s.windows[id], nil
	}
	for _, c := range adm.Cancel {
		s.cancelLocked(employee, c)
	}
	item := contract.WindowItem{ReceiptID: receiptID, Event: adm.Event}
	key := openKey(employee, *adm.Session)
	co := adm.Spec.Coalesce
	if id, ok := s.open[key]; ok {
		w := s.windows[id]
		switch {
		case w == nil || w.State != contract.WindowOpen:
			delete(s.open, key)
		case co != nil && len(w.Items) < co.MaxItems && now.Before(w.OpenedAt.Add(time.Duration(co.MaxWaitMs)*time.Millisecond)):
			w.Items = append(w.Items, item)
			w.DueAt = minTime(now.Add(time.Duration(co.QuietMs)*time.Millisecond), w.OpenedAt.Add(time.Duration(co.MaxWaitMs)*time.Millisecond))
			if len(w.Items) >= co.MaxItems {
				w.State = contract.WindowReady
				delete(s.open, key)
			}
			s.enqueued[rkey] = w.ID
			return *w, nil
		default:
			// The item cannot join: seal the open window so it runs before
			// the new one instead of staying open forever.
			w.State, w.DueAt = contract.WindowReady, minTime(w.DueAt, now)
			delete(s.open, key)
		}
	}
	w := &contract.Window{
		ID: s.id("win"), Employee: employee, Session: *adm.Session, Lane: adm.Spec.Lane,
		State: contract.WindowReady, Preemptible: adm.Spec.Preemptible,
		Items: []contract.WindowItem{item}, OpenedAt: now, DueAt: now,
	}
	if co != nil && co.MaxItems > 1 {
		w.State = contract.WindowOpen
		w.DueAt = now.Add(time.Duration(co.QuietMs) * time.Millisecond)
		s.open[key] = w.ID
	}
	s.windows[w.ID] = w
	s.order[w.ID] = s.seq
	s.enqueued[rkey] = w.ID
	return *w, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Claim implements contract.SessionQueue.
func (s *Store) Claim(_ context.Context, req contract.ClaimRequest) (*contract.Window, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	// Seal every due open window (tracked or not) and release expired leases.
	for key, id := range s.open {
		if w := s.windows[id]; w == nil || w.State != contract.WindowOpen {
			delete(s.open, key)
		}
	}
	runningSession := map[string]bool{}
	runningScene := map[string]bool{}
	runningForeground := map[string]bool{}
	pendingForeground := map[string]bool{}
	// head is the oldest unfinished (open, ready or running) window of each
	// session: only it may be claimed, so a window waiting for its retry
	// keeps the later windows of its session behind it.
	head := map[string]int64{}
	for _, w := range s.windows {
		if w.State == contract.WindowOpen && !now.Before(w.DueAt) {
			w.State = contract.WindowReady
			if key := openKey(w.Employee, w.Session); s.open[key] == w.ID {
				delete(s.open, key)
			}
		}
		if w.State == contract.WindowRunning && w.Lease != nil && now.After(w.Lease.Until) {
			w.State = contract.WindowReady
			w.Lease = nil
		}
		scene, session := w.Employee+"|"+w.Session.Scene, openKey(w.Employee, w.Session)
		switch w.State {
		case contract.WindowRunning:
			runningSession[session] = true
			runningScene[scene] = true
			if w.Lane == contract.LaneForeground {
				runningForeground[scene] = true
			}
		case contract.WindowOpen, contract.WindowReady:
			if w.Lane == contract.LaneForeground {
				pendingForeground[scene] = true
			}
		default:
			continue
		}
		if o, ok := head[session]; !ok || s.order[w.ID] < o {
			head[session] = s.order[w.ID]
		}
	}
	var candidates []*contract.Window
	for _, w := range s.windows {
		if w.State != contract.WindowReady || now.Before(w.DueAt) {
			continue
		}
		if len(req.Lanes) > 0 && !containsLane(req.Lanes, w.Lane) {
			continue
		}
		if len(req.Sessions) > 0 && !containsSession(req.Sessions, w.Session.Kind) {
			continue
		}
		scene, session := w.Employee+"|"+w.Session.Scene, openKey(w.Employee, w.Session)
		if runningSession[session] || head[session] != s.order[w.ID] || (req.SerializeScene && runningScene[scene]) {
			continue
		}
		if w.Lane != contract.LaneForeground && pendingForeground[scene] {
			continue
		}
		// A preemptible window never runs beside a foreground wake of its
		// scene: it would answer the same person in parallel.
		if w.Preemptible && runningForeground[scene] {
			continue
		}
		candidates = append(candidates, w)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if (a.Lane == contract.LaneForeground) != (b.Lane == contract.LaneForeground) {
			return a.Lane == contract.LaneForeground
		}
		if !a.DueAt.Equal(b.DueAt) {
			return a.DueAt.Before(b.DueAt)
		}
		return s.order[a.ID] < s.order[b.ID]
	})
	w := candidates[0]
	lease := req.LeaseFor
	if lease <= 0 {
		lease = 90 * time.Second
	}
	w.State = contract.WindowRunning
	w.Attempts++
	// The generation increases with every claim, fencing the previous owner.
	w.Lease = &contract.Lease{WindowID: w.ID, Token: token(), Generation: int64(w.Attempts), Until: now.Add(lease)}
	cp := *w
	return &cp, nil
}

func (s *Store) leased(lease contract.Lease) (*contract.Window, error) {
	w := s.windows[lease.WindowID]
	if w == nil || w.State != contract.WindowRunning || w.Lease == nil || w.Lease.Token != lease.Token || w.Lease.Generation != lease.Generation {
		return nil, contract.ErrLease
	}
	return w, nil
}

// Complete implements contract.SessionQueue.
func (s *Store) Complete(_ context.Context, lease contract.Lease, result contract.WindowResult, deliveries []contract.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.leased(lease)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		if cur, ok := s.outbox[d.ID]; ok && !sameDelivery(cur, d) {
			return fmt.Errorf("%w: delivery %s", contract.ErrConflict, d.ID)
		}
	}
	w.State = contract.WindowDone
	w.Lease = nil
	s.results[w.ID] = result
	for _, d := range deliveries {
		s.postLocked(d)
	}
	return nil
}

// Retry implements contract.SessionQueue.
func (s *Store) Retry(_ context.Context, lease contract.Lease, reason string, notBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.leased(lease)
	if err != nil {
		return err
	}
	w.State, w.Reason, w.DueAt, w.Lease = contract.WindowReady, reason, notBefore, nil
	return nil
}

// Hold implements contract.SessionQueue.
func (s *Store) Hold(_ context.Context, lease contract.Lease, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.leased(lease)
	if err != nil {
		return err
	}
	w.State, w.Reason, w.Lease = contract.WindowHeld, reason, nil
	return nil
}

// Cancel implements contract.SessionQueue.
func (s *Store) Cancel(_ context.Context, employee, receiptID string, cancels []contract.SessionCancel) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rkey := employee + "|" + receiptID
	if n, ok := s.cancelled[rkey]; ok {
		return n, nil
	}
	n := 0
	for _, c := range cancels {
		n += s.cancelLocked(employee, c)
	}
	s.cancelled[rkey] = n
	return n, nil
}

// cancelLocked cancels unsubmitted windows: open, ready and running. A
// running window loses its lease, so its late Complete fails and its reply
// is never delivered.
func (s *Store) cancelLocked(employee string, c contract.SessionCancel) int {
	n := 0
	for _, w := range s.windows {
		if w.Employee != employee || w.Session.Kind != c.Kind {
			continue
		}
		if c.Scene != "" && w.Session.Scene != c.Scene {
			continue
		}
		if c.Key != "" && w.Session.Key != c.Key {
			continue
		}
		if c.OnlyPreemp && !w.Preemptible {
			continue
		}
		switch w.State {
		case contract.WindowOpen, contract.WindowReady, contract.WindowRunning:
			w.State, w.Reason, w.Lease = contract.WindowCancelled, c.Reason, nil
			delete(s.open, openKey(employee, w.Session))
			n++
		}
	}
	return n
}

// Window implements contract.SessionQueue.
func (s *Store) Window(_ context.Context, employee, windowID string) (contract.Window, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windows[windowID]
	if w == nil || w.Employee != employee {
		return contract.Window{}, contract.ErrNotFound
	}
	return *w, nil
}

// Result returns the recorded result of a completed window.
func (s *Store) Result(windowID string) (contract.WindowResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.results[windowID]
	return r, ok
}

func containsLane(xs []contract.Lane, x contract.Lane) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsSession(xs []contract.SessionKind, x contract.SessionKind) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------- Journal

func (s *Store) journalFor(lease contract.Lease) (*journal, error) {
	if _, err := s.leased(lease); err != nil {
		return nil, err
	}
	j := s.journal[lease.WindowID]
	if j == nil {
		j = &journal{}
		s.journal[lease.WindowID] = j
	}
	return j, nil
}

// ModelTurns implements contract.Journal.
func (s *Store) ModelTurns(_ context.Context, lease contract.Lease) ([]contract.ModelTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.journalFor(lease)
	if err != nil {
		return nil, err
	}
	return append([]contract.ModelTurn(nil), j.turns...), nil
}

// SaveModelTurn implements contract.Journal.
func (s *Store) SaveModelTurn(_ context.Context, lease contract.Lease, turn contract.ModelTurn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.journalFor(lease)
	if err != nil {
		return err
	}
	if turn.Seq != len(j.turns) {
		return fmt.Errorf("%w: model turn %d out of order", contract.ErrConflict, turn.Seq)
	}
	j.turns = append(j.turns, turn)
	return nil
}

// Tools implements contract.Journal.
func (s *Store) Tools(_ context.Context, lease contract.Lease) ([]contract.ToolRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.journalFor(lease)
	if err != nil {
		return nil, err
	}
	return append([]contract.ToolRecord(nil), j.tools...), nil
}

// SaveTool implements contract.Journal.
func (s *Store) SaveTool(_ context.Context, lease contract.Lease, rec contract.ToolRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.journalFor(lease)
	if err != nil {
		return err
	}
	for _, cur := range j.tools {
		if cur.Turn == rec.Turn && cur.NativeID == rec.NativeID {
			return fmt.Errorf("%w: tool %d/%s already recorded", contract.ErrConflict, rec.Turn, rec.NativeID)
		}
	}
	j.tools = append(j.tools, rec)
	return nil
}

// ------------------------------------------------------------------ Outbox

// Post implements contract.Outbox.
func (s *Store) Post(_ context.Context, d contract.Delivery) (contract.Delivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.outbox[d.ID]; ok {
		if !sameDelivery(cur, d) {
			return cur, false, contract.ErrConflict
		}
		return cur, false, nil
	}
	return s.postLocked(d), true, nil
}

func sameDelivery(a, b contract.Delivery) bool {
	return a.Text == b.Text && a.Scene.ID == b.Scene.ID
}

// postLocked inserts a new delivery.
func (s *Store) postLocked(d contract.Delivery) contract.Delivery {
	if _, ok := s.outbox[d.ID]; ok {
		return s.outbox[d.ID]
	}
	now := time.Now()
	if d.State == "" {
		d.State = contract.DeliveryPending
	}
	d.CreatedAt, d.UpdatedAt = now, now
	s.outbox[d.ID] = d
	return d
}

// Transition implements contract.Outbox.
func (s *Store) Transition(_ context.Context, employee, id string, from []contract.DeliveryState, to contract.DeliveryState, providerID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.outbox[id]
	if !ok || d.Employee != employee {
		return false, contract.ErrNotFound
	}
	allowed := false
	for _, f := range from {
		if d.State == f {
			allowed = true
			break
		}
	}
	if !allowed {
		return false, nil
	}
	d.State, d.UpdatedAt = to, time.Now()
	if providerID != "" {
		d.ProviderID = providerID
	}
	s.outbox[id] = d
	return true, nil
}

// Pending implements contract.Outbox.
func (s *Store) Pending(_ context.Context, employee string, limit int) ([]contract.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []contract.Delivery
	for _, d := range s.outbox {
		if d.Employee == employee && d.State == contract.DeliveryPending {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt) || (out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID < out[j].ID)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Unknown implements contract.Outbox.
func (s *Store) Unknown(_ context.Context, employee string, before time.Time, limit int) ([]contract.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []contract.Delivery
	for _, d := range s.outbox {
		if d.Employee == employee && d.State == contract.DeliveryUnknown && (before.IsZero() || d.UpdatedAt.Before(before)) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.Before(out[j].UpdatedAt) || (out[i].UpdatedAt.Equal(out[j].UpdatedAt) && out[i].ID < out[j].ID)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Deliveries returns every delivery (tests and demos).
func (s *Store) Deliveries() []contract.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contract.Delivery, 0, len(s.outbox))
	for _, d := range s.outbox {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

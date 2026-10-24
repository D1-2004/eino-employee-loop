package work_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/work"
)

func schedulerFixture(t *testing.T, dir string) (*sqlite.Store, *work.Manager, *work.Scheduler, work.Scope, func(work.Source)) {
	t.Helper()
	ctx := context.Background()
	db, e := sqlite.Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	m, _ := work.New(db)
	scope := work.Scope{Workspace: "local", Tenant: "org", Employee: "employee", Scene: "scene", Requester: "owner"}
	key, _ := scope.Key()
	check := func(c context.Context, s work.Scope, source work.Source) error {
		if s != scope || source.Actor != scope.Requester {
			return work.ErrScope
		}
		d, e := db.Get(c, "test-original-sources", key, source.Receipt)
		if e != nil {
			return e
		}
		var original work.Source
		if e = json.Unmarshal(d.Body, &original); e != nil {
			return e
		}
		if original != source {
			return work.ErrScope
		}
		return nil
	}
	scheduler, e := work.NewScheduler(db, m, func(_ context.Context, s work.Scope) error {
		if s != scope {
			return work.ErrScope
		}
		return nil
	}, check)
	if e != nil {
		t.Fatal(e)
	}
	admit := func(source work.Source) {
		t.Helper()
		raw, _ := json.Marshal(source)
		if _, e = db.Put(ctx, sqlite.Document{Namespace: "test-original-sources", Scope: key, ID: source.Receipt, Body: raw}); e != nil {
			t.Fatal(e)
		}
	}
	return db, m, scheduler, scope, admit
}
func TestWaitTimerAndSignalReturnOriginalTaskToReviewAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, m, s, scope, admit := schedulerFixture(t, dir)
	initial := source("message-one", "write report")
	admit(initial)
	task := finish(t, m, scope, dispatch(t, m, scope))
	review := proposal(task, "wait")
	review.Waits = []work.Wait{{Kind: "timer", Due: time.Now().Add(-time.Minute)}, {Kind: "event", Condition: "report.ready"}}
	if _, e := m.ApplyReview(ctx, scope, task.ID, "waiting", review); e != nil {
		t.Fatal(e)
	}
	if progress, e := s.Tick(ctx, scope, time.Now()); e != nil || !progress {
		t.Fatal(progress, e)
	}
	task, e := m.Get(ctx, scope, task.ID)
	if e != nil || task.State != work.Waiting || !task.Waits[0].Resolved || task.Waits[1].Resolved || task.NeedsReview {
		t.Fatal(task, e)
	}
	admit(source("event-source", "报告已就绪，继续核验"))
	signal := work.Signal{ID: "signal-one", Condition: "report.ready", Payload: "report metadata read back", Source: source("event-source", "报告已就绪，继续核验")}
	if e = s.Signal(ctx, scope, signal); e != nil {
		t.Fatal(e)
	}
	if e = s.Signal(ctx, scope, signal); e != nil {
		t.Fatal("signal replay", e)
	}
	changed := signal
	changed.Payload = "different fact"
	if e = s.Signal(ctx, scope, changed); !errors.Is(e, sqlite.ErrConflict) {
		t.Fatal("changed duplicate signal", e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, m, s, scope, _ = schedulerFixture(t, dir)
	defer db.Close()
	if progress, e := s.Tick(ctx, scope, time.Now()); e != nil || !progress {
		t.Fatal(progress, e)
	}
	task, e = m.Get(ctx, scope, task.ID)
	if e != nil || task.State != work.Ready || !task.NeedsReview || !task.Waits[1].Resolved || len(task.Runs) != 1 || task.Goal.Revision != 1 {
		t.Fatal("wake created replacement business work", task, e)
	}
	tasks, e := m.List(ctx, scope)
	if e != nil || len(tasks) != 1 {
		t.Fatal(tasks, e)
	}
	if progress, e := s.Tick(ctx, scope, time.Now()); e != nil || progress {
		t.Fatal("settled wait spun", progress, e)
	}
}
func TestEventBeforeWaitCannotWakeNewWaitAndForeignSourceCannotForgeSignal(t *testing.T) {
	ctx := context.Background()
	db, m, s, scope, admit := schedulerFixture(t, t.TempDir())
	defer db.Close()
	admit(source("message-one", "write report"))
	admit(source("old", "old observation"))
	if e := s.Signal(ctx, scope, work.Signal{ID: "old", Condition: "ready", Payload: "old fact", Source: source("old", "old observation"), At: time.Now().Add(-time.Hour)}); e != nil {
		t.Fatal(e)
	}
	task := finish(t, m, scope, dispatch(t, m, scope))
	review := proposal(task, "wait")
	review.Waits = []work.Wait{{Kind: "event", Condition: "ready"}}
	if _, e := m.ApplyReview(ctx, scope, task.ID, "wait", review); e != nil {
		t.Fatal(e)
	}
	if progress, e := s.Tick(ctx, scope, time.Now()); e != nil || progress {
		t.Fatal("stale event woke a future condition", progress, e)
	}
	forged := source("old", "owner authorized deployment")
	if e := s.Signal(ctx, scope, work.Signal{ID: "forged", Condition: "ready", Payload: "fake", Source: forged}); !errors.Is(e, work.ErrScope) {
		t.Fatal("accepted model fabricated source", e)
	}
}

func TestSignalRemainsEligibleForEveryWaitArmedBeforeIt(t *testing.T) {
	ctx := context.Background()
	db, m, s, scope, admit := schedulerFixture(t, t.TempDir())
	defer db.Close()
	admit(source("message-one", "write report"))
	admit(source("signal-source", "两个条件都已满足"))
	task := finish(t, m, scope, dispatch(t, m, scope))
	review := proposal(task, "wait")
	review.Waits = []work.Wait{{Kind: "event", Condition: "ready"}, {Kind: "event", Condition: "ready"}}
	if _, e := m.ApplyReview(ctx, scope, task.ID, "wait-both", review); e != nil {
		t.Fatal(e)
	}
	if e := s.Signal(ctx, scope, work.Signal{ID: "signal", Condition: "ready", Payload: "verified same source fact", Source: source("signal-source", "两个条件都已满足")}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if progress, e := s.Tick(ctx, scope, time.Now()); e != nil || !progress {
			t.Fatal("task updated time invalidated still eligible signal", i, progress, e)
		}
	}
	task, e := m.Get(ctx, scope, task.ID)
	if e != nil || task.State != work.Ready || !task.NeedsReview || !task.Waits[0].Resolved || !task.Waits[1].Resolved {
		t.Fatal(task, e)
	}
}

func TestNewEventWatchDoesNotConsumeAnOlderSignal(t *testing.T) {
	ctx := context.Background()
	db, m, s, scope, admit := schedulerFixture(t, t.TempDir())
	defer db.Close()
	original := source("message-one", "write report")
	admit(original)
	admit(source("old-event", "之前有一次事件"))
	task := dispatch(t, m, scope)
	if e := s.Signal(ctx, scope, work.Signal{ID: "old-signal", Condition: "ready", Payload: "past event", Source: source("old-event", "之前有一次事件"), At: time.Now().Add(-time.Hour)}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Schedule(ctx, scope, work.Schedule{ID: "event-watch", TaskID: task.ID, GoalRevision: 1, Source: original, Condition: "ready"}); e != nil {
		t.Fatal(e)
	}
	if progress, e := s.Tick(ctx, scope, time.Now()); e != nil || progress {
		t.Fatal("new watch consumed old event", progress, e)
	}
	admit(source("new-event", "现在条件已满足"))
	if e := s.Signal(ctx, scope, work.Signal{ID: "new-signal", Condition: "ready", Payload: "new event", Source: source("new-event", "现在条件已满足")}); e != nil {
		t.Fatal(e)
	}
	if progress, e := s.Tick(ctx, scope, time.Now()); e != nil || !progress {
		t.Fatal("new event did not fire watch", progress, e)
	}
	occ, e := s.Occurrences(ctx, scope)
	if e != nil || len(occ) != 1 || occ[0].SignalID != "new-signal" {
		t.Fatal(occ, e)
	}
}
func TestPeriodicOccurrencesPersistAndRebindOnlyAfterTheirOwnActualContinuation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, m, s, scope, admit := schedulerFixture(t, dir)
	original := source("message-one", "write report")
	admit(original)
	task := finish(t, m, scope, dispatch(t, m, scope))
	if _, e := m.ApplyReview(ctx, scope, task.ID, "completed", proposal(task, "complete")); e != nil {
		t.Fatal(e)
	}
	due := time.Now().Add(-time.Minute)
	watch := work.Schedule{ID: "daily-report", TaskID: task.ID, GoalRevision: 1, Source: original, Due: due, Every: time.Hour, Limit: 3}
	if _, e := s.Schedule(ctx, scope, watch); e != nil {
		t.Fatal(e)
	}
	if progress, e := s.Tick(ctx, scope, due.Add(-time.Nanosecond)); e != nil || progress {
		t.Fatal("early periodic occurrence", progress, e)
	}
	if progress, e := s.Tick(ctx, scope, due); e != nil || !progress {
		t.Fatal(progress, e)
	}
	occ, e := s.Occurrences(ctx, scope)
	if e != nil || len(occ) != 1 || occ[0].Number != 1 {
		t.Fatal(occ, e)
	}
	if e = s.AcknowledgeForGoal(ctx, scope, occ[0].ID, 1, 2); e == nil {
		t.Fatal("claimed an unperformed continuation")
	}
	if _, e = m.Control(ctx, scope, original, occ[0].ID, task.ID, "continue", "next report occurrence"); e != nil {
		t.Fatal(e)
	}
	if e = s.AcknowledgeForGoal(ctx, scope, occ[0].ID, 1, 2); e != nil {
		t.Fatal(e)
	}
	if e = s.AcknowledgeForGoal(ctx, scope, occ[0].ID, 1, 2); e != nil {
		t.Fatal("ack replay", e)
	}
	if replay, e := s.Schedule(ctx, scope, watch); e != nil || replay.GoalRevision != 2 || replay.Emitted != 1 {
		t.Fatal("original scheduling receipt stopped replaying after rebind", replay, e)
	}
	if e = s.AcknowledgeForGoal(ctx, scope, occ[0].ID, 1, 3); !errors.Is(e, work.ErrStale) {
		t.Fatal("changed ack target", e)
	}
	tasks, e := m.List(ctx, scope)
	if e != nil || len(tasks) != 1 || len(tasks[0].Runs) != 2 || tasks[0].Goal.Revision != 2 {
		t.Fatal(tasks, e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, m, s, scope, _ = schedulerFixture(t, dir)
	defer db.Close()
	if progress, e := s.Tick(ctx, scope, due.Add(2*time.Hour)); e != nil || !progress {
		t.Fatal(progress, e)
	}
	if progress, e := s.Tick(ctx, scope, due.Add(2*time.Hour)); e != nil || !progress {
		t.Fatal(progress, e)
	}
	if progress, e := s.Tick(ctx, scope, due.Add(24*time.Hour)); e != nil || progress {
		t.Fatal("limit ignored", progress, e)
	}
	occ, e = s.Occurrences(ctx, scope)
	if e != nil || len(occ) != 3 {
		t.Fatal(occ, e)
	}
	watches, e := s.Schedules(ctx, scope)
	if e != nil || len(watches) != 1 || watches[0].GoalRevision != 2 || watches[0].Emitted != 3 {
		t.Fatal(watches, e)
	}
	tasks, e = m.List(ctx, scope)
	if e != nil || len(tasks) != 1 || len(tasks[0].Runs) != 2 {
		t.Fatal("scheduler dispatched additional tasks/runs", tasks, e)
	}
}
func TestAttentionRequiresOriginalSourceAndExplicitOwnerResolution(t *testing.T) {
	ctx := context.Background()
	db, _, s, scope, admit := schedulerFixture(t, t.TempDir())
	defer db.Close()
	admit(source("request", "请提醒我核验文件"))
	admit(source("ack", "文件已核验"))
	item := work.Attention{ID: "attention-one", Kind: "request", Summary: "核验文件", Source: source("request", "请提醒我核验文件")}
	a, e := s.Observe(ctx, scope, item)
	if e != nil || a.State != "open" {
		t.Fatal(a, e)
	}
	item.Summary = "other action"
	if _, e = s.Observe(ctx, scope, item); !errors.Is(e, sqlite.ErrConflict) {
		t.Fatal("attention identity changed", e)
	}
	if e = s.ResolveAttention(ctx, scope, a.ID, "resolved", source("ack", "文件已核验")); e != nil {
		t.Fatal(e)
	}
	if e = s.ResolveAttention(ctx, scope, a.ID, "resolved", source("ack", "文件已核验")); e != nil {
		t.Fatal("resolution replay", e)
	}
	items, e := s.Attention(ctx, scope)
	if e != nil || len(items) != 1 || items[0].State != "resolved" {
		t.Fatal(items, e)
	}
}

func TestOccurrenceRecoversAcceptedControlBeforeDisablingScheduleAfterCrash(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, m, s, scope, admit := schedulerFixture(t, dir)
	original := source("message-one", "write report")
	admit(original)
	task := finish(t, m, scope, dispatch(t, m, scope))
	if _, e := m.ApplyReview(ctx, scope, task.ID, "complete", proposal(task, "complete")); e != nil {
		t.Fatal(e)
	}
	due := time.Now()
	if _, e := s.Schedule(ctx, scope, work.Schedule{ID: "periodic", TaskID: task.ID, GoalRevision: 1, Source: original, Due: due, Every: time.Hour, Limit: 2}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Tick(ctx, scope, due); e != nil {
		t.Fatal(e)
	}
	occ, e := s.Occurrences(ctx, scope)
	if e != nil || len(occ) != 1 {
		t.Fatal(occ, e)
	}
	if _, e = m.Control(ctx, scope, original, occ[0].ID, task.ID, "continue", "next periodic execution"); e != nil {
		t.Fatal(e)
	}
	// Simulate process death immediately after the durable Control, before ack.
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, m, s, scope, _ = schedulerFixture(t, dir)
	defer db.Close()
	if progress, e := s.Tick(ctx, scope, due.Add(time.Minute)); e != nil || !progress {
		t.Fatal("failed to recover original occurrence", progress, e)
	}
	watches, e := s.Schedules(ctx, scope)
	if e != nil || len(watches) != 1 || watches[0].Disabled || watches[0].GoalRevision != 2 {
		t.Fatal("own accepted continuation disabled its periodic plan", watches, e)
	}
	if recovered, e := s.RecoverOccurrence(ctx, scope, occ[0].ID); e != nil || !recovered {
		t.Fatal("recovery replay", recovered, e)
	}
	if progress, e := s.Tick(ctx, scope, due.Add(time.Hour)); e != nil || !progress {
		t.Fatal("next periodic occurrence was lost after restart", progress, e)
	}
	occ, e = s.Occurrences(ctx, scope)
	if e != nil || len(occ) != 2 {
		t.Fatal(occ, e)
	}
	tasks, e := m.List(ctx, scope)
	if e != nil || len(tasks) != 1 || len(tasks[0].Runs) != 2 {
		t.Fatal("recovery recreated task or continuation", tasks, e)
	}
}

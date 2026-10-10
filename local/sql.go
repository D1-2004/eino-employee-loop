package local

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/dingtalk-fde/eino-employee-loop/core"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// SQLStore implements core.TaskStore and core.MemoryStore on database/sql for both SQLite and PostgreSQL.
// Only placeholders, FTS and the schema pragmas differ; all queries are otherwise portable.
type SQLStore struct {
	db  *sql.DB
	d   Dialect
	lim core.MemoryLimits
	FTS bool // SQLite FTS5(trigram) available
}

func OpenSQLite(path string) (*SQLStore, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	s := &SQLStore{db: db, d: SQLite, lim: core.DefaultLimits}
	return s, s.migrate()
}

func OpenPostgres(dsn string) (*SQLStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	s := &SQLStore{db: db, d: Postgres, lim: core.DefaultLimits}
	return s, s.migrate()
}

func (s *SQLStore) SetLimits(l core.MemoryLimits) { s.lim = l }
func (s *SQLStore) DB() *sql.DB                   { return s.db }
func (s *SQLStore) Close() error                  { return s.db.Close() }

func (s *SQLStore) q(query string) string {
	if s.d != Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, c := range query {
		if c == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

const ddl = `
CREATE TABLE IF NOT EXISTS tasks(
 id TEXT PRIMARY KEY, tenant TEXT NOT NULL, scene TEXT NOT NULL, source_ref TEXT, requester TEXT,
 goal TEXT NOT NULL, prompt TEXT, status TEXT NOT NULL, idem_key TEXT NOT NULL, receipt TEXT, result TEXT,
 created_ms BIGINT NOT NULL, updated_ms BIGINT NOT NULL, done_ms BIGINT, attempts INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS ix_tasks_scene ON tasks(tenant, scene, created_ms);
CREATE INDEX IF NOT EXISTS ix_tasks_idem ON tasks(tenant, scene, idem_key);
CREATE UNIQUE INDEX IF NOT EXISTS ux_tasks_active_idem ON tasks(tenant, scene, idem_key) WHERE status IN ('queued','running');
CREATE TABLE IF NOT EXISTS task_events(task_id TEXT NOT NULL, tenant TEXT NOT NULL, ts_ms BIGINT NOT NULL, kind TEXT NOT NULL, detail TEXT);
CREATE INDEX IF NOT EXISTS ix_events_task ON task_events(task_id, ts_ms);
CREATE TABLE IF NOT EXISTS memory_facts(
 id TEXT PRIMARY KEY, tenant TEXT NOT NULL, scene TEXT NOT NULL, subject TEXT NOT NULL DEFAULT '', audience TEXT NOT NULL,
 kind TEXT NOT NULL, text TEXT NOT NULL, quote TEXT, speaker TEXT, speaker_role TEXT, evidence_scope TEXT, source_ref TEXT,
 said_ms BIGINT, created_ms BIGINT NOT NULL, status TEXT NOT NULL DEFAULT 'active', archived_by TEXT, text_hash TEXT NOT NULL, bytes INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS ix_mem_bag ON memory_facts(tenant, scene, audience, subject, status, created_ms);
CREATE UNIQUE INDEX IF NOT EXISTS ux_mem_active_hash ON memory_facts(tenant, scene, audience, subject, text_hash) WHERE status='active';
`

func (s *SQLStore) migrate() error {
	for _, st := range strings.Split(ddl, ";\n") {
		if strings.TrimSpace(st) == "" {
			continue
		}
		if _, err := s.db.Exec(st); err != nil {
			return fmt.Errorf("ddl: %w: %s", err, st)
		}
	}
	if s.d == SQLite {
		if _, err := s.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(id UNINDEXED, text, tokenize='trigram')`); err == nil {
			s.FTS = true
		}
	}
	return nil
}

// ---------------- core.TaskStore ----------------
const taskCols = `id,tenant,scene,COALESCE(source_ref,''),COALESCE(requester,''),goal,COALESCE(prompt,''),status,idem_key,COALESCE(receipt,''),COALESCE(result,''),created_ms,updated_ms,COALESCE(done_ms,0),attempts`

type scanner interface{ Scan(...any) error }

func scanTask(r scanner) (*core.Task, error) {
	t := &core.Task{}
	return t, r.Scan(&t.ID, &t.Tenant, &t.Scene, &t.SourceRef, &t.Requester, &t.Goal, &t.Prompt, &t.Status, &t.IdemKey, &t.Receipt, &t.Result, &t.CreatedMs, &t.UpdatedMs, &t.DoneMs, &t.Attempts)
}

func (s *SQLStore) Create(ctx context.Context, t *core.Task, win time.Duration) (*core.Task, bool, error) {
	now := core.NowMs()
	// 1. recent finished duplicate inside dedup window, or an active one
	row := s.db.QueryRowContext(ctx, s.q(`SELECT `+taskCols+` FROM tasks WHERE tenant=? AND scene=? AND idem_key=? AND (status IN ('queued','running') OR (status='done' AND done_ms>?)) ORDER BY created_ms DESC LIMIT 1`),
		t.Tenant, t.Scene, t.IdemKey, now-win.Milliseconds())
	if ex, err := scanTask(row); err == nil {
		return ex, false, nil
	}
	t.CreatedMs, t.UpdatedMs = now, now
	if t.Status == "" {
		t.Status = core.StQueued
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO tasks(id,tenant,scene,source_ref,requester,goal,prompt,status,idem_key,receipt,result,created_ms,updated_ms,done_ms,attempts) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,0)`),
		t.ID, t.Tenant, t.Scene, t.SourceRef, t.Requester, t.Goal, t.Prompt, t.Status, t.IdemKey, t.Receipt, t.Result, now, now)
	if err != nil { // lost the race on ux_tasks_active_idem -> return the winner
		row := s.db.QueryRowContext(ctx, s.q(`SELECT `+taskCols+` FROM tasks WHERE tenant=? AND scene=? AND idem_key=? AND status IN ('queued','running') LIMIT 1`), t.Tenant, t.Scene, t.IdemKey)
		if ex, e2 := scanTask(row); e2 == nil {
			return ex, false, nil
		}
		return nil, false, err
	}
	return t, true, nil
}

func (s *SQLStore) Get(ctx context.Context, tenant, id string) (*core.Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, s.q(`SELECT `+taskCols+` FROM tasks WHERE tenant=? AND id=?`), tenant, id))
	if err == sql.ErrNoRows {
		return nil, core.ErrNotFound
	}
	return t, err
}

func likeEscape(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(q)
}

func (s *SQLStore) Find(ctx context.Context, tenant, scene, query string, limit int) ([]*core.Task, error) {
	args := []any{tenant, scene}
	w := `tenant=? AND scene=?`
	if query != "" {
		w += ` AND LOWER(goal) LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscape(strings.ToLower(query))+"%")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT `+taskCols+` FROM tasks WHERE `+w+` ORDER BY created_ms DESC LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLStore) Transition(ctx context.Context, tenant, id string, from []string, to, result string) (bool, error) {
	ph := strings.TrimRight(strings.Repeat("?,", len(from)), ",")
	set := "status=?, updated_ms=?"
	args := []any{to, core.NowMs()}
	if result != "" {
		set += ", result=?"
		args = append(args, result)
	}
	if to == core.StDone || to == core.StFailed || to == core.StCancelled {
		set += ", done_ms=?"
		args = append(args, core.NowMs())
	}
	if to == core.StRunning {
		set += ", attempts=attempts+1"
	}
	args = append(args, tenant, id)
	args = append(args, anyStrs(from)...)
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE tasks SET `+set+` WHERE tenant=? AND id=? AND status IN (`+ph+`)`), args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func anyStrs(a []string) []any {
	o := make([]any, len(a))
	for i, v := range a {
		o[i] = v
	}
	return o
}

func (s *SQLStore) AddEvent(ctx context.Context, tenant, id, kind, detail string) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO task_events(task_id,tenant,ts_ms,kind,detail) VALUES(?,?,?,?,?)`), id, tenant, core.NowMs(), kind, detail)
	return err
}

func (s *SQLStore) FailOrphans(ctx context.Context, tenant, reason string) (int, error) {
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE tasks SET status='failed', result=?, updated_ms=?, done_ms=? WHERE tenant=? AND status IN ('queued','running')`), reason, core.NowMs(), core.NowMs(), tenant)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ---------------- core.MemoryStore ----------------
const factCols = `id,tenant,scene,subject,audience,kind,text,COALESCE(quote,''),COALESCE(speaker,''),COALESCE(speaker_role,''),COALESCE(evidence_scope,''),COALESCE(source_ref,''),COALESCE(said_ms,0),created_ms,status,COALESCE(archived_by,''),text_hash`

func scanFact(r scanner) (*core.Fact, error) {
	f := &core.Fact{}
	return f, r.Scan(&f.ID, &f.Tenant, &f.Scene, &f.Subject, &f.Audience, &f.Kind, &f.Text, &f.Quote, &f.Speaker, &f.SpeakerRole, &f.EvidenceScope, &f.SourceRef, &f.SaidMs, &f.CreatedMs, &f.Status, &f.ArchivedBy, &f.TextHash)
}

func (s *SQLStore) CountActive(ctx context.Context, tenant, scene, subject, audience string) (int, int, error) {
	var n, b int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*), COALESCE(SUM(bytes),0) FROM memory_facts WHERE tenant=? AND scene=? AND audience=? AND subject=? AND status='active'`), tenant, scene, audience, subject).Scan(&n, &b)
	return n, b, err
}

func (s *SQLStore) Put(ctx context.Context, f *core.Fact, originalMsg string) (*core.Fact, bool, error) {
	if originalMsg != "" && !strings.Contains(originalMsg, strings.TrimSpace(f.Quote)) {
		return nil, false, core.ErrBadQuote
	}
	if f.Quote == "" {
		f.Quote = f.Text
	}
	if len(f.Text) > s.lim.MaxFactBytes {
		f.Text = truncBytes(f.Text, s.lim.MaxFactBytes)
	}
	if f.Audience == "scene" {
		f.Subject = ""
	}
	n, b, err := s.CountActive(ctx, f.Tenant, f.Scene, f.Subject, f.Audience)
	if err != nil {
		return nil, false, err
	}
	if n >= s.lim.MaxActivePerBag || b+len(f.Text) > s.lim.MaxBytesPerBag {
		return nil, false, core.ErrQuota
	}
	f.TextHash = core.NormHash(f.Text)
	f.CreatedMs, f.Status = core.NowMs(), "active"
	if f.ID == "" {
		f.ID = "mem_" + newToken()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO memory_facts(id,tenant,scene,subject,audience,kind,text,quote,speaker,speaker_role,evidence_scope,source_ref,said_ms,created_ms,status,archived_by,text_hash,bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`),
		f.ID, f.Tenant, f.Scene, f.Subject, f.Audience, f.Kind, f.Text, f.Quote, f.Speaker, f.SpeakerRole, f.EvidenceScope, f.SourceRef, f.SaidMs, f.CreatedMs, f.Status, f.TextHash, len(f.Text))
	if err != nil { // duplicate active text
		tx.Rollback()
		row := s.db.QueryRowContext(ctx, s.q(`SELECT `+factCols+` FROM memory_facts WHERE tenant=? AND scene=? AND audience=? AND subject=? AND text_hash=? AND status='active'`), f.Tenant, f.Scene, f.Audience, f.Subject, f.TextHash)
		if ex, e2 := scanFact(row); e2 == nil {
			return ex, false, nil
		}
		return nil, false, err
	}
	if s.FTS {
		if _, err = tx.ExecContext(ctx, `INSERT INTO memory_fts(id,text) VALUES(?,?)`, f.ID, f.Text); err != nil {
			return nil, false, err
		}
	}
	return f, true, tx.Commit()
}

func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	for len(string(r)) > n {
		r = r[:len(r)-1]
	}
	return string(r)
}

func (s *SQLStore) GetFact(ctx context.Context, tenant, id string) (*core.Fact, error) {
	f, err := scanFact(s.db.QueryRowContext(ctx, s.q(`SELECT `+factCols+` FROM memory_facts WHERE tenant=? AND id=?`), tenant, id))
	if err == sql.ErrNoRows {
		return nil, core.ErrNotFound
	}
	return f, err
}

// bag filter: audience=me -> subject's private facts; audience=scene -> shared facts.
// subject="*" means any subject (used for pinned-preference fallback across speakers).
func (s *SQLStore) Recent(ctx context.Context, tenant, scene, subject, audience string, limit int) ([]*core.Fact, error) {
	if subject == "*" {
		return s.queryFacts(ctx, `SELECT `+factCols+` FROM memory_facts WHERE tenant=? AND scene=? AND audience=? AND status='active' ORDER BY created_ms DESC LIMIT ?`, tenant, scene, audience, limit)
	}
	return s.queryFacts(ctx, `SELECT `+factCols+` FROM memory_facts WHERE tenant=? AND scene=? AND audience=? AND subject=? AND status='active' ORDER BY created_ms DESC LIMIT ?`, tenant, scene, audience, subject, limit)
}

func (s *SQLStore) Search(ctx context.Context, tenant, scene, subject, audience, query string, limit int) ([]*core.Fact, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return s.Recent(ctx, tenant, scene, subject, audience, limit)
	}
	if s.d == SQLite && s.FTS && len([]rune(query)) >= 3 {
		return s.queryFacts(ctx, `SELECT `+factCols+` FROM memory_facts WHERE tenant=? AND scene=? AND audience=? AND subject=? AND status='active' AND id IN (SELECT id FROM memory_fts WHERE memory_fts MATCH ?) ORDER BY created_ms DESC LIMIT ?`,
			tenant, scene, audience, subject, `"`+strings.ReplaceAll(query, `"`, ``)+`"`, limit)
	}
	return s.queryFacts(ctx, `SELECT `+factCols+` FROM memory_facts WHERE tenant=? AND scene=? AND audience=? AND subject=? AND status='active' AND LOWER(text) LIKE ? ESCAPE '\' ORDER BY created_ms DESC LIMIT ?`,
		tenant, scene, audience, subject, "%"+likeEscape(strings.ToLower(query))+"%", limit)
}

func (s *SQLStore) queryFacts(ctx context.Context, q string, args ...any) ([]*core.Fact, error) {
	rows, err := s.db.QueryContext(ctx, s.q(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *SQLStore) Archive(ctx context.Context, tenant string, ids []string, by string) (int, error) {
	n := 0
	for _, id := range ids {
		res, err := s.db.ExecContext(ctx, s.q(`UPDATE memory_facts SET status='archived', archived_by=? WHERE tenant=? AND id=? AND status='active'`), by, tenant, id)
		if err != nil {
			return n, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
		if s.FTS {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM memory_fts WHERE id=?`, id)
		}
	}
	return n, nil
}

func (s *SQLStore) Compact(ctx context.Context, tenant, scene, subject, audience string) (int, string, error) {
	all, err := s.queryFacts(ctx, `SELECT `+factCols+` FROM memory_facts WHERE tenant=? AND scene=? AND audience=? AND subject=? AND status='active' ORDER BY created_ms ASC`, tenant, scene, audience, subject)
	if err != nil {
		return 0, "", err
	}
	var cand []*core.Fact
	for _, f := range all {
		if f.Kind != "preference" && f.Kind != "decision" && f.Kind != "digest" { // pinned kinds survive compaction
			cand = append(cand, f)
		}
	}
	if len(cand) <= s.lim.CompactKeepNewer {
		return 0, "", nil
	}
	old := cand[:len(cand)-s.lim.CompactKeepNewer]
	var lines []string
	var ids []string
	for _, f := range old {
		lines = append(lines, fmt.Sprintf("[%s] %s", f.ID, truncBytes(f.Text, 80)))
		ids = append(ids, f.ID)
	}
	d := &core.Fact{Tenant: tenant, Scene: scene, Subject: subject, Audience: audience, Kind: "digest", Speaker: "host", SpeakerRole: "host", EvidenceScope: core.ScopeHostDigest,
		Text: truncBytes(fmt.Sprintf("已归档 %d 条较早记忆（原文仍可按 id 取回）：\n%s", len(old), strings.Join(lines, "\n")), 2000)}
	d.Quote = d.Text
	d.TextHash = core.NormHash(d.Text)
	d.ID, d.CreatedMs, d.Status = "mem_"+newToken(), core.NowMs(), "active"
	// digest bypasses quota (it replaces many facts) but is written before archiving so a crash loses nothing
	if _, err = s.db.ExecContext(ctx, s.q(`INSERT INTO memory_facts(id,tenant,scene,subject,audience,kind,text,quote,speaker,speaker_role,evidence_scope,source_ref,said_ms,created_ms,status,archived_by,text_hash,bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`),
		d.ID, tenant, scene, subject, audience, d.Kind, d.Text, d.Quote, d.Speaker, d.SpeakerRole, d.EvidenceScope, "", 0, d.CreatedMs, d.Status, d.TextHash, len(d.Text)); err != nil {
		return 0, "", err
	}
	n, err := s.Archive(ctx, tenant, ids, d.ID)
	return n, d.ID, err
}

var (
	_ core.TaskStore   = (*SQLStore)(nil)
	_ core.MemoryStore = (*SQLStore)(nil)
)

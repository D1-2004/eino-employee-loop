-- PostgreSQL production schema (what the local SQLite DDL becomes). The same Go code (store/sql.go) already runs
-- against PG 17 in the contract test; the changes below are production hardening only.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS tasks(
  id TEXT PRIMARY KEY, tenant TEXT NOT NULL, scene TEXT NOT NULL, source_ref TEXT, requester TEXT,
  goal TEXT NOT NULL, prompt TEXT, status TEXT NOT NULL CHECK (status IN ('queued','running','done','failed','cancelled')),
  idem_key TEXT NOT NULL, receipt TEXT, result TEXT,
  created_ms BIGINT NOT NULL, updated_ms BIGINT NOT NULL, done_ms BIGINT, attempts INT NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS ix_tasks_scene ON tasks(tenant, scene, created_ms DESC);
-- the dispatch dedup guard: at most one ACTIVE task per (tenant, scene, idempotency key) — race-free across pods
CREATE UNIQUE INDEX IF NOT EXISTS ux_tasks_active_idem ON tasks(tenant, scene, idem_key) WHERE status IN ('queued','running');
CREATE INDEX IF NOT EXISTS ix_tasks_goal_trgm ON tasks USING gin (goal gin_trgm_ops);

CREATE TABLE IF NOT EXISTS task_events(task_id TEXT NOT NULL, tenant TEXT NOT NULL, ts_ms BIGINT NOT NULL, kind TEXT NOT NULL, detail TEXT);
CREATE INDEX IF NOT EXISTS ix_events_task ON task_events(task_id, ts_ms);

CREATE TABLE IF NOT EXISTS memory_facts(
  id TEXT PRIMARY KEY, tenant TEXT NOT NULL, scene TEXT NOT NULL, subject TEXT NOT NULL DEFAULT '', audience TEXT NOT NULL CHECK (audience IN ('me','scene')),
  kind TEXT NOT NULL, text TEXT NOT NULL, quote TEXT, speaker TEXT, speaker_role TEXT, evidence_scope TEXT, source_ref TEXT,
  said_ms BIGINT, created_ms BIGINT NOT NULL, status TEXT NOT NULL DEFAULT 'active', archived_by TEXT, text_hash TEXT NOT NULL, bytes INT NOT NULL);
CREATE INDEX IF NOT EXISTS ix_mem_bag ON memory_facts(tenant, scene, audience, subject, status, created_ms DESC);
CREATE UNIQUE INDEX IF NOT EXISTS ux_mem_active_hash ON memory_facts(tenant, scene, audience, subject, text_hash) WHERE status='active';
CREATE INDEX IF NOT EXISTS ix_mem_text_trgm ON memory_facts USING gin (text gin_trgm_ops);   -- replaces SQLite FTS5(trigram)
-- optional later: embedding vector(1024) + pgvector HNSW for semantic recall

CREATE TABLE IF NOT EXISTS conversation(tenant TEXT NOT NULL, scene TEXT NOT NULL, seq BIGINT NOT NULL, role TEXT NOT NULL, speaker TEXT, text TEXT NOT NULL,
  kind TEXT, source_ref TEXT, at_ms BIGINT, PRIMARY KEY(tenant, scene, seq));   -- partition by hash(tenant) or time when >100M rows

-- multi-tenant hardening (set per connection: SET app.tenant = '...')
ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tasks USING (tenant = current_setting('app.tenant', true));
ALTER TABLE memory_facts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON memory_facts USING (tenant = current_setting('app.tenant', true));

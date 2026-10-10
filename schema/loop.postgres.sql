-- Reference DDL for PostgreSQL (15+): the Employee Loop's own storage
-- ports (contract.EventLog, SessionQueue, Journal, Outbox, MemoryStore,
-- Conversation). The Task Manager owns separate tables
-- (taskmgr.postgres.sql) and may use another database.
--
-- The module ships in-memory implementations only (store/memstore for the
-- loop, taskmgr/memtask for the Task Manager). This file is reference DDL:
-- the durable layout a deployment may implement the same ports on.
-- docs/storage.md maps every port method to these tables and lists the
-- state machines and idempotency keys.
--
-- No foreign keys, cascades or cross-table triggers: each table has its own
-- retention, and every relationship is checked by the port implementation
-- inside its own transaction (see docs/storage.md, "Why no foreign keys").
--
-- Indexes: every secondary index is built with CREATE INDEX CONCURRENTLY,
-- one statement each, after the tables. PostgreSQL rejects CONCURRENTLY
-- inside a transaction block, so run this file with autocommit (psql
-- without --single-transaction) or put each index statement in its own
-- migration step. Primary keys are created with their (empty) tables.
--
-- Timestamps are timestamptz; *_ms columns keep the Unix milliseconds that
-- the contract types (contract.Fact, contract.Msg) carry.

-- ---------------------------------------------------------------------------
-- Tables

-- contract.EventLog: one receipt per (employee, source, event id).
-- applied_at is set by MarkApplied once the admission's effect committed;
-- a duplicate of a receipt with applied_at NULL re-applies the effect.
CREATE TABLE IF NOT EXISTS event_receipts (
  id             text        PRIMARY KEY,
  employee       text        NOT NULL,
  source         text        NOT NULL,
  event_id       text        NOT NULL,
  type           text        NOT NULL,
  class          text        NOT NULL CHECK (class IN ('business', 'execution', 'completion', 'schedule', 'enablement')),
  mode           text        NOT NULL CHECK (mode IN ('triggered', 'listening')),
  lane           text        NOT NULL CHECK (lane IN ('foreground', 'background', 'host')),
  scene_id       text        NOT NULL DEFAULT '',
  scene_tenant   text        NOT NULL DEFAULT '',
  session_kind   text,
  session_key    text,
  fingerprint    text        NOT NULL,
  event          jsonb       NOT NULL,
  admitted_at    timestamptz NOT NULL,
  applied_at     timestamptz
);

-- contract.SessionQueue.Cancel: the cancellations of one host-lane receipt,
-- applied once. The row is inserted in the transaction that cancels the
-- windows; a repeated Cancel finds it and returns the recorded count.
CREATE TABLE IF NOT EXISTS session_cancellations (
  employee    text        NOT NULL,
  receipt_id  text        NOT NULL,
  cancelled   integer     NOT NULL,
  applied_at  timestamptz NOT NULL,
  PRIMARY KEY (employee, receipt_id)
);

-- contract.SessionQueue: one row per window (one model wake).
CREATE TABLE IF NOT EXISTS session_windows (
  id                text        PRIMARY KEY,
  employee          text        NOT NULL,
  scene_id          text        NOT NULL,
  session_kind      text        NOT NULL,
  session_key       text        NOT NULL DEFAULT '',
  lane              text        NOT NULL CHECK (lane IN ('foreground', 'background')),
  state             text        NOT NULL CHECK (state IN ('open', 'ready', 'running', 'done', 'held', 'cancelled')),
  preemptible       boolean     NOT NULL DEFAULT false,
  opened_at         timestamptz NOT NULL,
  due_at            timestamptz NOT NULL,
  attempts          integer     NOT NULL DEFAULT 0,
  lease_worker      text,
  lease_token       text,
  lease_generation  bigint      NOT NULL DEFAULT 0,
  lease_until       timestamptz,
  reason            text,
  result            jsonb,
  updated_at        timestamptz NOT NULL
);

-- contract.WindowItem: the admitted events of a window, in order.
CREATE TABLE IF NOT EXISTS window_items (
  window_id   text    NOT NULL,
  seq         integer NOT NULL,
  receipt_id  text    NOT NULL,
  PRIMARY KEY (window_id, seq)
);

-- contract.Journal: provider exchanges of a window, replayed on re-claim.
CREATE TABLE IF NOT EXISTS journal_model_turns (
  window_id    text        NOT NULL,
  seq          integer     NOT NULL,
  generation   bigint      NOT NULL,
  request      bytea       NOT NULL,
  response     bytea,
  failure      text,
  recorded_at  timestamptz NOT NULL,
  PRIMARY KEY (window_id, seq)
);

-- contract.Journal: tool executions of a window, keyed by (model turn,
-- native call id): providers may reuse native ids across turns. error is the
-- exact text the model saw; refused keeps its refusal class for replay.
CREATE TABLE IF NOT EXISTS journal_tool_records (
  window_id    text        NOT NULL,
  turn         integer     NOT NULL,
  native_id    text        NOT NULL,
  generation   bigint      NOT NULL,
  call         jsonb       NOT NULL,
  result       jsonb       NOT NULL,
  error        text,
  refused      boolean     NOT NULL DEFAULT false,
  recorded_at  timestamptz NOT NULL,
  PRIMARY KEY (window_id, turn, native_id)
);

-- contract.Outbox: user-visible deliveries. A window's deliveries are
-- inserted by SessionQueue.Complete in the transaction that marks the
-- window done under its lease fence. unknown rows are reconciled by status
-- query (Outbox.Unknown, ix_outbox_deliveries_unknown), never re-sent.
CREATE TABLE IF NOT EXISTS outbox_deliveries (
  employee      text        NOT NULL,
  id            text        NOT NULL,
  scene_id      text        NOT NULL,
  scene         jsonb       NOT NULL,
  session_kind  text        NOT NULL,
  session_key   text        NOT NULL DEFAULT '',
  window_id     text        NOT NULL,
  reply_to      text,
  text          text        NOT NULL,
  locale        text,
  state         text        NOT NULL CHECK (state IN ('pending', 'unknown', 'provider_accepted', 'delivered', 'failed', 'suppressed')),
  provider_id   text,
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  PRIMARY KEY (employee, id)
);

-- contract.MemoryStore: facts with provenance; never deleted, only archived.
CREATE TABLE IF NOT EXISTS memory_facts (
  id              text    PRIMARY KEY,
  tenant          text    NOT NULL,
  scene           text    NOT NULL,
  subject         text    NOT NULL DEFAULT '',
  audience        text    NOT NULL CHECK (audience IN ('me', 'scene')),
  kind            text    NOT NULL CHECK (kind IN ('fact', 'decision', 'preference', 'pattern', 'pitfall', 'digest')),
  text            text    NOT NULL,
  quote           text    NOT NULL DEFAULT '',
  speaker         text    NOT NULL DEFAULT '',
  speaker_role    text    NOT NULL DEFAULT '',
  evidence_scope  text    NOT NULL DEFAULT '',
  source_ref      text    NOT NULL DEFAULT '',
  said_ms         bigint  NOT NULL DEFAULT 0,
  created_ms      bigint  NOT NULL,
  status          text    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  archived_by     text,
  text_hash       text    NOT NULL,
  bytes           integer NOT NULL
);

-- contract.Conversation: the dialogue log of a scene. Append is idempotent
-- per (tenant, scene, source_ref, kind) when source_ref is set
-- (ux_conversation_messages_source).
CREATE TABLE IF NOT EXISTS conversation_messages (
  tenant      text   NOT NULL,
  scene       text   NOT NULL,
  seq         bigint NOT NULL,
  role        text   NOT NULL CHECK (role IN ('user', 'assistant', 'host')),
  speaker     text   NOT NULL DEFAULT '',
  text        text   NOT NULL,
  kind        text   NOT NULL DEFAULT 'msg',
  source_ref  text   NOT NULL DEFAULT '',
  at_ms       bigint NOT NULL,
  PRIMARY KEY (tenant, scene, seq)
);

-- ---------------------------------------------------------------------------
-- Indexes (CONCURRENTLY, one statement each, outside any transaction)

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_event_receipts_identity
  ON event_receipts (employee, source, event_id);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_session_windows_open
  ON session_windows (employee, scene_id, session_kind, session_key) WHERE state = 'open';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_session_windows_running
  ON session_windows (employee, scene_id, session_kind, session_key) WHERE state = 'running';

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_session_windows_claimable
  ON session_windows (employee, lane, due_at) WHERE state IN ('open', 'ready');

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_session_windows_scene
  ON session_windows (employee, scene_id, state);

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_session_windows_session
  ON session_windows (employee, scene_id, session_kind, session_key, opened_at, id) WHERE state IN ('open', 'ready', 'running');

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_session_windows_lease
  ON session_windows (lease_until) WHERE state = 'running';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_window_items_receipt
  ON window_items (receipt_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_outbox_deliveries_pending
  ON outbox_deliveries (employee, created_at, id) WHERE state = 'pending';

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_outbox_deliveries_unknown
  ON outbox_deliveries (employee, updated_at) WHERE state = 'unknown';

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_outbox_deliveries_window
  ON outbox_deliveries (window_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_memory_facts_bag
  ON memory_facts (tenant, scene, audience, subject, status, created_ms DESC);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_memory_facts_active_text
  ON memory_facts (tenant, scene, audience, subject, text_hash) WHERE status = 'active';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_conversation_messages_source
  ON conversation_messages (tenant, scene, source_ref, kind) WHERE source_ref <> '';

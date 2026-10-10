-- Reference DDL for SQLite (3.35+): the single-machine twin of
-- loop.postgres.sql with the same tables, columns and index names.
--
-- Types: text for strings and JSON, integer for booleans (0/1), counters
-- and *_ms values, blob for raw journal bytes. Timestamps are text in
-- RFC 3339 UTC with a fixed width (strftime('%Y-%m-%dT%H:%M:%fZ')), so
-- that text order is time order.
--
-- No foreign keys, cascades or triggers, as in the PostgreSQL files. SQLite
-- has no concurrent index builds; the indexes follow the tables. Run with
-- PRAGMA journal_mode=WAL and a busy timeout when several workers share a
-- file.

-- ---------------------------------------------------------------------------
-- Tables

CREATE TABLE IF NOT EXISTS event_receipts (
  id             text    PRIMARY KEY,
  employee       text    NOT NULL,
  source         text    NOT NULL,
  event_id       text    NOT NULL,
  type           text    NOT NULL,
  class          text    NOT NULL CHECK (class IN ('business', 'execution', 'completion', 'schedule', 'enablement')),
  mode           text    NOT NULL CHECK (mode IN ('triggered', 'listening')),
  lane           text    NOT NULL CHECK (lane IN ('foreground', 'background', 'host')),
  scene_id       text    NOT NULL DEFAULT '',
  scene_tenant   text    NOT NULL DEFAULT '',
  session_kind   text,
  session_key    text,
  fingerprint    text    NOT NULL,
  event          text    NOT NULL,
  admitted_at    text    NOT NULL,
  applied_at     text
);

CREATE TABLE IF NOT EXISTS session_cancellations (
  employee    text    NOT NULL,
  receipt_id  text    NOT NULL,
  cancelled   integer NOT NULL,
  applied_at  text    NOT NULL,
  PRIMARY KEY (employee, receipt_id)
);

CREATE TABLE IF NOT EXISTS session_windows (
  id                text    PRIMARY KEY,
  employee          text    NOT NULL,
  scene_id          text    NOT NULL,
  session_kind      text    NOT NULL,
  session_key       text    NOT NULL DEFAULT '',
  lane              text    NOT NULL CHECK (lane IN ('foreground', 'background')),
  state             text    NOT NULL CHECK (state IN ('open', 'ready', 'running', 'done', 'held', 'cancelled')),
  preemptible       integer NOT NULL DEFAULT 0,
  opened_at         text    NOT NULL,
  due_at            text    NOT NULL,
  attempts          integer NOT NULL DEFAULT 0,
  lease_worker      text,
  lease_token       text,
  lease_generation  integer NOT NULL DEFAULT 0,
  lease_until       text,
  reason            text,
  result            text,
  updated_at        text    NOT NULL
);

CREATE TABLE IF NOT EXISTS window_items (
  window_id   text    NOT NULL,
  seq         integer NOT NULL,
  receipt_id  text    NOT NULL,
  PRIMARY KEY (window_id, seq)
);

CREATE TABLE IF NOT EXISTS journal_model_turns (
  window_id    text    NOT NULL,
  seq          integer NOT NULL,
  generation   integer NOT NULL,
  request      blob    NOT NULL,
  response     blob,
  failure      text,
  recorded_at  text    NOT NULL,
  PRIMARY KEY (window_id, seq)
);

CREATE TABLE IF NOT EXISTS journal_tool_records (
  window_id    text    NOT NULL,
  turn         integer NOT NULL,
  native_id    text    NOT NULL,
  generation   integer NOT NULL,
  call         text    NOT NULL,
  result       text    NOT NULL,
  error        text,
  refused      integer NOT NULL DEFAULT 0,
  recorded_at  text    NOT NULL,
  PRIMARY KEY (window_id, turn, native_id)
);

CREATE TABLE IF NOT EXISTS outbox_deliveries (
  employee      text    NOT NULL,
  id            text    NOT NULL,
  scene_id      text    NOT NULL,
  scene         text    NOT NULL,
  session_kind  text    NOT NULL,
  session_key   text    NOT NULL DEFAULT '',
  window_id     text    NOT NULL,
  reply_to      text,
  text          text    NOT NULL,
  locale        text,
  state         text    NOT NULL CHECK (state IN ('pending', 'unknown', 'provider_accepted', 'delivered', 'failed', 'suppressed')),
  provider_id   text,
  created_at    text    NOT NULL,
  updated_at    text    NOT NULL,
  PRIMARY KEY (employee, id)
);

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
  said_ms         integer NOT NULL DEFAULT 0,
  created_ms      integer NOT NULL,
  status          text    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  archived_by     text,
  text_hash       text    NOT NULL,
  bytes           integer NOT NULL
);

CREATE TABLE IF NOT EXISTS conversation_messages (
  tenant      text    NOT NULL,
  scene       text    NOT NULL,
  seq         integer NOT NULL,
  role        text    NOT NULL CHECK (role IN ('user', 'assistant', 'host')),
  speaker     text    NOT NULL DEFAULT '',
  text        text    NOT NULL,
  kind        text    NOT NULL DEFAULT 'msg',
  source_ref  text    NOT NULL DEFAULT '',
  at_ms       integer NOT NULL,
  PRIMARY KEY (tenant, scene, seq)
);

-- ---------------------------------------------------------------------------
-- Indexes

CREATE UNIQUE INDEX IF NOT EXISTS ux_event_receipts_identity
  ON event_receipts (employee, source, event_id);

CREATE UNIQUE INDEX IF NOT EXISTS ux_session_windows_open
  ON session_windows (employee, scene_id, session_kind, session_key) WHERE state = 'open';

CREATE UNIQUE INDEX IF NOT EXISTS ux_session_windows_running
  ON session_windows (employee, scene_id, session_kind, session_key) WHERE state = 'running';

CREATE INDEX IF NOT EXISTS ix_session_windows_claimable
  ON session_windows (employee, lane, due_at) WHERE state IN ('open', 'ready');

CREATE INDEX IF NOT EXISTS ix_session_windows_scene
  ON session_windows (employee, scene_id, state);

CREATE INDEX IF NOT EXISTS ix_session_windows_session
  ON session_windows (employee, scene_id, session_kind, session_key, opened_at, id) WHERE state IN ('open', 'ready', 'running');

CREATE INDEX IF NOT EXISTS ix_session_windows_lease
  ON session_windows (lease_until) WHERE state = 'running';

CREATE UNIQUE INDEX IF NOT EXISTS ux_window_items_receipt
  ON window_items (receipt_id);

CREATE INDEX IF NOT EXISTS ix_outbox_deliveries_pending
  ON outbox_deliveries (employee, created_at, id) WHERE state = 'pending';

CREATE INDEX IF NOT EXISTS ix_outbox_deliveries_unknown
  ON outbox_deliveries (employee, updated_at) WHERE state = 'unknown';

CREATE INDEX IF NOT EXISTS ix_outbox_deliveries_window
  ON outbox_deliveries (window_id);

CREATE INDEX IF NOT EXISTS ix_memory_facts_bag
  ON memory_facts (tenant, scene, audience, subject, status, created_ms DESC);

CREATE UNIQUE INDEX IF NOT EXISTS ux_memory_facts_active_text
  ON memory_facts (tenant, scene, audience, subject, text_hash) WHERE status = 'active';

CREATE UNIQUE INDEX IF NOT EXISTS ux_conversation_messages_source
  ON conversation_messages (tenant, scene, source_ref, kind) WHERE source_ref <> '';

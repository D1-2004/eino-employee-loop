-- Reference storage of a durable Host (PostgreSQL 15+).
--
-- The loop itself stores nothing: a Host persists what it needs to admit
-- events once, queue sessions, replay a re-claimed wake exactly and deliver
-- replies without duplicates. docs/host.md explains every table.
--
-- No foreign keys or cascades: each table has its own retention and the
-- Host checks relationships in its own transactions. Secondary indexes use
-- CREATE INDEX CONCURRENTLY, one statement each, so run this file with
-- autocommit (psql without --single-transaction).

-- One receipt per admitted event (event.Event), keyed by producer identity.
-- A replay with the same fingerprint is a duplicate; another fingerprint
-- under the same id is a conflict.
CREATE TABLE IF NOT EXISTS event_receipts (
  id           text        PRIMARY KEY,
  employee     text        NOT NULL,
  source       text        NOT NULL,
  event_id     text        NOT NULL,
  type         text        NOT NULL,
  class        text        NOT NULL CHECK (class IN ('business', 'execution', 'completion', 'schedule', 'enablement')),
  mode         text        NOT NULL CHECK (mode IN ('triggered', 'listening')),
  lane         text        NOT NULL CHECK (lane IN ('foreground', 'background', 'host')),
  scene_id     text        NOT NULL DEFAULT '',
  session_kind text,
  session_key  text,
  fingerprint  text        NOT NULL,
  event        jsonb       NOT NULL,
  admitted_at  timestamptz NOT NULL
);

-- One row per window: a coalesced batch of events of one session that one
-- wake handles. Windows of a session run in order; a scene's foreground
-- window runs before its background ones; preemptible windows are
-- cancelled when the scene admits new human input (event.Cancel).
CREATE TABLE IF NOT EXISTS session_windows (
  id           text        PRIMARY KEY,
  employee     text        NOT NULL,
  scene_id     text        NOT NULL,
  session_kind text        NOT NULL,
  session_key  text        NOT NULL DEFAULT '',
  lane         text        NOT NULL CHECK (lane IN ('foreground', 'background')),
  state        text        NOT NULL CHECK (state IN ('open', 'ready', 'running', 'done', 'held', 'cancelled')),
  preemptible  boolean     NOT NULL DEFAULT false,
  receipts     text[]      NOT NULL,
  opened_at    timestamptz NOT NULL,
  due_at       timestamptz NOT NULL,
  attempts     integer     NOT NULL DEFAULT 0,
  lease_token  text,
  lease_until  timestamptz,
  -- The frozen wake (loop.Wake messages and configuration) of the first
  -- claim: a re-claim reuses it instead of rebuilding context.
  wake         jsonb,
  outcome      jsonb,
  reason       text,
  updated_at   timestamptz NOT NULL
);

-- Provider exchanges of a window. A re-claim replays recorded responses for
-- an identical request and never asks the model again.
CREATE TABLE IF NOT EXISTS window_model_turns (
  window_id   text        NOT NULL,
  seq         integer     NOT NULL,
  request     bytea       NOT NULL,
  response    bytea,
  failure     text,
  recorded_at timestamptz NOT NULL,
  PRIMARY KEY (window_id, seq)
);

-- Tool executions of a window keyed by (model turn, native call id): the
-- effect's idempotency key. A committed effect keeps its receipt.
CREATE TABLE IF NOT EXISTS window_tool_records (
  window_id   text        NOT NULL,
  turn        integer     NOT NULL,
  native_id   text        NOT NULL,
  tool        text        NOT NULL,
  arguments   jsonb       NOT NULL,
  content     text        NOT NULL DEFAULT '',
  receipt     text,
  error       text,
  refused     boolean     NOT NULL DEFAULT false,
  recorded_at timestamptz NOT NULL,
  PRIMARY KEY (window_id, turn, native_id)
);

-- User-visible deliveries, written in the transaction that completes the
-- window. "unknown" rows (the send may have happened) are reconciled by a
-- status query and never re-sent.
CREATE TABLE IF NOT EXISTS outbox (
  employee    text        NOT NULL,
  id          text        NOT NULL,
  window_id   text        NOT NULL,
  scene_id    text        NOT NULL,
  reply_to    text,
  text        text        NOT NULL,
  state       text        NOT NULL CHECK (state IN ('pending', 'unknown', 'delivered', 'failed')),
  provider_id text,
  created_at  timestamptz NOT NULL,
  updated_at  timestamptz NOT NULL,
  PRIMARY KEY (employee, id)
);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_event_receipts_identity
  ON event_receipts (employee, source, event_id);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_session_windows_open
  ON session_windows (employee, scene_id, session_kind, session_key) WHERE state = 'open';

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_session_windows_claimable
  ON session_windows (employee, lane, due_at) WHERE state IN ('open', 'ready');

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_outbox_pending
  ON outbox (employee, state, created_at) WHERE state IN ('pending', 'unknown');

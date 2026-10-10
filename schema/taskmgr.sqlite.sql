-- Reference DDL for SQLite (3.35+): the single-machine twin of
-- taskmgr.postgres.sql with the same tables, columns and index names.
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

CREATE TABLE IF NOT EXISTS tasks (
  task_ref      text    PRIMARY KEY,
  employee      text    NOT NULL,
  tenant        text    NOT NULL DEFAULT '',
  scene_id      text    NOT NULL,
  scene         text    NOT NULL,
  requester_id  text    NOT NULL,
  requester     text    NOT NULL,
  source_ref    text    NOT NULL DEFAULT '',
  goal          text    NOT NULL DEFAULT '',
  prompt        text    NOT NULL DEFAULT '',
  locale        text    NOT NULL DEFAULT '',
  status        text    NOT NULL CHECK (status IN ('queued', 'running', 'done', 'failed', 'cancelled')),
  revision      integer NOT NULL DEFAULT 1,
  current_run   text,
  last_run      text,
  report        text,
  failure       text,
  attempts      integer NOT NULL DEFAULT 0,
  created_at    text    NOT NULL,
  updated_at    text    NOT NULL,
  done_at       text
);

-- Idempotency records of every Task Manager mutation. A control looks up
-- its key before checking expected_revision or the task state, so a replay
-- of an applied control returns the stored receipt.
CREATE TABLE IF NOT EXISTS task_requests (
  employee         text    NOT NULL,
  scope            text    NOT NULL CHECK (scope IN ('dispatch', 'control')),
  idempotency_key  text    NOT NULL,
  op               text    NOT NULL CHECK (op IN ('dispatch', 'steer', 'stop', 'continue')),
  fingerprint      text    NOT NULL,
  task_ref         text    NOT NULL,
  receipt          text    NOT NULL,
  created_at       text    NOT NULL,
  PRIMARY KEY (employee, scope, idempotency_key)
);

CREATE TABLE IF NOT EXISTS task_runs (
  task_ref      text    NOT NULL,
  run_ref       text    NOT NULL,
  status        text    NOT NULL CHECK (status IN ('accepted', 'running', 'done', 'failed', 'cancelled', 'superseded')),
  reason        text,
  prompt        text    NOT NULL DEFAULT '',
  corrections   text,
  progress_seq  integer NOT NULL DEFAULT 0,
  created_at    text    NOT NULL,
  started_at    text,
  ended_at      text,
  PRIMARY KEY (task_ref, run_ref)
);

CREATE TABLE IF NOT EXISTS task_events (
  task_ref         text    NOT NULL,
  seq              integer NOT NULL,
  kind             text    NOT NULL,
  run_ref          text,
  event_id         text,
  type             text,
  detail           text,
  event            text,
  publish_state    text    CHECK (publish_state IN ('pending', 'published', 'rejected')),
  publish_attempts integer NOT NULL DEFAULT 0,
  next_attempt_at  text,
  reject_code      text,
  created_at       text    NOT NULL,
  published_at     text,
  PRIMARY KEY (task_ref, seq)
);

-- ---------------------------------------------------------------------------
-- Indexes

CREATE INDEX IF NOT EXISTS ix_tasks_scene
  ON tasks (employee, scene_id, created_at DESC);

CREATE INDEX IF NOT EXISTS ix_tasks_active_requester
  ON tasks (employee, scene_id, requester_id) WHERE status IN ('queued', 'running');

CREATE UNIQUE INDEX IF NOT EXISTS ux_task_events_event_id
  ON task_events (event_id) WHERE event_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_task_events_pending
  ON task_events (next_attempt_at, task_ref, seq) WHERE publish_state = 'pending';

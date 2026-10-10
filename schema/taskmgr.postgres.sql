-- Reference DDL for PostgreSQL (15+): a durable Task Manager behind
-- contract.TaskManager. The Employee Loop never reads these tables; it sees
-- tasks only through the TaskManager boundary and execution events.
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

-- ---------------------------------------------------------------------------
-- Tables

-- contract.TaskManager: one row per task.
CREATE TABLE IF NOT EXISTS tasks (
  task_ref      text        PRIMARY KEY,
  employee      text        NOT NULL,
  tenant        text        NOT NULL DEFAULT '',
  scene_id      text        NOT NULL,
  scene         jsonb       NOT NULL,
  requester_id  text        NOT NULL,
  requester     jsonb       NOT NULL,
  source_ref    text        NOT NULL DEFAULT '',
  goal          text        NOT NULL DEFAULT '',
  prompt        text        NOT NULL DEFAULT '',
  locale        text        NOT NULL DEFAULT '',
  status        text        NOT NULL CHECK (status IN ('queued', 'running', 'done', 'failed', 'cancelled')),
  revision      bigint      NOT NULL DEFAULT 1,
  current_run   text,
  last_run      text,
  report        text,
  failure       text,
  attempts      integer     NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL,
  done_at       timestamptz
);

-- Idempotency records of every Task Manager mutation. A control looks up
-- its key before checking expected_revision or the task state, so a replay
-- of an applied control returns the stored receipt.
CREATE TABLE IF NOT EXISTS task_requests (
  employee         text        NOT NULL,
  scope            text        NOT NULL CHECK (scope IN ('dispatch', 'control')),
  idempotency_key  text        NOT NULL,
  op               text        NOT NULL CHECK (op IN ('dispatch', 'steer', 'stop', 'continue')),
  fingerprint      text        NOT NULL,
  task_ref         text        NOT NULL,
  receipt          jsonb       NOT NULL,
  created_at       timestamptz NOT NULL,
  PRIMARY KEY (employee, scope, idempotency_key)
);

-- Runs: one attempt at a task; steering supersedes the current run.
CREATE TABLE IF NOT EXISTS task_runs (
  task_ref      text        NOT NULL,
  run_ref       text        NOT NULL,
  status        text        NOT NULL CHECK (status IN ('accepted', 'running', 'done', 'failed', 'cancelled', 'superseded')),
  reason        text,
  prompt        text        NOT NULL DEFAULT '',
  corrections   jsonb,
  progress_seq  integer     NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL,
  started_at    timestamptz,
  ended_at      timestamptz,
  PRIMARY KEY (task_ref, run_ref)
);

-- Task history and the ordered outbox of execution events published to the
-- loop's EventSink (history rows leave event NULL).
CREATE TABLE IF NOT EXISTS task_events (
  task_ref         text        NOT NULL,
  seq              bigint      NOT NULL,
  kind             text        NOT NULL,
  run_ref          text,
  event_id         text,
  type             text,
  detail           text,
  event            jsonb,
  publish_state    text        CHECK (publish_state IN ('pending', 'published', 'rejected')),
  publish_attempts integer     NOT NULL DEFAULT 0,
  next_attempt_at  timestamptz,
  reject_code      text,
  created_at       timestamptz NOT NULL,
  published_at     timestamptz,
  PRIMARY KEY (task_ref, seq)
);

-- ---------------------------------------------------------------------------
-- Indexes (CONCURRENTLY, one statement each, outside any transaction)

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_tasks_scene
  ON tasks (employee, scene_id, created_at DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_tasks_active_requester
  ON tasks (employee, scene_id, requester_id) WHERE status IN ('queued', 'running');

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_task_events_event_id
  ON task_events (event_id) WHERE event_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS ix_task_events_pending
  ON task_events (next_attempt_at, task_ref, seq) WHERE publish_state = 'pending';

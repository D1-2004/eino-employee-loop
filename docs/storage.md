# Storage

[中文](storage.zh-CN.md)

The loop depends only on the storage ports in `contract/store.go`. The
only implementation that ships is in memory: `store/memstore` for the loop
and `taskmgr/memtask` for the Task Manager. No durable implementation
ships. The SQL files in `schema/` are reference DDL, one pair per service
that owns the tables:

| Service | PostgreSQL | SQLite | Behind |
| --- | --- | --- | --- |
| Employee Loop | [loop.postgres.sql](../schema/loop.postgres.sql) | [loop.sqlite.sql](../schema/loop.sqlite.sql) | the loop's ports (`EventLog` .. `Conversation`) |
| Task Manager | [taskmgr.postgres.sql](../schema/taskmgr.postgres.sql) | [taskmgr.sqlite.sql](../schema/taskmgr.sqlite.sql) | a durable `contract.TaskManager` |

The two services may keep their tables in different databases. Each pair
defines the same tables, columns and index names (a test keeps them in
step and checks that no table belongs to both services), and
`schema/sql_test.go` applies the SQLite files and, with
`EEL_TEST_POSTGRES_URL` set, the PostgreSQL files. A deployment passes its
durable ports to `employeeloop.Config.Ports`; ports it leaves nil stay in
memory.

- [Ports and tables](#ports-and-tables)
- [State machines](#state-machines)
- [Idempotency keys and uniqueness](#idempotency-keys-and-uniqueness)
- [Transactions a durable implementation needs](#transactions-a-durable-implementation-needs)
- [Why no foreign keys](#why-no-foreign-keys)
- [Indexes on PostgreSQL](#indexes-on-postgresql)
- [Short-lived coordination](#short-lived-coordination)
- [Tenancy](#tenancy)

## Ports and tables

| Owner | Port | Methods | Tables |
| --- | --- | --- | --- |
| Loop | `EventLog` | `Admit`, `MarkApplied`, `Receipt` | `event_receipts` |
| Loop | `SessionQueue` | `Enqueue`, `Claim`, `Complete`, `Retry`, `Hold`, `Cancel`, `Window` | `session_windows`, `window_items`, `session_cancellations`, `outbox_deliveries` (`Complete`) |
| Loop | `Journal` | `ModelTurns`, `SaveModelTurn`, `Tools`, `SaveTool` | `journal_model_turns`, `journal_tool_records` |
| Loop | `Outbox` | `Post`, `Transition`, `Pending`, `Unknown` | `outbox_deliveries` |
| Loop | `MemoryStore` | `Put`, `Search`, `Recent`, `Archive`, `Compact`, `CountActive`, `GetFact` | `memory_facts` |
| Loop | `Conversation` | `Append`, `Recent` | `conversation_messages` |
| Task Manager | `TaskManager` (service boundary, no storage port) | `Dispatch`, `Get`, `Find`, `Steer`, `Stop`, `Continue` | `tasks`, `task_requests`, `task_runs`, `task_events` |

The Task Manager's storage is its own: the contract defines only its
service boundary, and the reference tables show one way to make it
durable.

Column notes:

- `event_receipts.event` keeps the routed event (class and mode filled), so
  windows can be rebuilt from `window_items`. `applied_at` is set by
  `MarkApplied` once the admission's effect committed
  (`EventReceipt.Applied`).
- `session_cancellations` records the cancellations of one host-lane
  receipt (`SessionQueue.Cancel`), so a re-applied duplicate cancels
  nothing more.
- `session_windows` carries the lease (`lease_worker`, `lease_token`,
  `lease_generation`, `lease_until`) and the recorded `result`
  (`contract.WindowResult`).
- `window_items.seq` is the item order inside a window.
- `outbox_deliveries.scene` keeps the full `SceneRef` (the gateway needs
  the locator); `scene_id` is the query key.
- `memory_facts` and `conversation_messages` keep the Unix-millisecond
  fields of `contract.Fact` and `contract.Msg`.
- `tasks` holds the fields of `contract.TaskView` plus what the Task
  Manager needs to run (`prompt`, `locale`, `current_run`).
- `task_events` is both the task history (rows with `event` NULL) and the
  ordered outbox of execution events the Task Manager publishes to the
  loop; each event carries its `employee`, so one Task Manager can report
  to many loops.

## State machines

**Windows** (`contract.WindowState`):

```
   seal (due / max_items / successor)     claim               complete
  open ───────────────────────────► ready ─────► running ───────────────► done
   │                                  ▲            │  │ retry (not_before)
   │                                  └────────────┘  │ lease expired -> ready
   │                                                  └─ hold ──► held
   └── preempt / revoke (open, ready or running) ──────────────► cancelled
```

`done`, `held` and `cancelled` are final. Every transition out of `running`
is fenced by the lease. An open window is also sealed when an item of its
session cannot join it, before the successor window is created.

**Deliveries** (`contract.DeliveryState`): `pending -> unknown ->
delivered | failed`; `failed` only for a definite rejection, any other send
error stays `unknown`. `unknown -> delivered | provider_accepted | failed`
only by reconciliation (status query). `unknown` is never re-sent;
`suppressed` is reserved for Hosts. See
[architecture.md](architecture.md#outbox-and-delivery-states).

**Tasks** (the `contract.Task*` statuses of `TaskView`):

```
queued ──run starts──► running ──► done | failed
  ▲                       │
  │ continue (done/failed)│ stop ──► cancelled
  └───────────────────────┘ steer: stays active, new run
```

Every state change increments `revision`. A control is checked in this
order, in one transaction: a recorded `idempotency_key` returns its stored
receipt (or `ErrConflict` for other content); then a positive
`expected_revision` must equal `revision` (`ErrStaleRevision`); then
`Steer` and `Stop` need an active task (`queued`/`running`), `Continue` a
`done` or `failed` one (`ErrTaskState`). The key comes first because an
applied control has already moved the task on, and its replay must get the
receipt, not a stale-revision or state rejection.

**Runs** (`taskmgr.Run*`): `accepted -> running -> done | failed |
cancelled`, or `superseded` when a steer starts a new run.

**Task events**: `pending -> published | rejected`. Pending events of a
task are published in `seq` order; a transient failure blocks the events
behind it, a permanent rejection (invalid event, unknown type or scene,
conflict) is recorded and skipped.

**Memory facts**: `active -> archived` (forget or compaction). Text is never
deleted or rewritten; compaction writes one `digest` fact listing the
archived originals verbatim.

## Idempotency keys and uniqueness

| Key | Enforced by | Purpose |
| --- | --- | --- |
| `(employee, source, event_id)` + `fingerprint` | `ux_event_receipts_identity` | one admission per event; same content -> duplicate, other content -> conflict |
| one `open` window per `(employee, scene_id, session_kind, session_key)` | `ux_session_windows_open` | coalescing target |
| one `running` window per session | `ux_session_windows_running` | serialized sessions |
| `receipt_id` in one window only | `ux_window_items_receipt` | an event is processed once; `Enqueue` of a held receipt returns its window |
| `(employee, receipt_id)` | primary key of `session_cancellations` | a host-lane receipt cancels once |
| `(window_id, seq)` | primary key | model turns replay in order |
| `(window_id, turn, native_id)` | primary key | one execution per tool call; providers may reuse native ids across model turns |
| `(employee, id)` with id `<window>/reply` | primary key | one reply per window |
| `(employee, scope, idempotency_key)` + `fingerprint` | primary key of `task_requests` | Task Manager mutations; scope `dispatch` or `control` |
| `event_id` = `<task>/<run>/<kind>` | `ux_task_events_event_id` | republished events are duplicates at the loop |
| `(tenant, scene, audience, subject, text_hash)` among active facts | `ux_memory_facts_active_text` | memory dedup |
| `(tenant, scene, seq)` | primary key | conversation order |
| `(tenant, scene, source_ref, kind)` with `source_ref` set | `ux_conversation_messages_source` | `Conversation.Append` is idempotent, so a retried publish records a message once |

## Transactions a durable implementation needs

- `SessionQueue.Enqueue`: look the receipt up in `window_items` (return
  its window if present), apply `Admission.Cancel`, seal the session's
  open window if the item cannot join it, and insert or append the item,
  all in one transaction, so a foreground admission and the preemption it
  implies commit together.
- Admission is receipt first, effect second, `MarkApplied` last
  (`UPDATE event_receipts SET applied_at = now() WHERE id = $receipt`).
  They need not share a transaction: a duplicate of a receipt with
  `applied_at` NULL re-runs the effect, which is idempotent per receipt.
- `SessionQueue.Cancel`: insert the `session_cancellations` row and cancel
  the windows in one transaction; on a primary-key conflict return the
  recorded count and cancel nothing.
- `Claim`: seal every open window whose `due_at` passed.
- `Claim`: select the candidate and set `running` + lease in one statement
  or with `SELECT ... FOR UPDATE SKIP LOCKED`; the partial unique index on
  running windows rejects a second runner of one session. The candidate
  must be the oldest unfinished window of its session (`NOT EXISTS` an
  `open`/`ready`/`running` row of the same session with a smaller
  `(opened_at, id)`, served by `ix_session_windows_session`); a background
  candidate needs no open or ready foreground row in its scene, and a
  `preemptible` one no running foreground row either
  (`ix_session_windows_scene`). Window ids must sort in creation order.
- `Complete`, `Retry`, `Hold`, journal writes: `UPDATE/INSERT ... WHERE
  id = $window AND state = 'running' AND lease_token = $token AND
  lease_generation = $gen`; zero rows means `ErrLease`. `Complete` inserts
  the window's `outbox_deliveries` rows in the same transaction as the
  fenced update (an existing row with other text or scene rolls back with
  `ErrConflict`), so a done window always has its reply in the outbox.
- `Outbox.Unknown` reads `state = 'unknown' AND updated_at < $before`
  (`ix_outbox_deliveries_unknown`); reconciliation only moves rows out of
  `unknown` with a compare-and-set `Transition`.
- Task Manager: the state change, the `task_requests` row and the
  `task_events` rows it emits in one transaction (transactional outbox),
  then publish.

## Why no foreign keys

The reference DDL has no `FOREIGN KEY`, `REFERENCES`, cascades or
triggers:

- The services may run apart and keep their tables in different databases
  (`loop.*.sql` and `taskmgr.*.sql`); a reference across them could not be
  enforced anyway.
- Each table has its own retention. Receipts, journals and the outbox are
  audit trails; a cascade that deletes them with a window or a task would
  destroy evidence silently.
- Relationships are checked by the port implementation inside its own
  transaction (for example `Enqueue` writes the receipt link, `Complete`
  checks the lease), and fencing protects ordering better than
  referential constraints.
- Hot tables (`session_windows`, `outbox_deliveries`) avoid the extra
  locking that foreign-key checks add.
- The same DDL runs on SQLite and PostgreSQL.

## Indexes on PostgreSQL

Every secondary index in `loop.postgres.sql` and `taskmgr.postgres.sql`
is a separate
`CREATE [UNIQUE] INDEX CONCURRENTLY IF NOT EXISTS` statement after the
tables. PostgreSQL rejects `CONCURRENTLY` inside a transaction block, so
apply each file with autocommit (plain `psql -f`, not
`--single-transaction`), or give each index statement its own migration
step in tools that wrap a file in a transaction. Primary keys are created
with their tables.

Text search over `tasks.goal` and `memory_facts.text` uses substring
matching in the reference implementations. A deployment may add a
`pg_trgm` GIN index; some managed PostgreSQL services refuse
`CREATE EXTENSION` to application roles, so guard the extension and keep
the index optional.

## Short-lived coordination

The loop needs no coordination store: everything needed after a crash
lives in the tables above, and the contract defines no key-value port.
Hosts that add policies of their own (a speech budget, a lock around side
work) keep that state where they like; it may be lost. Suggested keys for
a Redis-like store, with `{t}` the tenant:

| Purpose | Key | Value | TTL | Operation |
| --- | --- | --- | --- | --- |
| speech budget per scene and hour | `{t}:speak:{scene}:{yyyymmddhh}` | integer | 2 h | `INCR` + `PEXPIRE` |
| per-scene lock for Hosts that serialize side work | `{t}:lock:scene:{scene}` | token | 30-60 s, renewed | `SET NX PX` + compare-and-delete |
| short dedup claims | `{t}:dedup:{scope}:{key}` | id | the dedup window | `SET NX` |
| optional hot conversation cache | `{t}:conv:{scene}` | last N messages | 1 h | list push + trim |

A lost key may at worst allow one duplicate inside its window; durable
idempotency stays with the unique indexes above.

## Tenancy

Loop tables are keyed by `employee`; memory, conversation and tasks also by
`tenant`. A multi-tenant PostgreSQL deployment can add row-level security
on the tenant columns (for example a policy
`USING (tenant = current_setting('app.tenant', true))` with the setting
applied per connection). The checkpoint table of the prototype's Eino
TurnLoop is superseded by the journal tables.

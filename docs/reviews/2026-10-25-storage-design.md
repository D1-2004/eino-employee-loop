# Storage design review: local reference and server integration

Reviewed on 2026-10-25 against `a7752bb` and the current composition candidate.
This is a source and local mechanism review, not proof of an external database
adapter, distributed deployment, remote execution or IM delivery.

The local design is coherent for a small, private reference application: one
Entry owns the workers, SQLite commits state transitions and receipts, and files
hold readable artifacts. It is **not yet a server database design simply because
State is replaceable**. A production adapter must preserve the behavioral
contracts below; implementing Get/Put over another database is insufficient.

## Actual persistence layout

There are four SQLite tables, not separate tables for every domain namespace.
Workflow sessions and model journals are records in `documents`. Leases are OS
advisory locks under the private state directory, not SQL lease rows.

| Physical object | Key / contents | Actual responsibility |
| --- | --- | --- |
| `eel_schema` | version 1 and applied timestamp | Reject an unsupported schema generation; current bootstrap is create-if-missing, not a general upgrade runner. |
| `documents` | `(namespace, scope, id)`, revision, JSON body, timestamp | Task aggregate, command receipt, execution record, journal/budget, source, job, cursor and workflow snapshot. |
| `events` | global monotonic sequence; unique `(namespace, scope, id)`; kind/body/time | Immutable, scoped facts; duplicate IDs require identical kind and bytes. Delivery consumes a persisted cursor. |
| `eino_checkpoints` | `(scope, version, id)`, opaque body | Native engine continuation, isolated by trusted scope and code version. Not a task outcome or proof of exit. |
| `locks/session-*.lock` | hash of complete workflow key | Local process ownership; the OS releases it on process death. No age-based takeover. |
| private `.md` files | path, SHA-256, bounded body | Artifacts and projections; hash CAS and atomic rename, separate from SQL transactions. |

Schema: [store.go](../../app/local/providers/storage/sqlite/store.go#L98).
Sessions: [session.go](../../app/local/providers/storage/sqlite/session.go#L17).
Artifacts: [store.go](../../app/local/providers/storage/markdown/store.go#L114).

## Correctness already present

- **Scoped keys.** Scope is the JSON encoding of trusted workspace, tenant,
  employee, scene and requester values, avoiding delimiter collisions. Task
  decoding also checks stored identity, scope and document revision. Storage is
  not an authorization service: callers must still authorize the original
  source and current audience. See [Scope](../../employee/contract/types.go#L17)
  and [task reads](../../employee/task/state/manager.go#L22).
- **CAS and atomic commands.** Revision zero is create-only; updates match the
  exact prior revision. A Task transition, its idempotency receipt and event are
  committed in one transaction. A failed callback rolls everything back.
  Reusing an operation key with a changed payload conflicts. See
  [CAS](../../app/local/providers/storage/sqlite/store.go#L179) and
  [command transaction](../../employee/task/state/manager.go#L71).
- **Short transactions.** Local model/executor/delivery calls occur after a
  durable reservation and outside the business transaction. Settlement gets a
  bounded context independent of caller cancellation. Unknown external effects
  cannot be safely turned into new operations.
- **Exit and goal are separate.** Submit intent is recorded before I/O; unknown
  submission retains the original key. A successor cannot bypass an old writer
  without exit proof. Review checks Task/Goal/Run revisions and physical exit;
  process completion alone does not complete the Goal. A never-submitted result
  requires a durable key tombstone, not a transient NotFound. See
  [submission](../../employee/task/state/manager.go#L283),
  [execution facts and Review](../../employee/task/state/manager.go#L332), and
  [never-submitted proof](../../employee/task/state/submission.go#L5).
- **Replayable model calls.** The physical-call budget and pending reservation
  are atomic. A complete response is persisted before tools can consume it.
  Crash recovery consumes another physical attempt rather than refunding the
  unknown first attempt. Streaming is explicitly rejected without a complete
  durable-response protocol. See [journal](../../employee/kernel/journal/model.go#L110).
- **Delivery reconciliation.** An unknown send retains its identity and frozen
  target; Lookup does not send. Provider acceptance and applied/delivered are
  distinct. Outbox enqueue precedes cursor advancement, so a crash between them
  replays the same enqueue key. This is an intentional idempotent repair path,
  not a claim that both writes share a transaction. See
  [outbox](../../employee/interaction/outbox.go#L147) and
  [event consumption](../../employee/interaction/delivery.go#L24).

## Findings and integration gates

No P1 blocker was demonstrated within the intended single-Entry local assembly.
The following are P2 gaps or explicit preconditions before a server integration
can claim equivalent behavior. They should be addressed by the selected host
route, not by creating a second Task authority or a new queue beside an existing
one.

### P2: generic JSON and full-history reads need a server query boundary

`State.List(namespace, scope)` returns all bodies with no pagination or state
projection. Task workers list complete Task history before discarding completed
items; scheduler, Outbox and Owner processing use similar namespace lists.
The document primary key correctly serves namespace/scope lookup, but it cannot
filter pending/due/held status hidden inside bodies.

Evidence: [List](../../app/local/providers/storage/sqlite/store.go#L222),
[worker](../../employee/task/progress.go#L18),
[scheduler](../../employee/task/state/scheduler.go#L179), and
[Outbox list](../../employee/interaction/outbox.go#L122).

Counterexample: add many historical completed tasks to one scope; the next
worker step still materializes them all. Switching SQLite to PostgreSQL without
changing this query contract does not fix that behavior. For the local reference,
this is an understandable simplicity tradeoff. Before multi-tenant service use,
introduce only the bounded ready/due/status projections actually consumed by the
chosen host. Reuse its existing pending-operation mechanisms where applicable.

### P2: event cursor index does not match its complete query

The actual query filters `namespace`, `scope`, and `sequence > cursor`, orders
by sequence and limits the page. Existing indexes are the global integer primary
key and unique namespace/scope/id. A 20-scope, 2,000-row SQLite probe using the
actual DDL produced `SEARCH events USING INTEGER PRIMARY KEY (rowid>?)`; it can
walk other scopes' rows after the cursor. The page limit alone does not prove a
bounded discovery scan.

An in-memory comparison with `(namespace, scope, sequence)` used all three
predicates. This demonstrates a specific query/index mismatch, not measured
production latency and not a reason to add speculative indexes everywhere.
An external adapter must inspect its actual plan and its expected retention
volume. No production schema or index was changed in this review.

Reproduce with the DDL from `sqlite/store.go`, insert interleaved scopes, run
`ANALYZE`, then:

```sql
EXPLAIN QUERY PLAN
SELECT sequence,id,kind,body,created_at FROM events
WHERE namespace='work-events' AND scope='s3' AND sequence>1700
ORDER BY sequence LIMIT 100;
```

### P2: hot Task state and replay payload are still coupled

The Task document embeds all Runs, outputs, sources, waits and deliveries. Every
transition event embeds the Task again. Documents/events have an 8 MiB per-record
limit; engine checkpoints have a separate 64 MiB limit. Those bounds prevent an
unbounded single write, but do not bound the total database or each namespace.

Evidence: [limits](../../app/local/providers/storage/sqlite/store.go#L29),
[Task shape](../../employee/task/state/types.go#L89),
[event snapshot](../../employee/task/state/manager.go#L129), and
[checkpoints](../../app/local/providers/storage/sqlite/checkpoint.go#L35).

Counterexample: an otherwise valid large Task near 8 MiB can fail its transition
when the containing event exceeds 8 MiB. Repeated accepted controls grow the
source list even if the run budget remains finite. This fails atomically, so it
does not silently commit half a transition, but it can prevent further progress.
Server storage should retain small typed state and versioned immutable payload
references, with digest/size/provenance and admission bounds before execution.
Do not truncate a result or delete old evidence to make a transition fit.
Preserve the exact completed-Goal snapshot needed by delayed Delivery; replacing
it with "read the latest Task" would corrupt historical attribution.

### P2 / hard precondition: leases and journal ownership are local assumptions

The default Entry acquires one leadership lease before starting workers. Local
execution also uses an in-process active map; Cancel/Stop explicitly rely on
Entry ownership proving that the previous local process is gone. Native
checkpoints use upsert rather than CAS and rely on exclusive workflow ownership.
The model journal permits a new Owner to retry a pending call, charging another
attempt, on the assumption that ownership was acquired correctly.

Evidence: [Entry lease](../../employee/entry/standalone.go#L89),
[Local executor](../../app/local/providers/execution/local.go#L190),
[checkpoint upsert](../../app/local/providers/storage/sqlite/checkpoint.go#L42),
and [pending journal](../../employee/kernel/journal/model.go#L121).

Counterexample outside that contract: two separately constructed Local providers
using the same database but no shared Entry lease can both run the same stored
execution; CAS can reject the older settlement only after both external calls
have happened. This is not a supported multi-writer composition. A distributed
adapter needs fenced ownership on every protected write and effect admission,
with an explicit loss-of-lease path. A timer-based lease alone cannot prove that
a remote process exited. Do not implement this by merely inserting SQL lease
rows or changing Owner strings.

### P2: schema and payload upgrades need an explicit compatibility policy

`migrate` currently creates version-1 tables and validates its small marker.
It does not provide ordered schema migrations, payload-version transformations,
checksums, readiness or mixed-version deployment support. Document bodies have
namespace-specific Go shapes; the physical schema marker alone does not version
those shapes. Checkpoint version isolation intentionally avoids loading an
incompatible engine snapshot, but old work still needs a compatible resolver or
an explicit unsupported-version result.

Before an adapter changes schema or persisted body semantics, define read/write
compatibility, old-work drain/upgrade, rollback limits and evidence retention.
Do not mutate all historical documents during ordinary startup or silently read
a current profile in place of a frozen one.

## Model binding and effort composition

Model receipts must be tied to the exact provider/model/effort/options binding,
not just a friendly model name. The journal fingerprints common call options,
messages and tool schemas. Provider-specific configuration must be frozen into
the binding identity supplied as journal `Name`; otherwise identical prompts
with different efforts could replay the wrong response. Pending/failed/complete
attempts and budget consumption must survive recovery; changing a route must not
refund a previous unknown physical call. This is physical-call accounting, not
strict token or money accounting.

The composition candidate supplies a resolved binding identity in the main
Runner; all alternate model entry points must preserve the same rule. Raw
credentials do not belong in that identity or in persisted model requests.

## What a storage Provider must implement

The existing [port](../../employee/ports/storage/ports.go#L35) is a behavioral
contract. A conforming implementation needs:

1. Atomic transactions across command state, operation receipts and events;
   create-only insertion, CAS updates, unique event identity and immutable replay.
2. Trusted scope separation, detached reads and writes, consistent revision
   checks, and no transaction callback usable after return.
3. Version-scoped native checkpoints plus exclusive, fenced workflow ownership;
   crash/lease-loss recovery must not introduce another active writer.
4. Durable effect reservations and exact lookup identity, bounded settlement,
   explicit unknown state, and no inference of exit from absence or timeout.
5. Artifact hash CAS and immutable version references, with a repair strategy
   across database/object-store boundaries. Files are not in the SQL transaction.
6. Explicit ownership of borrowed/owned resources, migration compatibility,
   bounded server query projections and a retention policy that preserves live
   idempotency, unknown-effect and recovery evidence.

Logical consistency should align across hosts; physical tables need not match.
Choose either the existing host Task authority plus shared kernel mechanisms,
or the complete standalone authority with a verified adapter. Do not import a
host's full task lifecycle under an atomic Run to compensate for storage gaps.

## Verification performed

`go test ./app/local/providers/storage/sqlite ./employee/ports/storage
./employee/kernel/journal` passed on 2026-10-25. Existing meaningful cases cover
atomic rollback/CAS, detached values, scoped checkpoints, process-death lock
release, canceled-response persistence, unknown Outbox reconciliation and a
killed model process whose retry still consumes budget. The isolated in-memory
SQLite query-plan probe above changed no application database. No load test,
external database integration, remote writer fencing or real IM acceptance is
claimed by this review.

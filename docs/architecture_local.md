# Local architecture

`local.Stack` opens one SQLite file that holds tasks, memory, conversation, and checkpoints.

## TaskStore (SQLite)

Tables (see `local/sql.go` DDL):

| Table | Role |
| --- | --- |
| `tasks` | Host task registry. PK `id`. Indexed by `(tenant,scene,created_ms)` and `(tenant,scene,idem_key)`. Partial unique index on active `(tenant,scene,idem_key)` where `status IN ('queued','running')` — this is the create-race winner. |
| `task_events` | Append-only lifecycle lines (`queued` / `done` / `failed` / …) keyed by `task_id`. |
| `memory_facts` | Provenanced memory; active-hash unique per bag; FTS5 trigram on SQLite. |
| `conversation` | Local message log (window + history for context builder). |
| `checkpoints` | Eino `CheckPointStore` KV (`id` → gob blob) for TurnLoop resume. |

`Create(task, dedupWindow)` returns an existing active (or recently-done-in-window) row with `created=false` instead of inserting a duplicate. `Transition` is compare-and-set on `status`. Statuses: `queued → running → done|failed|cancelled`.

MemKV covers short-lived locks / dispatch dedup keys. RedisKV + Postgres are available for cloud parity (`OpenPostgres`).

## Wire

```go
stack, _ := local.Open(ctx, local.StackOpts{DBPath: path, LLM: cfg, Executor: "mock"})
rt, _ := stack.WireRuntime(ctx, local.RuntimeOpts{Field: field, OnOutbound: send})
```

## Budgets

- `MaxModelRoundsPerTurn` (default **4**) → Eino `ChatModelAgent.MaxIterations`
- `BudgetFallbackDispatch` (default **true**): if a turn burns its rounds without a terminal tool, Host auto-dispatches the window text (or asks once on pure ❌ correction wakes)

Overall wakes are not hard-capped; only per-turn model rounds are.

## DWS

`providers/dws` defaults to **`dws event +listen-im`** (DingTalk Stream) for `message` + `reaction`. `--transport poll` keeps the legacy ticker. Only used by `cmd/dws-consumer`.

# Local assembly

`local.Stack` is one way to wire Core ports for a single machine. It opens one SQLite file that holds tasks, memory, conversation, and checkpoints.

## TaskStore (SQLite)

Tables (see `local/sql.go` DDL):

| Table | Role |
| --- | --- |
| `tasks` | Host task registry. Partial unique index on active `(tenant,scene,idem_key)`. |
| `task_events` | Append-only lifecycle lines. |
| `memory_facts` | Provenanced memory; FTS5 on SQLite. |
| `conversation` | Message log for the context builder. |
| `checkpoints` | Eino `CheckPointStore` KV for TurnLoop resume. |

`Create(task, dedupWindow)` returns an existing active row with `created=false` on race. Statuses: `queued → running → done|failed|cancelled`.

## Wire

```go
stack, _ := local.Open(ctx, local.StackOpts{DBPath: path, LLM: cfg, Executor: "mock"})
rt, _ := stack.WireRuntime(ctx, local.RuntimeOpts{Field: field, OnOutbound: send})
```

## Other assemblies

- `cmd/demo` — local only (stdin IM)
- `cmd/dws-consumer` — local + `adapters/dws` (DingTalk IM)

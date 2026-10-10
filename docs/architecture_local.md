# Local architecture

## Packages

| Path | Role |
|------|------|
| `core/` | Runtime, TurnLoop, context builder, Host tool orchestration. **Ports only** — no DWS, SQLite, OpenCode, or mock executor imports. |
| `checkpoint/` | Eino `CheckPointStore` on SQLite (`checkpoints` table). |
| `providers/store/` | SQLite/Postgres Task+Memory, MemKV/RedisKV, ConvStore. |
| `providers/im/` | Stdin + DWS poll/send. |
| `providers/session/opencode/` | `core.Session` via local `opencode` CLI. |
| `providers/tools/` | `MockExecutor` (`core.JobExecutor`). |
| `cmd/demo` | Wires SQLite + stdin + MockExecutor. |
| `cmd/dws-consumer` | Wires SQLite + DWS + MockExecutor. |

## Injection

```go
cfg := core.DefaultConfig(...)
cfg.CheckPointStore = cps          // checkpoint.Attach(db)
cfg.Executor = tools.MockExecutor{}
cfg.Session = &opencode.Session{}  // optional
cfg.OnOutbound = func(ctx, kind, text) { _ = im.Send(...) }

rt, err := core.NewRuntime(ctx, cfg, tasks, memory, kv, conv, stats)
```

## Data (SQLite)

`tasks`, `task_events`, `memory_facts` (+ FTS5), `conversation`, `checkpoints`.

# Local architecture

`local.Stack` opens one SQLite file that holds:

- `tasks`, `task_events`
- `memory_facts` (+ FTS5)
- `conversation`
- `checkpoints` (Eino CheckPointStore KV)

MemKV covers short-lived locks/dedup. RedisKV is available for cloud parity.

Wire:

```go
stack, _ := local.Open(ctx, local.StackOpts{DBPath: path, LLM: cfg, Executor: "mock"})
rt, _ := stack.WireRuntime(ctx, local.RuntimeOpts{Field: field, OnOutbound: send})
```

DWS stays in `providers/dws` and is only used by `cmd/dws-consumer`.

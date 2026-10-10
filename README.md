# Eino Employee Loop

Employee decision loop on [CloudWeGo Eino](https://github.com/cloudwego/eino) — ack early, decide once, Host owns tasks and memory.

> **Repo:** [dingtalk-fde/eino-employee-loop](https://github.com/dingtalk-fde/eino-employee-loop)

## Layout

```
core/                 Ports + runtime only
                      Field · Actor · Trigger · ProcessEvent
                      TaskStore · MemoryStore · KV · Conversation
                      IMProvider · Session · JobExecutor · ModelProvider · EventSink
                      ToolSpec + timeout budgets (injected)
                      NO SQLite / LLM / IM / OpenCode imports

local/                One assembly form
                      Wires Core ports → SQLite · checkpoint · LLM · OpenCode/mock · stdin IM
                      Stack.Open / WireRuntime

adapters/dws/         DingTalk Workspace IM adapter
                      Subscribe / Send / history / read-marker

cmd/demo/             Assembles local/ for a runnable demo
cmd/dws-consumer/     Assembles local/ + adapters/dws for a live conversation

cloud/                PG / Redis notes for cloud parity
docs/                 Design notes (optional reading)
```

**Rule:** `core/` never imports `local/` or `adapters/*`. Commands (and future assemblies) wire them in.

```
Trigger on Field
  └─► core.Runtime  (Eino TurnLoop + ChatModelAgent)
        ├─ tools via Host (timeouts from ToolSpec)
        ├─ dispatch_task → TaskStore + JobExecutor + ProcessEvent
        └─ reply → OnOutbound → IM.Send
```

## Quick start (local assembly)

```bash
export LLM_API_KEY=...          # or DEEPSEEK_API_KEY / DASHSCOPE_API_KEY
# optional: LLM_PROVIDER=dashscope LLM_MODEL=deepseek-v4.1-flash

go test ./local/ ./core/ ./adapters/...
go run ./cmd/demo -script
go run ./cmd/demo               # interactive; /tasks /memory /stats /quit
```

## Live IM (local + DWS adapter)

```bash
export LLM_PROVIDER=dashscope LLM_API_KEY=...
go run ./cmd/dws-consumer \
  --cid '...' \
  --kind group \
  --db ./data/employee.db \
  --executor opencode
```

## Abstractions

| Core type | Meaning |
| --- | --- |
| `Field` | Work locus — not bound to a chat or enterprise |
| `Actor` / `Trigger` | Who woke the loop and why (`user_message`, `reaction`, `task_result`, …) |
| `ProcessEvent` | Host lifecycle: accepted / progress / done / failed / budget fallback |
| Host wake budget | Per-wake model-round cap with optional Host fallback when exhausted |
| `ToolSpec` | Tools are injected; each call has a timeout budget |
| `IMProvider` | Channel port — any adapter that can Subscribe + Send |

## License

MIT

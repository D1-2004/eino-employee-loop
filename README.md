# Eino Employee Loop

Grok Bot–style **employee decision loop** on [CloudWeGo Eino](https://github.com/cloudwego/eino) v0.9.21.

> **Repo:** [dingtalk-fde/eino-employee-loop](https://github.com/dingtalk-fde/eino-employee-loop). Intended org home is `D1-2004` once an admin creates/transfers the public repo.

## Layout (keep Core clean & simple)

```
core/                 Ports + runtime orchestration only
                      Field · Actor · Trigger · ProcessEvent · ToolSpec/Timeout
                      TaskStore · MemoryStore · KV · Conversation · IMProvider
                      Session · JobExecutor · ModelProvider · EventSink
                      NO DWS / DingTalk / SQLite / OpenCode imports

local/                All-in-one local stack implementing Core ports
                      SQLite store + checkpoint · MemKV · stdin IM
                      LLM (DeepSeek/DashScope/OpenAI) · OpenCode · MockExecutor
                      Stack.Open / WireRuntime

providers/dws/        DingTalk Workspace IM adapter (poll + send + mark-read)

cmd/demo/             Local runnable demo
cmd/dws-consumer/     Long-running DWS conversation consumer
cloud/                PG DDL · Redis key notes
docs/                 Architecture & Multica mapping notes
```

Core never imports `local` or `providers/*`. Commands wire them in.

## Quick start (local)

```bash
export LLM_API_KEY=...          # or DEEPSEEK_API_KEY / DASHSCOPE_API_KEY
# optional: LLM_PROVIDER=dashscope LLM_MODEL=deepseek-v4.1-flash

go test ./local/
go run ./cmd/demo -script
go run ./cmd/demo               # interactive; /tasks /memory /stats /checkpoint /quit
```

## DWS consumer (Mac / dxxh)

```bash
export LLM_PROVIDER=dashscope LLM_API_KEY=...
export DWS_PROFILE='corpId:userId'   # optional

go run ./cmd/dws-consumer \
  --cid 'cid...' \
  --kind group \
  --db ~/.local/share/eino-employee-loop/employee.db \
  --executor opencode \
  --opencode-dir ~/eino-employee-loop-workspace
```

## How the loop works

```
Trigger (user_message | task_result | …) on Field
  └─► core.Runtime
        ├─► optional ack (AckMode=auto skips greetings/status lookups)
        └─► Eino TurnLoop + ChatModelAgent
              ├─ tools via Host (timeout budget per call)
              ├─ dispatch_task → TaskStore + JobExecutor + ProcessEvent(accepted)
              └─ reply / ask → OnOutbound → IM.Send
        └─► job done → ProcessEvent(done|failed) → Push task_result
```

## Multica lessons we ported

| Multica (`dt-fde-multica` employeeloop) | Eino Employee Loop |
| --- | --- |
| `scene.Ref` + Identity never from model text | `core.Field` + `Actor` / `Trigger` |
| Host owns effects + receipts | TaskStore receipts; `EventSink` for accepted/progress/done/failed |
| Context: system persona → memory → tasks → history → window | `genInput` sections + token budgets |
| Terminal tools + dispatch.reply in one shot | Terminal map + `dispatch_task.reply` |
| Max 3 model calls / wake | `MaxModelRoundsPerTurn=4` + Host auto-dispatch fallback when budget exhausted |
| Tool registry from Host | `ToolSpec` + per-call timeout budgets |

## Dialogue defaults

- Chinese colleague system instruction (`core.DefaultColleagueInstruction`)
- Markdown send, no AI badge, mark-read after inbound
- AckMode=auto: skip Hi/谢谢 and 「有哪些任务/记忆/进度」 — no more 「我看下…」 spam
- Prefer one accept line via `dispatch_task.reply`; Host events for lifecycle

## Turn budget & ❌ reactions

- Per-turn model rounds capped at **4** (`MaxModelRoundsPerTurn`). Overall wakes stay open.
- Budget exhausted without `reply` / `dispatch_task` / … → Host **auto-dispatches** the current window (no stall).
- DWS consumer uses **`dws event +listen-im`** (Stream) for messages **and** reactions. ❌ / `打叉` → `Trigger.Kind=reaction` correction wake.

## Task storage (local)

SQLite `tasks` + `task_events` in the same DB as memory/conversation/checkpoints. See [docs/architecture_local.md](docs/architecture_local.md).


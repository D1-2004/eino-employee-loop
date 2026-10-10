# Eino Employee Loop

Grok Bot–style **employee decision loop** on [CloudWeGo Eino](https://github.com/cloudwego/eino) v0.9.21.

> **Repo:** [dingtalk-fde/eino-employee-loop](https://github.com/dingtalk-fde/eino-employee-loop). Intended org home is `D1-2004` once an admin creates/transfers the public repo.

## Architecture

```
core/                         Runtime, TurnLoop wiring, context, Host tool orchestration
                              Ports only: TaskStore · MemoryStore · KV · Conversation ·
                              IMProvider · Session · JobExecutor  (NO concrete backends)
checkpoint/                   Eino CheckPointStore on SQLite (Get/Set/Delete)
providers/store/              SQLite/Postgres + MemKV/RedisKV + ConvStore
providers/im/                 StdinProvider · DWSProvider (poll + send)
providers/session/opencode/   Local OpenCode CLI adapter (core.Session)
providers/tools/              MockExecutor (injectable JobExecutor)
cmd/demo/                     Local runnable demo
cmd/dws-consumer/             Long-running DWS conversation consumer
cloud/                        PG DDL · Redis key design notes
docs/                         Architecture & cost notes
scripts/                      install-consumer.sh
```

Core never imports providers. Commands wire providers through constructor injection.

## Quick start (local demo)

```bash
export DEEPSEEK_API_KEY=sk-...
go test ./providers/store/ ./checkpoint/
go run ./cmd/demo -script
go run ./cmd/demo   # interactive; /tasks /memory /stats /checkpoint /quit
```

SQLite (default under `$TMPDIR`) holds `tasks`, `task_events`, `memory_facts` (+FTS5), `conversation`, `checkpoints`.

## DWS consumer

```bash
export DEEPSEEK_API_KEY=sk-...
export DWS_PROFILE='corpId:userId'   # optional

go run ./cmd/dws-consumer \
  --cid 'cid...' \
  --kind dm \
  --db ./data/employee.db \
  --profile "$DWS_PROFILE" \
  --self-name 'dxxh' \
  --poll 8s
```

## OpenCode session (optional)

`providers/session/opencode` shells out to local `opencode`:

- binary: `opencode` on PATH (e.g. nvm)
- data: `~/.local/share/opencode/`
- CLI: `opencode run -s <id> …`, `opencode session list`

Wire it on `core.Config.Session` from a command when you want durable coding sessions behind `dispatch_task` (pair with a custom `JobExecutor`).

## How the loop works

```
IM Provider (stdin | DWS)
  └─► core.Runtime.UserMessage
        ├─► ack (DeepSeek, thinking OFF) ──► OnOutbound → IM.Send
        └─► TurnLoop.Push (+ preempt)     [CheckPointStore]
              └─► ChatModelAgent
                    ├─ find_tasks / memory_*  ← injected stores
                    ├─ dispatch_task          → TaskStore + JobExecutor
                    └─ reply / ask / …        → OnOutbound
```

## Model providers (env)

`providers/llm` injects the models into Core (`core.ModelProvider`). Env:

```
LLM_PROVIDER=dashscope            # deepseek (default) | dashscope | openai
LLM_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1
LLM_API_KEY=...                   # never commit
LLM_MODEL=deepseek-v4.1-flash     # LLM_ACK_MODEL optional
```
Ack path runs with thinking off (`enable_thinking=false` on DashScope, `thinking: disabled` on DeepSeek).
`dws-consumer --executor opencode --opencode-dir <sandbox>` runs dispatched tasks in a local OpenCode session.

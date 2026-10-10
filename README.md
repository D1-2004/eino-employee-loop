# Eino Employee Loop

Grok Bot–style **employee decision loop** on [CloudWeGo Eino](https://github.com/cloudwego/eino) v0.9.21:

> **Repo home:** currently published at [dingtalk-fde/eino-employee-loop](https://github.com/dingtalk-fde/eino-employee-loop). Intended org home is `D1-2004` once an org admin creates/transfers the public repo.

1. **Instant ack** (thinking off, no tools) — ~0.7–1.5 s visible reply  
2. **Decision turn** (thinking on, tools) — dispatch / reply / ask / stay quiet  
3. **Background jobs** — `dispatch_task` returns a receipt immediately; result is pushed back via `TurnLoop.Push`  
4. **Preempt** — a new user message cancels the in-flight decision turn at a safe point  
5. **Task registry + memory** — Host-owned stores (not model self-talk) so status questions and “remember this” survive across turns  

Local stack: **SQLite (WAL + FTS5)** + **in-process KV**. Cloud-shaped adapters: **PostgreSQL** + **Redis** (same Go interfaces).

> This repository is the **implementation**. The companion evaluation harness under `employee_loop_evals` (not shipped here) measures accuracy on frozen real cases.

## Layout

```
store/          TaskStore · MemoryStore · KV interfaces
                SQLStore (SQLite + PostgreSQL) · MemKV · RedisKV
loop/           Runtime (ADK ChatModelAgent + TurnLoop), context builder, tools, demo CaseData
demo/           Interactive + scripted demo
cloud/         Production PG DDL · Redis key design
docs/           Architecture & local→cloud cost notes
```

## Quick start

```bash
export DEEPSEEK_API_KEY=sk-...          # required; never commit
# optional: export DEEPSEEK_BASE_URL=https://api.deepseek.com

go test ./store/                        # SQLite+MemKV contract tests
go run ./demo -script                   # scripted simulation
go run ./demo                           # interactive: type messages, /tasks /memory /stats /quit
```

Optional local cloud backends (contract test + runtime `-backend`):

```bash
redis-server --port 6390 --daemonize yes --save ""
# PostgreSQL 17 on :5433, database `rt`
export REDIS_ADDR=127.0.0.1:6390
export PG_DSN='postgres://postgres@127.0.0.1:5433/rt?sslmode=disable'
go test ./store/                        # also runs postgres+redis
```

## How the loop works

```
user message
   ├─► ack path (DeepSeek, thinking OFF) ──► outbox "ack"
   └─► TurnLoop.Push (WithPreempt if busy)
         └─► ChatModelAgent
               ├─ find_tasks / read_task  ← TaskStore (Host registry)
               ├─ memory_*               ← MemoryStore
               ├─ dispatch_task          → Create(+dedup) → goroutine job → Push(task_result)
               └─ reply / ask_human / stay_quiet → outbox
```

**Context composition** (token budgets): system + runtime notes → frozen clock → memory snapshot → registry snapshot → nearby conversation → current window (+ optional protocol-repair hint).

**Dedup**: KV `SET NX` claim + SQL partial unique index on `(tenant,scene,idem_key) WHERE status IN (queued,running)` + Jaccard on active/recent goals. User text matching “重新/再查/重跑” bypasses recent-done dedup.

**Memory provenance**: every fact stores speaker, role, quote (must be a substring of the sender’s message), and `evidence_scope`. Employee speech = “saying only, not execution”. Compaction **archives** old facts and writes a Host digest — it does **not** rewrite claims with an LLM.

## Interfaces (cloud mapping)

| Local | Cloud | Role |
|-------|-------|------|
| `SQLStore` / SQLite WAL | PostgreSQL | tasks, memory_facts, conversation, events |
| `MemKV` | Redis | dedup claims, scene locks, reported flags, counters |
| in-process `TurnLoop.Push` | Redis Streams / queue → pod | task_result fan-in |

See `cloud/pg_schema.sql` and `cloud/redis_keys.md`.

## Dependencies

- Go 1.24+（`go.mod` 可能要求更新的 toolchain；用 `GOTOOLCHAIN=auto`）
- `github.com/cloudwego/eino` **v0.9.21** (`adk.ChatModelAgent`, `adk.TurnLoop`)
- `github.com/cloudwego/eino-ext/components/model/deepseek`
- `modernc.org/sqlite` (pure Go), optional `jackc/pgx`, `redis/go-redis`

## License

MIT

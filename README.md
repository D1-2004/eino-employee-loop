# Eino Employee Loop

Grok Bot–style **employee decision loop** on [CloudWeGo Eino](https://github.com/cloudwego/eino) v0.9.21:

> **Repo home:** [dingtalk-fde/eino-employee-loop](https://github.com/dingtalk-fde/eino-employee-loop). Intended org home is `D1-2004` once an org admin creates/transfers the public repo.

1. **Instant ack** (thinking off, no tools) — ~0.7–1.5 s visible reply  
2. **Decision turn** (thinking on, tools) — dispatch / reply / ask / stay quiet  
3. **Background jobs** — `dispatch_task` returns a receipt immediately; result is pushed back via `TurnLoop.Push`  
4. **Preempt** — a new user message cancels the in-flight decision turn at a safe point  
5. **Task registry + memory** — Host-owned stores (not model self-talk)  
6. **SQLite CheckPointStore** — Eino `adk.TurnLoop` resume via table `checkpoints`  
7. **IM Provider** — stdin (local) or DWS poll (DingTalk Workspace CLI)

Local stack: **SQLite (WAL + FTS5)** for tasks / memory / conversation / **checkpoints**, plus in-process KV. Cloud-shaped adapters: PostgreSQL + Redis (same Go interfaces).

## Layout

```
store/              TaskStore · MemoryStore · KV (SQLite+PG · MemKV · RedisKV)
checkpoint/         Eino adk CheckPointStore on SQLite (Get/Set/Delete)
loop/               Runtime only: ack∥decision, tools, context builder
im/                 Provider · StdinProvider · DWSProvider (poll + send)
cmd/demo/           Local runnable demo (stdin IM + SQLite checkpoint)
cmd/dws-consumer/   Long-running consumer: poll one conversation via DWS
cloud/              Production PG DDL · Redis key design
docs/               Architecture & local→cloud cost notes
scripts/            install-consumer.sh (copy binary to a dws-capable host)
```

## Quick start (local demo)

```bash
export DEEPSEEK_API_KEY=sk-...          # required; never commit
# optional: export DEEPSEEK_BASE_URL=https://api.deepseek.com

go test ./store/ ./checkpoint/          # SQLite contracts + checkpoint
go run ./cmd/demo -script               # scripted simulation
go run ./cmd/demo                       # interactive stdin IM
#   /tasks /memory /stats /checkpoint /quit
```

SQLite file (default under `$TMPDIR`) holds:

| Table | Role |
|-------|------|
| `tasks` / `task_events` | Host task registry |
| `memory_facts` (+ FTS5) | Provenanced memory |
| `conversation` | Nearby dialogue log |
| `checkpoints` | Eino TurnLoop / Runner blob store (`id`, `blob`, `updated_at`) |

## DWS consumer

DingTalk has no native subscribe in DWS; the provider **polls** `dws chat +chat-messages` and sends with `dws chat +messages-send`. Profile/identity is configurable — **no tokens in the repo**.

```bash
export DEEPSEEK_API_KEY=sk-...
export DWS_PROFILE='corpId:userId'      # optional; default = current dws profile
# Real Niubility / DingTalk-FDE Director example (replace with your profile list entry):
#   dinge033bd8ae2430d9b35c2f4657eb6378f:014228605753392760936

go run ./cmd/dws-consumer \
  --cid 'cid...' \
  --db ./data/employee.db \
  --profile "$DWS_PROFILE" \
  --self-name 'DingTalk-FDE Director' \
  --poll 8s
```

Flags / env:

| Flag | Env | Meaning |
|------|-----|---------|
| `--cid` | `DWS_CID` | openConversationId (group) or openDingTalkId (dm) |
| `--kind` | `DWS_KIND` | `group` (default) or `dm` |
| `--profile` | `DWS_PROFILE` | `dws --profile` value |
| `--self-name` | `DWS_SELF_NAME` | skip inbound from these display names |
| `--db` | | SQLite path (tasks + memory + conversation + checkpoints) |

### Auth

If poll/send fails with auth errors, refresh the device login (do **not** invent tokens):

```bash
python3 "$HOME/.agents/skills/dws-env/scripts/dws_env.py" as <identity> -- auth login --device
# or:
dws --profile <corpId:userId> auth login --device
dws auth status   # confirm token_valid
```

The **stdin demo still works with no DWS** at all.

### Run on dxxh

The conversation consumer is meant to run on a machine where `dws` is already logged in (the user referred to **dxxh**). This repo ships the binary + install script; **deployment onto dxxh is not done from CI** until that machine is connected and approved.

```bash
./scripts/install-consumer.sh                # → ~/.local/bin/eino-dws-consumer
# on the target host (with dws + DEEPSEEK_API_KEY):
eino-dws-consumer --cid 'cid...' --db ~/.local/share/eino-employee-loop/employee.db
```

## How the loop works

```
IM Provider (stdin | DWS poll)
   └─► Runtime.UserMessage
         ├─► ack path (DeepSeek, thinking OFF) ──► OnOutbound → IM.Send
         └─► TurnLoop.Push (WithPreempt if busy)  [CheckPointStore = SQLite]
               └─► ChatModelAgent
                     ├─ find_tasks / read_task  ← TaskStore
                     ├─ memory_*               ← MemoryStore
                     ├─ dispatch_task          → Create(+dedup) → job → Push(task_result)
                     └─ reply / ask / …        → OnOutbound → IM.Send
```

**Dedup**: KV `SET NX` + SQL partial unique index on active `idem_key` + Jaccard on recent goals.  
**Memory provenance**: quote must be a substring of the speaker’s message; employee speech = saying only, not execution.  
**Checkpoint**: `TurnLoopConfig.Store` + `CheckpointID`; mid-turn cancel/interrupt resumes via `GenResume`.

## Interfaces (cloud mapping)

| Local | Cloud | Role |
|-------|-------|------|
| `SQLStore` / SQLite WAL | PostgreSQL | tasks, memory, conversation |
| `checkpoint.Store` / SQLite | Redis / PG bytea | Eino CheckPointStore |
| `MemKV` | Redis | dedup claims, locks |
| `im.StdinProvider` | — | local demo |
| `im.DWSProvider` | DWS poll + send | DingTalk IM |

See `cloud/pg_schema.sql` and `cloud/redis_keys.md`.

## Dependencies

- Go 1.24+ (`GOTOOLCHAIN=auto` if needed)
- `github.com/cloudwego/eino` **v0.9.21** (`adk.ChatModelAgent`, `adk.TurnLoop`)
- `github.com/cloudwego/eino-ext/components/model/deepseek`
- `modernc.org/sqlite` (pure Go); optional `jackc/pgx`, `redis/go-redis`
- Host `dws` CLI only for `cmd/dws-consumer`

## License

MIT

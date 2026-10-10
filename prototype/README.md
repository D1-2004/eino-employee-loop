# Prototype: single-process TurnLoop runtime

This directory keeps the first runtime of the Employee Loop for reference
and evaluation. It runs one employee in one process on an Eino TurnLoop:
acknowledge early, decide once, with the Host owning tasks and memory.

It is **superseded** by the service architecture at the module root:

| Prototype | Replaced by |
| --- | --- |
| `core.Runtime` (TurnLoop + ChatModelAgent) | `kernel` (one wake) + `scene` (admission, sessions, outbox) |
| `core` tools and Host | `toolhost` over `contract` ports |
| `core.Trigger` / `core.Field` | `contract.Event` / `contract.SceneRef` |
| SQLite task store + job executor | the Task Manager service (`taskmgr`, `cmd/task-manager`) |
| `adapters/dws` IM provider | the Event Gateway (`gateway/dws`, `cmd/dws-gateway`) |
| `local` LLM wiring | `providers/llm` |

New work belongs in the root packages. The prototype only receives fixes
that keep it building, so it stays usable as a baseline for evaluations
(`docs/bench.json`, `docs/summary.json`).

## Layout

```
core/               ports + TurnLoop runtime (no SQLite / LLM / IM imports)
local/              single-machine assembly: SQLite, checkpoint, stdin IM,
                    OpenCode or mock executor; LLM from providers/llm
adapters/dws/       core.IMProvider over gateway/dws.Client
cmd/demo/           scripted or interactive local demo
cmd/dws-consumer/   one live DingTalk conversation through dws
docs/               design notes, cost model and benchmark data
```

## Run

```bash
export LLM_API_KEY=...          # see providers/llm for providers and aliases
go test ./prototype/...
go run ./prototype/cmd/demo -script
go run ./prototype/cmd/dws-consumer --cid '...' --kind group --db ./data/employee.db --executor mock
```

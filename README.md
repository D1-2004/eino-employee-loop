# Eino Employee Loop

[中文](README.zh-CN.md)

An event-driven loop for AI employees that work in scenes such as group
chats, direct messages and enterprise workspaces, built on
[CloudWeGo Eino](https://github.com/cloudwego/eino). Every input is admitted
as an event, routed to a session of its scene, and decided in one model wake
that acts only through Host-executed tools. Background work belongs to a
separate Task Manager that reports back with events.

> **Repository:** [D1-2004/eino-employee-loop](https://github.com/D1-2004/eino-employee-loop)

- [Event and scene model](#event-and-scene-model)
- [Three services](#three-services)
- [Package map](#package-map)
- [Quick start](#quick-start)
- [Embedding the kernel](#embedding-the-kernel)
- [Testing](#testing)
- [Internationalization](#internationalization)
- [Documentation](#documentation)
- [License](#license)

## Event and scene model

```
Event {class, mode, scene, actor, refs}
  │  class: business | execution | completion | schedule | enablement
  │  mode:  triggered (passive) | listening (active)
  ▼
Catalog.Route ──► scene (employee x tenant x locus)
                   ├─ foreground  interactive          one per scene; people addressing the employee; served first
                   ├─ background  observation          one per scene; unaddressed messages; preemptible
                   │              follow_up/<task>     progress and results of work in flight
                   │              routine/<routine>    routine occurrences
                   │              lifecycle            bound to a scene, configuration changes
                   └─ host lane   receipts and facts, recorded without a wake

session ──► window (coalesced events) ──claim, lease──► wake (kernel) ──fenced complete──► outbox ──► gateway
```

- **Class** says what happened: `business`, `execution`, `completion`,
  `schedule`, `enablement`. **Mode** says why the employee received it:
  `triggered` (passive: addressed to it, or about its own work) or
  `listening` (active: it subscribed, observes or keeps time).
- Each scene has exactly one **foreground interactive session** and any
  number of **queued background sessions**. Only triggered business events
  from people reach the foreground; a person addressing the employee
  preempts its observation of the scene.
- A **window** is one wake over a coalesced burst of events. Claims are
  leased, every later write is fenced, model turns and tool effects are
  journaled, and replies commit with the window into an outbox that never
  resends a delivery of unknown outcome; it reconciles it by status query.

Details: [architecture](docs/architecture.md), [events](docs/events.md),
generated [catalog](docs/catalog.md).

## Three services

```
Event Gateway ──Event──► Employee Loop ──DispatchRequest──► Task Manager
      ▲                       │  ▲                               │
      └──────Delivery─────────┘  └──────────Event (execution.*)──┘
```

| Service | Owns | Boundary |
| --- | --- | --- |
| **Event Gateway** | provider connections, scene resolution (minted scene ids, never raw conversation ids), sending | publishes `contract.Event`; serves `contract.Deliverer` |
| **Employee Loop** | admission, sessions, wakes, journal, outbox, memory, conversation log | serves `contract.EventSink`; calls `contract.TaskManager` and `Deliverer` |
| **Task Manager** | tasks, runs, executors | serves `contract.TaskManager`; reports **only** by publishing `execution.*` events |

The services run in one process or apart without code changes: every
boundary value is a plain JSON type from `contract`, carried over HTTP by
`transport/httpapi`, with JSON Schemas in [schema/](schema/).
`internal/archtest` enforces the import boundaries as per-service
allow-lists (transports import only `contract`; loop packages only the
loop, `contract` and `i18n`). Every event names its `employee`, so one Task
Manager can serve many loops (`contract.SinkResolver`). Wire protocol and
topologies: [services](docs/services.md).

## Package map

| Package | Role |
| --- | --- |
| `contract` | zero-dependency contract: events, the standard catalogs, tools, the loop's storage ports, service boundaries |
| `i18n` | message bundles; embeds the loop's packs, other packages embed and merge their own |
| `kernel` | one wake on Eino components; `WakeJournal` replay; `kerneltest` scripted model and journal |
| `scene` | `Hub` (admission, sessions, fenced completion, outbox) and `Worker` |
| `toolhost` | the standard tools over contract ports |
| `store/memstore` | in-memory event log, session queue, journal, outbox, memory, conversation (the only shipped port implementation) |
| `employeeloop` | Employee Loop assembly (scene + toolhost over contract ports; memstore for any port not given) |
| `taskmgr` | Task Manager side: executor port, event sequence; `memtask` (in-memory), `executors` |
| `gateway/dws`, `gateway/stdin` | DingTalk via the `dws` CLI; a local stdin scene |
| `providers/llm` | Eino chat models from the environment (DeepSeek, DashScope, OpenAI-compatible) |
| `transport/httpapi` | HTTP handlers and clients for the three boundaries |
| `schema` | JSON Schemas and reference DDL per service (`loop.*.sql`, `taskmgr.*.sql`; PostgreSQL, SQLite) |
| `cmd/employee-loop`, `cmd/task-manager`, `cmd/dws-gateway` | the services |
| `cmd/catalog-doc` | generates [docs/catalog.md](docs/catalog.md) from the catalogs and packs |
| `examples/hostext` | a sample platform extension profile (event types, tools and their own i18n pack); not used by the loop |
| `e2e` | offline cross-service tests over `httptest` |
| `prototype` | the earlier single-process TurnLoop runtime, kept for reference |

## Quick start

**As a library** (embed the kernel, the scene hub or the contract in your own
host; see [docs/integration.md](docs/integration.md)):

```bash
go get github.com/D1-2004/eino-employee-loop@v0.1.0
```

Requirements: Go 1.26. A chat model is configured from the environment
(`LLM_API_KEY`, optional `LLM_PROVIDER` = `deepseek` | `dashscope` |
`openai`, `LLM_MODEL`, `LLM_BASE_URL`); see `.env.example`.

**All in one process** (stdin scene, in-process Task Manager with an echo
executor):

```bash
export LLM_API_KEY=...
go run ./cmd/employee-loop -stdin
go run ./cmd/employee-loop -stdin -locale zh-CN   # Chinese prompts and output
```

**Three processes** (set `EEL_TOKEN` to require a shared bearer token on
every boundary):

```bash
go run ./cmd/employee-loop -serve :8081 -tasks http://localhost:8082 -deliver http://localhost:8083
go run ./cmd/task-manager  -serve :8082 -loop http://localhost:8081
go run ./cmd/dws-gateway   -serve :8083 -loop http://localhost:8081 -cid '<conversation>' -self-id '<employee id>'
```

The gateway needs the `dws` CLI and a DingTalk profile; any other gateway
only has to `POST /v1/events` and serve `POST /v1/deliveries`.

## Embedding the kernel

A platform keeps its own tools, permissions, storage and context and lets
the module run the decision loop. The smallest embedding is one wake:

```go
res, err := kernel.Run(ctx, kernel.Options{
    Model: chatModel,         // any Eino ToolCallingChatModel
    Host:  platformHost,      // contract.Host: executes tools, enforces permissions, returns receipts
    Tools: platformToolSpecs, // []contract.ToolSpec
    RequireTerminal: true,
}, kernel.Wake{Identity: identity, Instructions: persona, Blocks: frozenContext})
// res.Decision: reply | quiet | dispatched | waiting; res.Receipts prove effects
```

Or run `scene.Hub` with platform storage and a `scene.KernelRunner` whose
`Hosts`, `Tools` and `Context` come from the platform. Runnable examples:
`ExampleRun` (`kernel/example_test.go`) and `ExampleKernelRunner`
(`scene/example_test.go`). [Multica](https://github.com/multica-ai/multica)
embeds the kernel this way behind a per-agent engine switch; the mapping of
its job kinds and components is in [integration](docs/integration.md).

## Testing

```bash
go test ./...                     # offline: scripted models, fakes, httptest
go vet ./...
go run ./cmd/catalog-doc -locale en -o docs/catalog.md   # after catalog or pack changes
EEL_TEST_POSTGRES_URL=postgres://... go test ./schema/    # also apply schema/*.postgres.sql
```

No test calls a model provider or the network. `kernel/kerneltest` scripts
model responses; `e2e` runs the three services apart over `httptest`;
`schema` validates real contract values against the JSON Schemas and
applies the SQLite DDL; `internal/catalogdoc` fails when the committed
catalog documents are stale.

## Internationalization

All user-visible and model-facing text lives in i18n packs: the loop's in
`i18n/locales`, every other package's next to its code (`<dir>/locales`),
merged with `Bundle.LoadFS`. `en` and `zh-CN` ship with identical key sets
per package. Hosts override or add locales at run time with `Bundle.Merge`
/ `Bundle.Load`. See [i18n](docs/i18n.md).

## Documentation

| Document | Contents |
| --- | --- |
| [architecture](docs/architecture.md) | scenes, sessions, lanes, admission to delivery, preemption, fencing, journals, outbox |
| [events](docs/events.md) | classification, routing invariants, refs, coalescing, custom types |
| [catalog](docs/catalog.md) | generated tables of the standard event types and tools |
| [tools](docs/tools.md) | tool contract, terminal decisions, receipts, refusals, budgets |
| [storage](docs/storage.md) | ports, tables, state machines, idempotency keys |
| [services](docs/services.md) | endpoints, payloads, errors, auth, topologies |
| [integration](docs/integration.md) | embedding into a platform; Multica mapping |
| [i18n](docs/i18n.md) | packs, resolution, overrides, adding a locale |

Each document has a Chinese twin (`*.zh-CN.md`).

## License

[MIT](LICENSE)

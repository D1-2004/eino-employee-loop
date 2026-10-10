# Integrating with a platform

[中文](integration.zh-CN.md)

A platform can adopt the Employee Loop at three depths. Each depth keeps the
platform's business logic on the platform side: the module only orders,
validates and routes decisions.

- [Choosing a depth](#choosing-a-depth)
- [Depth 1: the kernel](#depth-1-the-kernel)
- [Depth 2: the scene Hub with platform ports](#depth-2-the-scene-hub-with-platform-ports)
- [Depth 3: the services](#depth-3-the-services)
- [Example: Multica](#example-multica)
- [Checklist](#checklist)

## Choosing a depth

| Depth | The platform keeps | The module provides | Entry point |
| --- | --- | --- | --- |
| 1. Kernel | admission, sessions, journals, delivery, tools, context | one wake: model loop, batch validation, budgets, terminal decisions | `kernel.Run` |
| 2. Hub | tools, context, Host, storage implementations | admission, routing, sessions, claim order, fencing, outbox flow | `scene.Hub` + `scene.KernelRunner` |
| 3. Services | gateways and executors | the whole loop behind HTTP | `cmd/employee-loop`, `transport/httpapi` |

## Depth 1: the kernel

The platform injects everything into one wake:

```go
res, err := kernel.Run(ctx, kernel.Options{
    Model:           chatModel,      // any Eino model.ToolCallingChatModel
    Host:            platformHost,   // contract.Host: executes every tool, owns receipts
    Tools:           specs,          // []contract.ToolSpec with descriptions resolved
    Budget:          contract.Budget{MaxResponses: 8, MaxConsecutiveErrors: 3, MaxAttempts: 11},
    RequireTerminal: true,
    Validator:       platformBatchRules, // optional contract.BatchValidator
    TextGuard:       platformTextRules,  // optional contract.TextGuard
    FinalText:       platformFinalText,  // optional contract.FinalTextHost
    Permanent:       isPermanentProviderError,
    Observer:        tracing,            // kernel.Observer
}, kernel.Wake{
    Identity:   contract.Identity{Tenant: t, Employee: e, Scene: scene, WindowID: jobID},
    Transcript: frozenMessages,      // or Instructions + Blocks
})
```

- **Context.** Either pass a `Transcript` the platform already rendered
  (exactly one system message, first) or `Instructions` plus
  `ContextBlock`s that the kernel renders as user-role data with localized
  labels. Blocks never grant authority.
- **Tools.** Convert platform tools to `contract.ToolSpec`: `kind` from
  whether the tool commits an effect or ends the wake, `parameters` as JSON
  Schema, `exclusive` where needed. Set `timeout` when the platform's own
  deadline should govern.
- **Host.** `Execute(ctx, identity, call)` must check permissions from the
  identity (never from arguments), deduplicate effects by identity plus
  native call id, and return a receipt for every committed effect.
- **Durability.** Per claim, create `kernel.NewWakeJournal(journal, lease)`
  over a `contract.Journal`, wrap the model and Host with its `Model` and
  `Host`, and start a re-claim from its `FrozenTranscript`; or keep the
  platform's own journal and replay it in the Host and model adapters.
- **Offline tests.** `kernel/kerneltest` scripts the model;
  `ExampleRun` in `kernel/example_test.go` is a complete wake with a
  platform tool.

## Depth 2: the scene Hub with platform ports

The platform implements the storage ports (`EventLog`, `SessionQueue`,
`Journal`, `Outbox`; see [storage.md](storage.md)) and plugs its own Host
into `KernelRunner`:

```go
runner := &scene.KernelRunner{
    Model:    chatModel,
    Hosts:    func(win contract.Window) contract.Host { return platform.HostFor(win) },
    Tools:    platformSpecs,          // filtered per lane and session by ToolsFor
    Journal:  journal,
    Persona:  trustedInstructions,
    Context:  platform.FreezeContext, // memory, history, resources as blocks
    Budgets:  map[contract.Lane]contract.Budget{contract.LaneBackground: {MaxResponses: 2, MaxConsecutiveErrors: 2, MaxAttempts: 3}},
    Messages: bundle,                 // i18n overrides
}
hub := &scene.Hub{Employee: id, Catalog: catalog, Log: log, Queue: queue, Outbox: outbox,
    Runner: runner, OnHostEvent: platform.ApplyHostEvent}
worker := &scene.Worker{Hub: hub, Deliverer: platform.Channel, Concurrency: 4}
```

- Publish platform inputs as events (`hub.Publish`), registering custom
  types where the standard catalog lacks one ([events.md](events.md)).
- `OnHostEvent` applies host-lane facts (receipts, terminal facts,
  revisions) without a wake. It must be idempotent per receipt: when an
  admission did not finish, the republished duplicate runs it again.
- `SessionQueue.Complete` writes the window's deliveries in the same
  fenced transaction; implement it on the store that holds the outbox.
- `toolhost` can serve the standard tools next to platform tools: offer
  `toolhost.Host.Specs()` plus your own specs and route calls by name in
  the bound Host.
- `ExampleKernelRunner` in `scene/example_test.go` is a runnable version.

## Depth 3: the services

Run `cmd/employee-loop` (or assemble `employeeloop.New`) and connect the
platform as a gateway (publish to `POST /v1/events`, serve
`POST /v1/deliveries`) and as a Task Manager (serve `/v1/tasks...`, publish
`execution.*` events back). See [services.md](services.md). The in-memory
stores make this depth suitable for single-process deployments; pass
durable implementations of the loop's ports through
`employeeloop.Config.Ports` (reference DDL in [storage.md](storage.md)).
One Task Manager may serve several loops: its events carry `employee` and
route through `contract.SinkResolver`.

## Example: Multica

[Multica](https://github.com/multica-ai/multica)'s EmployeeLoop embeds the
module at depth 1. Its `server/internal/service/einoengine` runs a wake on
`kernel.Run` with Multica's frozen transcript, tool registry, durable model
and Host; admission, journals, prompt assembly and delivery stay in Multica.
A per-agent setting (`employee_loop_engine`: `multica` or `eino`) selects
the kernel for new wakes; a wake freezes its engine. Multica owns that
setting and any event announcing its change; the module has no engine
switch.

### Job kinds to event types

Multica jobs (`server/internal/employeeentry`) are classified as contract
events for traces (`einoengine.Classify`). `plan.step_completed` and
`collection.ready` are Multica's own extension types, registered on its
catalog with their texts in its own pack (the shape of
[`examples/hostext`](../examples/hostext)):

| Multica job kind | Contract event type | Lane / session |
| --- | --- | --- |
| `message` | `message.addressed` | foreground / interactive |
| `message` whose source events are native proactive group events | `message.observed` | background / observation |
| `human_response` | `human.response` | foreground / interactive |
| `task_wake` (`execution.result`) | `execution.result` | background / follow_up |
| `task_wake` (`execution.progress`) | `execution.progress` | background / follow_up |
| `task_wake` (`execution.follow_up`) | `plan.step_completed` | background / follow_up |
| `task_wake` (`collection.ready`) | `collection.ready` | background / follow_up |
| `task_wake` (`routine.decision`, `routine.group_news`, `webhook.decision`) | `routine.due` | background / routine |
| `task_wake` (`routine.group_proactive`) | `observation.due` | background / observation |
| `task_event_batch` | `execution.progress`, or `execution.result` when an item is a result | background / follow_up |
| `group_observation` | `observation.due` | background / observation |
| any other `task_wake` kind | `task_wake.<kind>`, not in the catalog, so it does not route | - |

### Components to services

| Service | Multica components |
| --- | --- |
| Event Gateway | DingTalk native subscription and inbound handlers (`handler/dws_native_*`, `dwseventsource`), A2A ingress; delivery through `employeedelivery` and the result/notice senders |
| Employee Loop | `employeeentry` (admission, typed scene jobs, leases), the scene entry worker (`handler/employee_scene_entry_worker.go`), `service/employeeloop` (Host contract and built-in kernel) or `service/einoengine` (this kernel), employee memory and context builders |
| Task Manager | `employeetask` (Employee Tasks, runs, steering), the task wake producers (`handler/employee_task_wake.go`) and the runtimes that execute tasks (daemon, cloud sandboxes) |

Names refer to the Multica repository at the time of writing; the mapping
documents intent, not a stable API.

## Checklist

- [ ] Scene ids are minted, never raw provider conversation ids.
- [ ] Actors come from authenticated data; the model never supplies
      identity or permissions.
- [ ] Every effect tool returns a durable receipt and is idempotent by
      identity plus native call id.
- [ ] Tool results are machine fields; user-visible text comes from i18n
      packs or the model's reply.
- [ ] The Task Manager reports only by publishing events.
- [ ] Deliverers wrap `contract.ErrDeliveryRejected` only for definite
      rejections; deliveries in `unknown` are reconciled by status query
      (`contract.DeliveryReconciler`, `Hub.Reconcile`), never resent.
- [ ] Locale packs for your custom types, sessions and tools exist in
      every shipped locale ([i18n.md](i18n.md)).
- [ ] Behaviour is covered offline with `kerneltest` models and fakes.

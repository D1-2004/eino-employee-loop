# Services and wire protocol

[中文](services.zh-CN.md)

The Employee Loop is one of three services. They may run in one process or
apart; every value crossing a boundary is a plain JSON type from
`contract`, carried over HTTP by `transport/httpapi`. JSON Schemas for every
body are in [schema/](../schema/).

```
Event Gateway ──Event──► Employee Loop ──DispatchRequest──► Task Manager
      ▲                       │  ▲                               │
      └──────Delivery─────────┘  └──────────Event (execution.*)──┘
```

- [Boundaries](#boundaries)
- [Endpoints](#endpoints)
- [Errors](#errors)
- [Authentication and limits](#authentication-and-limits)
- [Idempotency on the wire](#idempotency-on-the-wire)
- [Task Manager event sequence](#task-manager-event-sequence)
- [Deployment topologies](#deployment-topologies)
- [Commands](#commands)

## Boundaries

| Service | Owns | Exposes | Calls |
| --- | --- | --- | --- |
| Event Gateway | provider connections, scene resolution, provider message ids | `contract.Deliverer` (`POST /v1/deliveries`), `contract.DeliveryReconciler` (`POST /v1/deliveries/status`) | loop `EventSink` |
| Employee Loop | admission, sessions, windows, wakes, journal, outbox, memory, conversation | `contract.EventSink` (`POST /v1/events`) | `TaskManager`, `Deliverer` |
| Task Manager | tasks, runs, executors, its event outbox | `contract.TaskManager` (`/v1/tasks...`) | loop `EventSink` |

Rules that keep the boundaries clean (enforced by
`internal/archtest`, as allow-lists: every package must match a rule, so a
new top-level package is not importable by the loop until a rule says so):

- `contract` imports nothing of the module; `i18n` imports only
  `contract`.
- Loop packages (`kernel`, `scene`, `toolhost`, `store`, `employeeloop`)
  import only each other, `contract` and `i18n`.
- Transports import only `contract`; gateways only `contract` and `i18n`;
  task managers only `contract`, `i18n` and `taskmgr`.
- Each package that speaks owns its i18n pack; the loop's default bundle
  holds only loop text (see [i18n.md](i18n.md)).
- The Task Manager reports **only** by publishing events to the loop. The
  loop never polls it for results; it reads task state (`Get`, `Find`) only
  when a tool asks.
- Every Task Manager event carries `employee`, so one Task Manager serves
  many loops: `contract.SinkResolver` routes an event to its employee's
  loop, and a loop rejects another employee's event (`unknown_employee`).

## Endpoints

| Method and path | Request | Success | Service |
| --- | --- | --- | --- |
| `POST /v1/events` | [Event](../schema/event.schema.json) | `202` [EventAck](../schema/event-ack.schema.json) | Loop |
| `POST /v1/tasks` | [DispatchRequest](../schema/dispatch-request.schema.json) | `200` [TaskReceipt](../schema/task-receipt.schema.json) | Task Manager |
| `GET /v1/tasks/{ref}?employee=` | - | `200` [TaskView](../schema/task-view.schema.json) | Task Manager |
| `GET /v1/tasks?employee=&scene=&requester=&text=&limit=` | - | `200` [TaskList](../schema/task-list.schema.json) | Task Manager |
| `POST /v1/tasks/{ref}/steer` | [TaskControl](../schema/task-control.schema.json) (`text` = correction) | `200` TaskReceipt | Task Manager |
| `POST /v1/tasks/{ref}/stop` | TaskControl | `200` TaskReceipt | Task Manager |
| `POST /v1/tasks/{ref}/continue` | TaskControl (`text` = new prompt, optional) | `200` TaskReceipt | Task Manager |
| `POST /v1/deliveries` | [Delivery](../schema/delivery.schema.json) | `200` [DeliveryAck](../schema/delivery-ack.schema.json); `422 delivery_rejected` when the gateway did not send | Gateway |
| `POST /v1/deliveries/status` | Delivery | `200` [DeliveryStatus](../schema/delivery-status.schema.json); never sends | Gateway |
| `GET /healthz` | - | `200 {"status":"ok"}` | all |

Details:

- `EventAck.lane` reports where the event went; `window_id` is set for
  session lanes; `duplicate: true` for a replay.
- `Event.employee` addresses the loop. Empty means "the loop that receives
  it"; a loop answers `404 unknown_employee` for another employee's event.
- On control endpoints an empty `task_ref` in the body is filled from the
  path; a different one is `400 invalid_request`.
- `GET /v1/tasks` requires `employee` and `scene`; `limit` must be a
  non-negative integer (memtask defaults to 20 and caps at 200). The list
  is newest first and never `null`.
- Task Manager events carry `payload_schema: "taskmgr.execution/v1"` and an
  [ExecutionPayload](../schema/execution-payload.schema.json).

Each handler (`httpapi.NewEventHandler`, `NewTaskManagerHandler`,
`NewDeliveryHandler`) wraps the contract interface, and each client
(`NewEventSinkClient`, `NewTaskManagerClient`, `NewDelivererClient`)
implements it, so a service can switch between in-process and remote peers
without code changes.

## Errors

Errors are `{"code": "...", "message": "..."}`
([Error](../schema/error.schema.json)). Each code maps to one sentinel, so
`errors.Is` works across the wire (`httpapi.ErrorCodes`):

| Code | HTTP | Sentinel | Typical cause |
| --- | --- | --- | --- |
| `invalid_event` | 400 | `contract.ErrInvalidEvent` | missing envelope field, class/mode mismatch, foreground without a human actor, missing session ref |
| `unknown_type` | 400 | `contract.ErrUnknownType` | type not in the loop's catalog |
| `unknown_scene` | 400 | `contract.ErrUnknownScene` | type requires a scene and `scene.id` is empty |
| `unknown_employee` | 404 | `contract.ErrUnknownEmployee` | the event is addressed to an employee this sink does not serve |
| `invalid_request` | 400 | `contract.ErrInvalidRequest` | missing Task Manager field, bad `limit`, path/body ref mismatch; the loop returns it to the model as the refusal `invalid_request` |
| `delivery_rejected` | 422 | `contract.ErrDeliveryRejected` | the gateway definitely did not send the delivery (no locator, foreign scene) |
| `bad_request` | 400 | `httpapi.ErrBadRequest` | malformed JSON |
| `unauthorized` | 401 | `httpapi.ErrUnauthorized` | missing or wrong bearer token |
| `forbidden` | 403 | `contract.ErrForbidden` | control by someone other than the requester, or from another scene |
| `not_found` | 404 | `contract.ErrNotFound` | unknown task (or another employee's) |
| `conflict` | 409 | `contract.ErrConflict` | idempotency key or event id reused with other content |
| `task_state` | 409 | `contract.ErrTaskState` | steer/stop on a finished task, continue on an active one |
| `stale_revision` | 409 | `contract.ErrStaleRevision` | `expected_revision` is not the task's current revision (checked after the idempotency key) |
| `lease` | 409 | `contract.ErrLease` | stale lease |
| `too_large` | 413 | `httpapi.ErrTooLarge` | body above the limit |
| `quota` | 429 | `contract.ErrQuota` | provider or memory quota |
| `unavailable` | 503 | `contract.ErrUnavailable` | Task Manager closed or shutting down; the loop retries the window |
| `internal` | 500 | `httpapi.ErrRemote` | anything else |

Retry guidance for clients: `400`, `401`, `403`, `404`, `409`, `413` and
`422` cannot succeed unchanged; `429`, `503`, `500` and transport errors may
be retried with backoff **with the same idempotency key or event id**.
Deliveries are the exception: they are never retried. `DelivererClient`
reports `delivery_rejected`, `bad_request`, `unauthorized` and `too_large`
as definite rejections (`contract.ErrDeliveryRejected`, the delivery
becomes `failed`); every other failure leaves the delivery `unknown` until
a status query settles it.
`memtask` treats `invalid_event`, `unknown_type`, `unknown_scene`,
`unknown_employee` and `conflict` from the loop as permanent and skips the
event;
`gateway/dws.Permanent` does the same for the gateway.

## Authentication and limits

- An optional shared bearer token (`httpapi.WithToken`, `-token` or
  `EEL_TOKEN` in the commands) protects every endpoint except `/healthz`.
  Handlers compare in constant time and answer `401` with
  `WWW-Authenticate: Bearer`.
- Request bodies are limited to 1 MiB by default
  (`httpapi.WithMaxBody`); clients read at most 16 MiB of a response.
- Identity inside bodies (`actor`, `requester`) is data the caller vouches
  for. Run the services on a private network or behind a gateway that
  authenticates callers; the token is a shared secret, not per-caller
  authorization.

## Idempotency on the wire

| Call | Key | Replay |
| --- | --- | --- |
| `POST /v1/events` | `(source, id)` per employee | same content: `202` with the original receipt and `duplicate: true`; other content: `409 conflict` |
| `POST /v1/tasks` | `(employee, idempotency_key)` | same request: the stored receipt with `created: false`; other request: `409 conflict` |
| control endpoints | `(employee, idempotency_key)` | same control: stored receipt; other control: `409 conflict` |
| `POST /v1/deliveries` | `Delivery.id` | the gateway must not resend a delivery whose outcome is unknown; `DelivererClient` never retries |
| `POST /v1/deliveries/status` | `Delivery.id` | read only: the loop asks about `unknown` deliveries (`Hub.Reconcile`); `dws.Deliverer` answers from the deliveries it sent since start, otherwise `unknown` |

The loop derives Task Manager keys as
`<window id>/<model turn>/<native call id>`, so a replayed wake never
creates a second task.

## Task Manager event sequence

```
Dispatch / Steer / Continue ──► execution.accepted   (host lane, per run)
run starts                  ──► execution.started    (host lane)
Reporter.Progress           ──► execution.progress   (follow_up session, seq 1..n)
Steer                       ──► execution.terminal (superseded), execution.steered, execution.accepted
run done or failed          ──► execution.result     (follow_up session; text = report or failure)
every run end               ──► execution.terminal   (host lane)
Stop                        ──► execution.terminal   (cancelled, reason stopped)
```

Every event has source `task-manager`, the task's `employee`, a `system`
actor, refs `task` and `run`, the task's scene and locale, and id
`<task>/<run>/<kind>`. memtask
publishes through an ordered in-memory queue with capped exponential
backoff; events not yet published are lost if the process exits (a durable
Task Manager keeps them in `task_events`).

## Deployment topologies

| Topology | How | Notes |
| --- | --- | --- |
| All in one process | `go run ./cmd/employee-loop -stdin` | in-process memtask with the echo executor, stdin gateway; for local development |
| Loop with remote peers | `employee-loop -serve :8081 -tasks URL -deliver URL` | the Task Manager publishes back to `-serve` |
| Three processes | `employee-loop`, `task-manager`, `dws-gateway` | see [Commands](#commands) |
| One Task Manager, many loops | `task-manager -loops e1=URL1,e2=URL2` | events route by `employee` (`contract.SinkResolver`) |
| Kernel embedded in a platform | the platform calls `kernel.Run` or runs `scene.Hub` with its own ports | see [integration.md](integration.md) |

Scaling: `scene.Worker` is stateless and claims with leases, so several loop
replicas can share durable ports passed through `employeeloop.Config.Ports`
(reference DDL in [storage.md](storage.md); none ships). The in-memory
stores are single-process. Task
Managers scale independently; the loop only needs their HTTP endpoint.

## Commands

| Command | Flags |
| --- | --- |
| `cmd/employee-loop` | `-serve`, `-tasks`, `-deliver`, `-stdin`, `-persona`, `-speaker`, `-employee`, `-tenant`, `-locale`, `-token`, `-dotenv` |
| `cmd/task-manager` | `-serve` (default `:8082`), `-loop` and/or `-loops employee=URL,...` (one is required), `-delay`, `-locale`, `-token`, `-dotenv` |
| `cmd/dws-gateway` | `-cid`, `-kind`, `-profile`, `-self-id`, `-self-name`, `-transport`, `-loop`, `-serve` (default `:8083`), `-namespace`, `-tenant`, `-employee`, `-reactions`, `-locale`, `-token`, `-dotenv` |
| `cmd/catalog-doc` | `-locale`, `-o` |

Environment: `LLM_PROVIDER`, `LLM_MODEL`, `LLM_ACK_MODEL`, `LLM_BASE_URL`,
`LLM_API_KEY` (aliases `DEEPSEEK_*`, `DASHSCOPE_*`, `OPENAI_*`; see
`providers/llm`), `EMPLOYEE_LOCALE`, `EEL_TOKEN`, and for the DingTalk
gateway `DWS_CID`, `DWS_KIND`, `DWS_PROFILE`, `DWS_SELF_ID`,
`DWS_SELF_NAME`, `DWS_TRANSPORT`, `DWS_REACTIONS`, `EMPLOYEE_ID`.
`.env.example` lists them.

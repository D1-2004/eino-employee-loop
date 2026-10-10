# Architecture

[中文](architecture.zh-CN.md)

This document describes how the Employee Loop turns events into decisions:
how an event reaches a scene, which session processes it, and what makes
each step safe to retry. The code lives in `contract` (types and ports),
`scene` (Hub and Worker), `kernel` (one wake) and `store/memstore` (the
in-memory reference ports).

- [Model: events, scenes, sessions](#model-events-scenes-sessions)
- [Lanes](#lanes)
- [Lifecycle of an event](#lifecycle-of-an-event)
- [Claim order and concurrency](#claim-order-and-concurrency)
- [Preemption and revocation](#preemption-and-revocation)
- [Failure handling](#failure-handling)
- [Idempotency, leases and fencing](#idempotency-leases-and-fencing)
- [Journals: replaying a wake](#journals-replaying-a-wake)
- [Outbox and delivery states](#outbox-and-delivery-states)
- [Proactive group observation: compatibility notes](#proactive-group-observation-compatibility-notes)

## Model: events, scenes, sessions

Every input is a `contract.Event`. Its type selects a `TypeSpec` in the
catalog, which fixes the event's **class** (what happened: business,
execution, completion, schedule, enablement) and **mode** (why the employee
received it: triggered = passive, listening = active). See
[events.md](events.md).

Every event belongs to one **scene**: an employee x tenant x locus (a group,
a direct conversation, an enterprise, or a Host-defined kind). The scene id
(`SceneRef.ID`) is minted by the Host or the gateway and is the only key of
per-scene state; the provider's own identity travels as the opaque
`SceneRef.Locator`.

A scene owns sessions:

```
scene
├── interactive  (foreground)  exactly one per scene; people talking to the employee
├── observation  (background)  one per scene; messages nobody addressed to the employee
├── follow_up    (background)  one per task (ref "task"); progress and results of work in flight
├── routine      (background)  one per routine (ref "routine"); routine occurrences
└── lifecycle    (background)  one per scene; enablement events that need a decision
```

A session processes its events in **windows**. A window is one model wake:
a sealed, ordered list of admitted events (`contract.Window.Items`) that the
model sees together. Bursts of the same session coalesce into one window
(see [events.md](events.md#coalescing)).

## Lanes

| Lane | Sessions | Who enters | Wake |
| --- | --- | --- | --- |
| `foreground` | `interactive` | triggered business events from a human actor | yes; plain text may be the reply |
| `background` | `observation`, `follow_up`, `routine`, `lifecycle` (and Host-defined kinds) | listening events, work reports, routines, lifecycle | yes; only a terminal tool ends it |
| `host` | none | receipts, terminal facts, configuration revisions | no; recorded, then applied by `Hub.OnHostEvent` |

`TypeSpec.Validate` enforces the lane rules: only triggered business events
may use the foreground, the foreground always uses the interactive session,
background types need a background session kind, and host types have no
session. `Catalog.Route` additionally rejects a foreground event whose actor
is not `human`. Background wakes run with `RequireTerminal`: plain model
text is private and only a Host-accepted terminal decision (reply, quiet,
dispatched, waiting) ends them.

## Lifecycle of an event

```
producer ──Publish──► Hub.Admit
                        │ Catalog.Route        validate, classify, pick lane/session, compute cancellations
                        │ EventLog.Admit       receipt per (employee, source, event id); applied replay -> duplicate
                        ├─ host lane:          SessionQueue.Cancel(receipt, revocations) -> OnHostEvent
                        ├─ session lanes:      SessionQueue.Enqueue (coalesce + preemption, one transaction)
                        └ EventLog.MarkApplied every step above is idempotent per receipt
Worker ──StepOnce──► Hub.Step
                        │ SessionQueue.Claim   foreground first, lease + generation
                        │ Runner.RunWindow     KernelRunner: context, tools for lane/session, kernel.Run
                        └ SessionQueue.Complete(result, deliveries)  one commit fenced by the lease;
                                               reply text of the decision (never for quiet)
Worker ──DeliverOnce──► Hub.Deliver
                        │ Outbox.Pending -> Transition pending->unknown -> Deliverer.Deliver
                        └ Transition unknown->delivered | failed (definite rejection only)
Worker ──ReconcileOnce──► Hub.Reconcile (Deliverer is a DeliveryReconciler)
                        │ Outbox.Unknown -> DeliveryReconciler.DeliveryStatus (never sends)
                        └ Transition unknown->delivered | failed, or stays unknown
```

1. **Admit.** `Catalog.Route` validates the envelope, rejects unknown
   types (the loop never guesses a route), checks the producer's
   class/mode against the catalog, requires a scene when the type does,
   and returns an `Admission` with the session and the cancellations the
   event implies. `EventLog.Admit` stores one receipt per
   `(employee, source, event id)`. The receipt and its effect (step 2, or
   the host lane's cancellations and `OnHostEvent`) are not one
   transaction: the receipt is marked applied (`EventLog.MarkApplied`)
   only after the effect committed. A republished duplicate of an
   unapplied receipt re-runs the effect, and every step is idempotent per
   receipt (`Enqueue` returns the window that already holds the receipt,
   `Cancel` applies a receipt's cancellations once, `OnHostEvent` must be
   idempotent), so a failure or crash between the two never drops the
   event and never applies it twice.
2. **Enqueue.** A session-lane event joins the session's open window or
   starts a new one. Its cancellations are applied in the same transaction.
   When the event cannot join the open window (no coalescing, `max_items`
   reached or past `max_wait_ms`), the open window is sealed first, due no
   later than now, so it runs before its successor and never stays open.
3. **Claim.** A worker claims one due, ready window and receives a lease.
4. **Wake.** `KernelRunner` filters the tools by the window's lane and
   session, resolves their descriptions and the session role for the
   wake's locale (the last event's locale wins over the default), freezes
   the Host's context blocks and runs `kernel.Run`. With a journal, a
   re-claim reuses the first recorded model request instead of calling
   `Context` again, so input admitted after the first claim never changes
   a replayed wake.
5. **Complete.** Any decision with reply text except `quiet` becomes one
   delivery `<window id>/reply`: a reply, a dispatch acknowledgement or a
   question. Foreground deliveries carry `reply_to` = the last event id.
   `SessionQueue.Complete` records the outcome and inserts the deliveries
   in one commit fenced by the lease. If the window was cancelled
   meanwhile, the lease no longer matches and nothing is posted; if the
   commit fails, the window stays running, its lease expires and the
   journal replays it, so a reply is never lost behind a done window.
6. **Deliver.** The delivery loop sends pending deliveries through the
   channel's `Deliverer` and reconciles unknown ones by status query.

A task reports back as events: `dispatch_task` returns a receipt at once,
the Task Manager later publishes `execution.*` events, and the result opens
a `follow_up` window keyed by the task (see [services.md](services.md)).

## Claim order and concurrency

`SessionQueue.Claim` (memstore semantics, required of every
implementation):

- Every open window whose `due_at` passed is sealed (`open -> ready`);
  running windows whose lease expired return to `ready`.
- A candidate is `ready`, due, inside the requested lanes/sessions, and
  the oldest unfinished (`open`, `ready` or `running`) window of its
  session. Sessions are FIFO across retries: a window that waits for its
  retry backoff keeps the later windows of its session behind it, so
  messages are answered in the order they arrived, and at most one window
  per session runs. With `SerializeScene`, its scene has no running window
  at all.
- A background window waits while its scene has any open or ready
  foreground window, and a preemptible window also waits while its scene
  has a running foreground window: human input always goes first in its
  scene, and an observation admitted during a foreground wake runs only
  after it, whatever the number of workers.
- Foreground windows sort before background ones, then by `due_at`, then by
  id. `scene.Worker.StepOnce` asks for the foreground lane first and only
  then for any lane.
- Each claim increments `attempts` and issues a new lease
  `(window id, random token, generation = attempts, until)`. memstore
  uses a 90 s lease when `ClaimRequest.LeaseFor` is zero.

Workers hold no state: any number of `scene.Worker` step loops, in one
process or many, may serve the same ports.

## Preemption and revocation

**Preemption.** Types with `preempts` (`message.addressed`,
`message.follow_up`, `human.response`, `reaction.added`) cancel the scene's
windows of every session kind that some type marks `preemptible`
(today: `observation`). Cancellation covers `open`, `ready` and `running`
windows and happens in the same transaction as the admission. A running
window loses its lease: its later journal writes and its `Complete` fail
with `ErrLease`, and `Hub.Step` drops its decision, so a preempted
observation never speaks after a person addressed the employee.

**Revocation.** Types with `revokes` cancel every window of the named
session kinds, preemptible or not: `policy.changed` and
`subscription.changed` revoke `observation`; `employee.disabled` revokes
`observation` and `lifecycle`. Host-lane events apply their revocations in
`Hub.Admit` before `OnHostEvent`, once per receipt: a re-applied duplicate
does not revoke windows admitted after the first attempt. An event without
a scene id revokes across all scenes of the employee.

Cancelled windows record the reason (`foreground_admission` or `revoked`)
and are not retried. Their events stay in the event log.

## Failure handling

`Hub.Step` classifies the runner's error:

| Runner result | Window | Notes |
| --- | --- | --- |
| success | `done` | the reply is committed with the completion |
| success, but the reply's delivery id exists with other content | `held` with `delivery_conflict` | `Complete` committed nothing |
| `scene.HoldError{Reason}` | `held` with the reason | explicit stop, never retried (for example `empty_window`) |
| journal mismatch (`kernel.ErrJournal` + `contract.ErrConflict`) | `held` with `journal_conflict` | the re-claim no longer matches its record; the model is never re-asked |
| any other error | `ready` again after `RetryDelay` (default 1 s) | after `MaxAttempts` claims (default 3) the window is `held` with `attempts_exhausted` |
| context cancelled (shutdown) | unchanged | the lease expires and another worker replays the journal |

Inside a wake the kernel spends a `contract.Budget` (default: 3 responses,
3 consecutive errors, 3 provider attempts). Invalid output never enters the
transcript; the kernel appends a localized repair instruction instead. A
Host refusal (`contract.ErrToolRefused` without a receipt) costs budget but
lets the model correct the call; a failed effect fails the wake. See
[tools.md](tools.md).

## Idempotency, leases and fencing

| Step | Identity | Replay with same content | Replay with other content |
| --- | --- | --- | --- |
| Admission | `(employee, source, event id)` + `Event.Fingerprint()` | original receipt, `duplicate: true`; an unapplied receipt re-applies its effect | `ErrConflict` (HTTP 409 `conflict`) |
| Window item | receipt id | the window that holds it | - |
| Host-lane cancellations | receipt id | the recorded count, nothing cancelled | - |
| Window | one open window per session | joins it | - |
| Model turn | `(window, seq)` + request bytes | recorded response | `ErrConflict`; the wake stops |
| Tool call | `(window, model turn, native call id)` | recorded result and exact error text | `ErrConflict` if the tool name or arguments differ; the wake stops |
| Task Manager mutation | `(employee, idempotency key)`, key = `<window id>/<model turn>/<native call id>` | stored receipt | `ErrConflict` |
| Delivery | `<window id>/reply`, written by `Complete` | existing delivery | `ErrConflict` if text or scene differ; the completion commits nothing |
| Task Manager event | `<task>/<run>/<kind>` (source `task-manager`) | duplicate at admission | `ErrConflict` |

The fingerprint is the SHA-256 of the event's JSON without `class` and
`mode`, so a producer may omit or include them.

Every write after a claim is **fenced** by the lease: `Complete`, `Retry`,
`Hold` and every journal read or write check `(window id, token,
generation)` against the current lease and fail with `contract.ErrLease`
otherwise. A worker that lost its lease (expiry, preemption, revocation)
cannot complete the window, cannot record journal entries and cannot post
its reply, because the reply is part of the completion.

## Journals: replaying a wake

With `KernelRunner.Journal` set, each claim runs the wake through one
`kernel.WakeJournal` (`NewWakeJournal(journal, lease)`), which wraps the
model (`Model`) and the Host (`Host`):

- **Frozen input.** `FrozenTranscript` returns the request of model turn 0
  recorded by the first claim; `KernelRunner` passes it as the wake
  transcript and does not call `Context` again. Messages admitted to the
  scene between claims therefore never change a replayed wake.
- **Model turns** replay in order. A replayed turn must have byte-identical
  request JSON; otherwise the wake stops with `kernel.ErrJournal` +
  `contract.ErrConflict` and the Hub holds the window (`journal_conflict`).
  It never falls through to live model calls.
- **Tool calls** replay by `(model turn, native call id)`, since providers
  may reuse ids across turns, and must have the same tool name and
  arguments. A recorded error replays with its exact text; `ToolRecord.Refused`
  keeps `errors.Is(err, contract.ErrToolRefused)`.
- **What is recorded.** Successes, refusals and errors that carry a receipt
  are recorded at once. Provider failures and other tool errors are
  transient: they are recorded only when a later model turn is recorded,
  because the model has seen them and the replay must show them again. A
  wake that ends on a transient failure leaves it unrecorded, so the
  re-claim asks the model or executes the call again under the same
  idempotency key.
- Any journal read or write failure (`kernel.ErrJournal`) ends the wake at
  once.

A window re-claimed after a crash therefore repeats no provider request and
no effect it already committed; Hosts still deduplicate effects by identity
plus call id, as `contract.Host` requires.

## Outbox and delivery states

```
pending ──Hub.Deliver──► unknown ──send ok──────────────► delivered
                            │  └──definite rejection─────► failed
                            │     (other errors: stays unknown)
                            └──Hub.Reconcile (status query, never sends)
                                   ├─ delivered | provider_accepted
                                   ├─ failed (definitely not sent)
                                   └─ unknown (asked again later)
(suppressed: reserved for Hosts)
```

`Hub.Deliver` moves a delivery from `pending` to `unknown` **before** it
calls the `Deliverer`. A successful send moves it to `delivered` with the
provider id. Only a definite rejection, an error wrapping
`contract.ErrDeliveryRejected` (the channel did not send and never will),
moves it to `failed`. Every other error (transport failure, timeout, 5xx)
is ambiguous and leaves it `unknown`, as does a crash between the two
transitions. `Outbox.Pending` never returns an `unknown` delivery, so it is
never sent twice.

`Hub.Reconcile` settles `unknown` deliveries that are older than a minimum
age (`Outbox.Unknown`) by asking a `contract.DeliveryReconciler`; it never
sends. The answer `delivered` or `provider_accepted` records the provider
id, `failed` means the channel knows the message was not sent, and
`unknown` leaves the delivery for a later pass. `scene.Worker` runs it
every `ReconcileEvery` for deliveries unknown for `ReconcileAfter` when its
`Deliverer` also implements `DeliveryReconciler`. `httpapi.DelivererClient`
implements it over `POST /v1/deliveries/status`, and `dws.Deliverer`
answers from the deliveries it sent since start. `suppressed` is reserved
for Hosts that revoke a delivery before any send. Deliverers must not
retry a send whose outcome is unknown (`contract.Deliverer`,
`httpapi.DelivererClient`).

## Proactive group observation: compatibility notes

Observing group conversations is where an employee most easily becomes
noisy or stale. The contract keeps it safe with these rules; each note says
what the module enforces and what a Host must provide.

- **Scene-level attention is not a session lifetime.** Whether the
  employee listens to a scene is a subscription
  (`subscription.changed`, enablement) owned by the Host and the gateway,
  not an open session. The observation session holds no state between
  windows; closing a subscription revokes its windows.
- **Observation windows are preemptible and stateless.** A person
  addressing the employee cancels pending and running observation windows
  in the same transaction as the admission. Nothing is resumed: the next
  observation window re-freezes its context from the scene at wake time
  (in `employeeloop`, the conversation log records observed human messages
  and every wake gets a fresh history block). The module does not
  re-enqueue the events of a cancelled window; a Host that wants them
  reviewed again publishes `observation.due` or new events.
- **Observation never runs beside a foreground wake.** Preemption cancels
  the observation windows that exist when a person addresses the employee;
  an observation admitted while the foreground wake runs is not claimed
  until that wake is done (`Claim` holds preemptible windows back while
  their scene has a running foreground window). It then runs with a fresh
  history. The foreground reply enters the conversation log when it is
  delivered; observation windows coalesce for seconds, so the reply is
  normally recorded first, and a Host that needs a hard guarantee lets its
  observation Host refuse to speak while the scene has a pending delivery.
- **At most one active observation window per scene.** The observation
  session is one per scene; coalescing keeps at most one open window per
  session, and claiming runs only the oldest unfinished window of a
  session.
  Types without a coalesce rule (`member.changed`, `resource.changed`,
  `observation.due`) may queue further ready windows behind it; they run
  one after another.
- **Separate decision and speech budgets.** `KernelRunner.Budgets` bounds
  each wake per lane (decision budget). How often the employee may speak
  in a scene (speech budget) is a Host policy: count deliveries per scene
  (for example with a Redis `INCR`) and have the observation Host
  refuse or force `stay_quiet` when the budget is spent. The module ships
  no speech budget.
- **Unknown deliveries are reconciled, never resent.** See
  [Outbox and delivery states](#outbox-and-delivery-states).
- **Versioned configuration snapshots revoke windows.** A Host freezes the
  configuration revision a window runs with and publishes
  `policy.changed` (scene) or `subscription.changed` when it changes; their
  `revokes` cancel observation windows built on the old revision, including
  a running one, whose late result is dropped by fencing. Carrying the
  revision (for example in `payload` or `refs`) is up to the Host.

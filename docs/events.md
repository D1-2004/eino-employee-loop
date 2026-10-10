# Events

[中文](events.zh-CN.md)

Every input of the Employee Loop is one `contract.Event`. This document
explains how events are classified and routed. The full list of standard
types, generated from the catalog, is in [catalog.md](catalog.md); the wire
format is [schema/event.schema.json](../schema/event.schema.json).

- [The envelope](#the-envelope)
- [Classification](#classification)
- [Routing invariants](#routing-invariants)
- [References](#references)
- [Coalescing](#coalescing)
- [Producers of the standard types](#producers-of-the-standard-types)
- [Host extension profile](#host-extension-profile)
- [Custom event types](#custom-event-types)

## The envelope

| Field | Required | Meaning |
| --- | --- | --- |
| `id` | yes | Stable per producer. Replaying `(source, id)` must carry the same content. |
| `source` | yes | Producer namespace, for example `dingtalk.im`, `local.stdin`, `task-manager`. |
| `employee` | no | The employee whose loop receives the event. Empty means the receiving loop; a loop rejects another employee's event (`unknown_employee`). Producers serving several employees (a shared Task Manager) set it so `contract.SinkResolver` can route the event. |
| `type` | yes | Selects the `TypeSpec`, for example `message.addressed`. |
| `class`, `mode` | no | Filled from the catalog on admission; a producer value that disagrees is rejected. |
| `scene` | per type | `SceneRef{id, kind, tenant, provider, locator}`. `id` is the only state key; `locator` is the opaque provider identity. |
| `actor` | per type | `Actor{id, kind, display_name}` from authenticated data. `kind` is `human`, `employee`, `system` or `integration`. |
| `occurred_at` | yes | RFC 3339 time. |
| `locale` | no | BCP 47 tag of human content; it selects the wake's language. |
| `refs` | no | Correlation references (see [References](#references)). |
| `text` | no | Human-visible content. |
| `payload_schema`, `payload` | no | Producer-specific data and the name of its schema, for example `taskmgr.execution/v1`. |

Gateways must mint scene ids (for example `gateway/dws.HashSceneResolver`
hashes a namespace and the conversation id) and must never use the raw
provider conversation id as `scene.id`.

## Classification

Two independent questions classify every type.

**Class: what happened.**

| Class | Covers |
| --- | --- |
| `business` | Scene business: messages, reactions, answers, membership, provider resources (documents, calendars, approvals). |
| `execution` | Work in flight: accepted, started, progress, steered. Never claims that the work finished. |
| `completion` | Terminal facts: a run's result, a terminal state (and, in a platform's extension types, for example a finished plan step or a closed collection). |
| `schedule` | Time: routine occurrences, due observation windows, reminders and deadlines. |
| `enablement` | Capability and lifecycle: enabled, disabled, bound to a scene, subscription or policy changed. |

**Mode: why the employee received it.**

| Mode | Also called | Meaning |
| --- | --- | --- |
| `triggered` | passive | Addressed to the employee or reporting on its own work: an @-mention, a direct message, a reply to its message, an answer to its question, its task finishing. |
| `listening` | active | Received because the employee subscribed, observes or keeps time: unaddressed group messages, provider feeds, routines, observation windows. |

The standard catalog fills the grid like this (types per cell; see
[catalog.md](catalog.md) for every option):

| | triggered (passive) | listening (active) |
| --- | --- | --- |
| business | `message.addressed`, `message.follow_up`, `human.response`, `reaction.added` | `message.observed`, `member.changed`, `resource.changed` |
| execution | `execution.accepted`, `execution.started`, `execution.progress`, `execution.steered` | - |
| completion | `execution.result`, `execution.terminal` | - |
| schedule | - | `routine.due`, `observation.due` |
| enablement | `employee.enabled`, `employee.disabled`, `subscription.changed`, `policy.changed`, `scene.bound` | - |

## Routing invariants

`TypeSpec.Validate` and `Catalog.Route` enforce:

1. Only **triggered business** events enter the **foreground**, always in
   the `interactive` session, and their actor must be `human`.
2. **Listening** events never enter the foreground.
3. Foreground windows are never preemptible.
4. **Host-lane** types have no session; foreground and background types
   always have one, and background types use a non-interactive kind.
5. Session lanes require a scene (`requires_scene: true`); only host-lane
   types may omit it.
6. A type with `session_key` partitions its session by that ref; an event
   without the ref is rejected.
7. Unknown types are rejected (`ErrUnknownType`): the loop never guesses a
   route. A producer's `class`/`mode` must match the catalog.
8. `coalesce` needs `max_items > 0` and `0 <= quiet_ms <= max_wait_ms`;
   every type has a `summary_key` (an i18n key).

Errors map to wire codes: `invalid_event`, `unknown_type`, `unknown_scene`
(HTTP 400), `unknown_employee` (HTTP 404), `conflict` (HTTP 409); see
[services.md](services.md#errors).

## References

`refs` correlates an event with other objects. Well-known keys
(`contract.Ref*`):

| Key | Used by | Meaning |
| --- | --- | --- |
| `task` | `execution.*` (and the extension types of task flows) | Task reference; session key of `follow_up`. |
| `run` | Task Manager events | Run reference; memtask uses `<task>.r<N>`. |
| `routine` | `routine.due` | Routine reference; session key of `routine`. |
| `reply_to` | business messages | Provider message the event answers. |
| `window` | Host events | A window the event refers to. |

Refs are data. Identity and permissions come from `actor` and the Host,
never from refs or text.

## Coalescing

A type with a `coalesce` rule lets bursts of one session share one wake.
memstore implements the rule as follows (durable stores must match):

- The first event opens a window with `due_at = now + quiet_ms`.
- A later event of the same session joins the open window while it holds
  fewer than `max_items` items and `now < opened_at + max_wait_ms`; then
  `due_at = min(now + quiet_ms, opened_at + max_wait_ms)`.
- The window seals (`ready`) when it reaches `max_items`, or at claim time
  once `due_at` passed.
- An event whose type has no rule (or `max_items <= 1`) always starts its
  own ready window, even if the session has an open one.

Standard rules: addressed and follow-up messages, execution progress and
results coalesce for 250 ms (at most 1 s, 16 items); observed group
messages for 12 s (at most 30 s, 20 items).

## Producers of the standard types

| Producer | Types |
| --- | --- |
| Event Gateway (`gateway/dws`, `gateway/stdin`) | `message.addressed`, `message.observed`, `reaction.added` |
| Host (platform) | `message.follow_up`, `human.response`, `member.changed`, `resource.changed`, schedule and enablement types |
| Task Manager (`taskmgr`, source `task-manager`) | `execution.accepted`, `execution.started`, `execution.progress`, `execution.steered`, `execution.result`, `execution.terminal` |

`gateway/dws` classifies a DingTalk message as `message.addressed` when it
is a direct message, mentions the employee (provider ids, the provider's
mention flag or `@Name` in the text) or replies to the employee's message;
other group messages become `message.observed`; a negative reaction to the
employee's message becomes `reaction.added`. `gateway/stdin` publishes every
input line as `message.addressed`.

## Host extension profile

Types for flows the module does not implement (plans, collections,
reminders, configuration switches) belong to the embedding platform, not to
the module: nothing in the module produces them, so neither the standard
catalog nor the loop's i18n bundle carries them. A platform registers its
types on its catalog (`Catalog.Register`), offers its tools through
`toolhost.WithCatalog` and its own `contract.Host`, and ships their texts in
its own pack (`i18n.Extend`). [`examples/hostext`](../examples/hostext)
is a sample profile that does exactly that: `plan.step_completed`,
`collection.ready`, `task.reminder` and the tools `continue_plan`,
`describe_capabilities`, `scene_config_get`, `create_collection` and
`set_scene_participation`, with its own `plan` ref (`hostext.RefPlan`) and
`hostext.*` keys.

## Custom event types

Register a type at assembly time:

```go
cat := contract.StandardCatalog()
err := cat.Register(contract.TypeSpec{
    Type: "ticket.assigned", Class: contract.ClassBusiness, Mode: contract.ModeTriggered,
    Lane: contract.LaneBackground, Session: "ticket", SessionKey: "ticket",
    RequiresScene: true, SummaryKey: "event.ticket.assigned",
})
```

- Pick the class and mode by the two questions above; the lane follows
  from the invariants.
- A new background session kind is any string; add `session.<kind>` and,
  for `KernelRunner`, `scene.session.<kind>` (the session role) to your
  i18n packs.
- Add the `summary_key` text to every locale you ship (see
  [i18n.md](i18n.md)).
- `Register` replaces an existing type of the same name; keep standard
  names for standard meanings.
- [schema/type-spec.schema.json](../schema/type-spec.schema.json)
  describes a `TypeSpec` for catalogs kept as data.

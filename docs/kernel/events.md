> This guide covers the reusable kernel/example. For the application and M1–M7 ownership, see [ARCHITECTURE.md](../../ARCHITECTURE.md).

# Events and scenes

[中文](events.zh-CN.md)

An employee is driven by **events in scenes**. A scene is where work happens —
a group chat, a direct conversation, an enterprise space — identified by a
Host-minted `scene.id`. An event is anything the employee may need to act on.
`event.Catalog.Route` decides, for every event, *where* it is handled; the loop
decides *what* to do.

## Classification

Each type answers two independent questions.

**Class — what happened**

| Class | Covers | In DingTalk terms |
| --- | --- | --- |
| `business` | Messages, reactions, answers, membership, provider resources | 钉钉场域事件 |
| `execution` | Work in flight: accepted, started, progress, steered | 执行事件 |
| `completion` | Terminal facts: a run's result, its terminal state | 完成事件 |
| `schedule` | Time: routine occurrences, observation windows, reminders | 定时事件 |
| `enablement` | Enabled, disabled, bound to a scene, subscription or policy changed | 启用事件 |

**Mode — why the employee received it**

| Mode | Meaning |
| --- | --- |
| `triggered` (passive) | Addressed to the employee, or about its own work: a mention, a direct message, a reply to it, an answer to its question, its task finishing. |
| `listening` (active) | Received because the employee subscribed, observes or keeps time: unaddressed group messages, provider feeds, routines, observation windows. |

## Lanes and sessions

| Lane | Sessions | Receives | Model wake |
| --- | --- | --- | --- |
| `foreground` | `interactive` — one per scene | triggered business events from people | yes, serialized; human input first |
| `background` | `observation` (one per scene, preemptible), `follow_up` (per task), `routine` (per routine), `lifecycle` | listening events, work reports, routines, lifecycle decisions | yes, queued; never takes the interactive turn |
| `host` | none | receipts, terminal facts, configuration revisions | no; the Host applies them |

Standard types (`kernel/event/events.json`):

| Type | Class | Mode | Lane / session |
| --- | --- | --- | --- |
| `message.addressed` | business | triggered | foreground / interactive |
| `message.follow_up` | business | triggered | foreground / interactive |
| `human.response` | business | triggered | foreground / interactive |
| `reaction.added` | business | triggered | foreground / interactive |
| `message.observed` | business | listening | background / observation |
| `member.changed` | business | listening | background / observation |
| `resource.changed` | business | listening | background / observation |
| `execution.accepted` | execution | triggered | host |
| `execution.started` | execution | triggered | host |
| `execution.progress` | execution | triggered | background / follow_up (key `task`) |
| `execution.steered` | execution | triggered | host |
| `execution.result` | completion | triggered | background / follow_up (key `task`) |
| `execution.terminal` | completion | triggered | host |
| `routine.due` | schedule | listening | background / routine (key `routine`) |
| `observation.due` | schedule | listening | background / observation |
| `employee.enabled` | enablement | triggered | host |
| `employee.disabled` | enablement | triggered | host (revokes observation, lifecycle) |
| `subscription.changed` | enablement | triggered | host (revokes observation) |
| `policy.changed` | enablement | triggered | host (revokes observation) |
| `scene.bound` | enablement | triggered | background / lifecycle |

## Routing invariants

`TypeSpec.Validate` and `Catalog.Route` enforce:

1. Only **triggered business** events enter the **foreground**, always in the
   `interactive` session, and their actor must be a human.
2. **Listening** events never enter the foreground.
3. Foreground windows are never preemptible.
4. **Host-lane** types have no session; other lanes always have one.
5. Session lanes require a scene; only employee-wide host events may omit it.
6. A type with `session_key` partitions its session by that reference; an
   event without it is rejected.
7. Unknown types are rejected (`ErrUnknownType`): a route is never guessed. A
   producer's `class`/`mode` must agree with the catalog.

## Preemption and revocation

`Admission.Cancel` lists what the Host must cancel **in the same transaction**
as the admission:

- types with `preempts` (people addressing the employee) cancel the scene's
  **preemptible** windows (`observation`) — an observation in progress never
  speaks after someone addressed the employee;
- types with `revokes` cancel every window of the listed kinds; without a scene
  they apply to all scenes of the employee.

Cancelled observation windows are not resumed: the next observation freezes its
context again. This matches proactive group observation — stateless,
preemptible windows with their own speaking budget, whose uncertain deliveries
are reconciled, never resent.

## Coalescing

A type with `coalesce` lets a burst share one wake: the window closes
`quiet_ms` after its last item, `max_wait_ms` after its first, or at
`max_items`. Addressed messages and work reports coalesce for 250 ms (at most
1 s, 16 items); observed group messages for 12 s (at most 30 s, 20 items).
These values are a default profile; a Host overrides them by registering the
type again ([host.md](host.md#1-admit-the-event)).

## Host types

Hosts register their own types on top of the standard catalog:

```go
c := event.Standard()
err := c.Register(event.TypeSpec{
	Type: "plan.step_completed", Class: event.Completion, Mode: event.Triggered,
	Lane: event.Background, Session: event.FollowUp, SessionKey: event.RefTask, RequiresScene: true,
	Summary: map[string]string{"en": "A step of a plan finished.", "zh-CN": "计划中的一步已完成。"},
})
```

`Summary` carries one line per locale; `Describe(locale)` falls back to the base
language and then to English.

<!-- Generated from contract/catalog/*.json and the i18n packs by `go run ./cmd/catalog-doc -locale en -o docs/catalog.md`. Do not edit by hand. -->

# Event and tool catalog

The catalogs that ship with the module. The standard catalogs are what the module implements end to end: the event types its gateways, Task Manager and loop produce or handle, and the tools package toolhost executes. Hosts may register more event types (contract.Catalog.Register) and offer more tools (contract.ToolSpec); see [events.md](events.md) and [tools.md](tools.md).

## Vocabulary

| Dimension | Value | Meaning |
| --- | --- | --- |
| Class | `business` | Business event |
| Class | `execution` | Execution event |
| Class | `completion` | Completion event |
| Class | `schedule` | Schedule event |
| Class | `enablement` | Enablement event |
| Mode | `triggered` | Triggered (passive) |
| Mode | `listening` | Listening (active) |
| Lane | `foreground` | Foreground |
| Lane | `background` | Background |
| Lane | `host` | Host only |
| Session | `interactive` | Interactive session |
| Session | `observation` | Observation session |
| Session | `follow_up` | Task follow-up session |
| Session | `routine` | Routine session |
| Session | `lifecycle` | Lifecycle session |

## Event types

Each type has exactly one class and one mode. The lane and session decide where the loop processes an admitted event; options control coalescing, preemption and revocation. 20 event types.

### Business event (`business`)

| Type | Mode | Lane | Session | Options | Summary |
| --- | --- | --- | --- | --- | --- |
| `human.response` | Triggered (passive) | Foreground | `interactive` | preempts observation | A person answered the employee's question or card. |
| `member.changed` | Listening (active) | Background | `observation` | preemptible | Someone joined or left the scene. |
| `message.addressed` | Triggered (passive) | Foreground | `interactive` | preempts observation; coalesce: quiet 250 ms, max wait 1000 ms, max 16 items | A person addressed the employee: a mention, a direct message or a reply to its message. |
| `message.follow_up` | Triggered (passive) | Foreground | `interactive` | preempts observation; coalesce: quiet 250 ms, max wait 1000 ms, max 16 items | The same person continued the conversation right after the employee's delivered reply, without addressing it again. |
| `message.observed` | Listening (active) | Background | `observation` | preemptible; coalesce: quiet 12000 ms, max wait 30000 ms, max 20 items | A group message nobody addressed to the employee. |
| `reaction.added` | Triggered (passive) | Foreground | `interactive` | preempts observation | A person reacted to the employee's message, for example to correct it. |
| `resource.changed` | Listening (active) | Background | `observation` | preemptible | A provider resource in the scene changed (document, calendar, approval). |

### Execution event (`execution`)

| Type | Mode | Lane | Session | Options | Summary |
| --- | --- | --- | --- | --- | --- |
| `execution.accepted` | Triggered (passive) | Host only | - | - | Background work was accepted and has a receipt. |
| `execution.progress` | Triggered (passive) | Background | `follow_up` | one session per ref `task`; coalesce: quiet 250 ms, max wait 1000 ms, max 16 items | Background work reported meaningful progress. |
| `execution.started` | Triggered (passive) | Host only | - | - | Background work started running. |
| `execution.steered` | Triggered (passive) | Host only | - | - | A correction was applied to background work in flight. |

### Completion event (`completion`)

| Type | Mode | Lane | Session | Options | Summary |
| --- | --- | --- | --- | --- | --- |
| `execution.result` | Triggered (passive) | Background | `follow_up` | one session per ref `task`; coalesce: quiet 250 ms, max wait 1000 ms, max 16 items | A finished run's result is ready for the employee to report. |
| `execution.terminal` | Triggered (passive) | Host only | - | - | Background work reached a terminal state (fact only). |

### Schedule event (`schedule`)

| Type | Mode | Lane | Session | Options | Summary |
| --- | --- | --- | --- | --- | --- |
| `observation.due` | Listening (active) | Background | `observation` | preemptible | An observation window is due for review. |
| `routine.due` | Listening (active) | Background | `routine` | one session per ref `routine` | A routine occurrence is due. |

### Enablement event (`enablement`)

| Type | Mode | Lane | Session | Options | Summary |
| --- | --- | --- | --- | --- | --- |
| `employee.disabled` | Triggered (passive) | Host only | - | revokes `observation`, `lifecycle`; scene optional | The employee was disabled. |
| `employee.enabled` | Triggered (passive) | Host only | - | scene optional | The employee was enabled. |
| `policy.changed` | Triggered (passive) | Host only | - | revokes `observation` | A scene policy or runtime configuration revision changed. |
| `scene.bound` | Triggered (passive) | Background | `lifecycle` | - | The employee was bound to a new scene. |
| `subscription.changed` | Triggered (passive) | Host only | - | revokes `observation`; scene optional | A listening subscription was opened or closed. |

## Standard tools

Tools package toolhost implements over the contract ports. A tool is offered only in the listed lanes and sessions; `name?` marks an optional parameter. Effect tools must return a durable receipt. 13 tools.

| Tool | Kind | Ends the wake with | Offered in | Ports | Parameters | Description |
| --- | --- | --- | --- | --- | --- | --- |
| `reply` | terminal | `reply` | `foreground`, `background` / any session | `outbox`, `conversation` | `source_ref`, `reply` | Reply directly with what you already know, then finish without creating work. |
| `stay_quiet` | terminal | `quiet` | `foreground`, `background` / any session | - | `reason?` | Finish without saying anything when nothing needs a response. |
| `ask_human` | effect | `waiting` | `foreground`, `background` / `interactive`, `follow_up` | `outbox`, `tasks` | `source_ref`, `question`, `options?` | Ask the person one brief question and wait for the answer. |
| `dispatch_task` | effect | `dispatched` | `foreground` / `interactive` | `tasks`, `journal`, `executor` | `source_ref`, `goal`, `prompt`, `reply?` | Create real background work. Returns an async receipt; the result arrives later as a completion event. |
| `find_tasks` | read | - | `foreground` / any session | `tasks` | `source_ref`, `query?` | Find earlier work recorded by the Host in this scene. |
| `read_task` | read | - | `foreground`, `background` / any session | `tasks` | `source_ref`, `task_ref` | Read the Host-recorded state and report of one task. |
| `continue_task` | effect | `dispatched` | `foreground` / `interactive` | `tasks`, `journal`, `executor` | `source_ref`, `read_ref`, `prompt`, `reply?` | Continue a finished task the requester explicitly wants to extend. |
| `steer_task` | effect | `dispatched` | `foreground` / `interactive` | `tasks`, `journal`, `executor` | `source_ref`, `correction`, `reply?`, `task_ref?` | Correct the requester's own task while it is running. |
| `stop_task` | effect; exclusive | `reply` | `foreground` / `interactive` | `tasks`, `journal`, `executor` | `source_ref`, `read_ref`, `reply` | Stop the requester's own task when they explicitly ask to stop it. |
| `memory_capture` | effect | - | `foreground` / `interactive` | `memory` | `source_ref`, `audience`, `type`, `subject`, `quote` | Record something the person explicitly asked you to remember; quote their own words. |
| `memory_lookup` | read | - | `foreground`, `background` / any session | `memory` | `source_ref`, `scope`, `query` | Look up memory when the snapshot lacks the fact. |
| `memory_forget` | effect | - | `foreground` / `interactive` | `memory` | `source_ref`, `record_ref` | Archive one memory record the person asks to remove. |
| `read_context` | read | - | `foreground`, `background` / any session | `conversation` | `source_ref`, `section` | Read older context already frozen for this wake. |

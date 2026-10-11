# What a Host implements

[中文](host.zh-CN.md)

The loop decides; the Host does everything with side effects. This page lists
the Host's duties in the order a wake meets them.

## 1. Admit the event

Turn a provider input into an `event.Event` from **authenticated** data (actor,
scene, tenant come from the Host, never from message text), then
`catalog.Route(e)`:

- store one receipt per `(employee, source, id)` — a replay with the same
  `Fingerprint()` is a duplicate, another fingerprint is a conflict
  (`event_receipts`);
- enqueue the event into its session's open window, coalescing by the type's
  rule (`session_windows`);
- apply `Admission.Cancel` in the same transaction;
- host-lane events: apply the fact (record a receipt, close a task) without a
  wake.

## 2. Run windows

A window is one wake of one session. Claim windows so that a session's windows
run in order, the scene's foreground window goes first, and a preemptible window
waits while its scene's foreground runs. Hold a lease while running.

## 3. Assemble the wake

Freeze the messages of the wake **at the first claim**: system prompt (persona,
rules), context the Host chose (memory, recent conversation, task state —
labelled as data, never as instructions), and the window's events. A re-claim
reuses the frozen messages (`session_windows.wake`) instead of rebuilding
them, so a replay sends identical requests.

## 4. Offer tools

`loop.Tool` describes one capability: `Schema` (arguments, validated before any
call), `Effect` (must return a receipt), `Terminal` (the disposition it always
ends with), `Exclusive` (alone in its batch), `Timeout`. A typical employee
offers:

| Tool | Kind | Purpose |
| --- | --- | --- |
| `reply` | terminal `reply`, effect | Answer the current message. |
| `stay_quiet` | terminal `quiet` | Finish without speaking. |
| `ask_human` | terminal `waiting`, effect | Ask a person and wait for `human.response`. |
| `dispatch_task` | effect (returns `dispatched`) | Start background work; the result comes back as `execution.*` events. |
| `find_tasks`, `read_task` | read | Look up work the employee owns. |
| `steer_task`, `stop_task`, `continue_task` | effect, exclusive | Control work in flight. |
| `memory_capture`, `memory_lookup` | effect / read | Remember facts with provenance. |
| `read_context` | read | Read older conversation or resources on demand. |

`TurnTools` narrows the tools of each model call (for example, drop a
first-turn-only tool after the first call); a call to a tool not offered is
rejected like an unknown tool.

## 5. Execute calls

`Host.Execute(ctx, identity, call)`:

- check permissions and tenant fences with the **Host's** identity;
- deduplicate effects by `(window, turn, native call id)` and journal the
  result (`window_tool_records`); a committed effect returns its receipt even
  if `ctx` was cancelled right after the commit;
- refuse before any effect with an error wrapping `loop.ErrToolRefused`; the
  model sees the error text and may correct itself;
- return `Terminal` when the call ends the wake (a sent reply, an accepted
  dispatch with its acknowledgement).

`FinalTextHost.AcceptFinalText` optionally accepts a first plain answer as the
reply when the wake requires a terminal tool.

## 6. Make the model durable

Wrap the Eino model so every request/response is journaled
(`window_model_turns`) and a re-claim replays the recorded response for an
identical request instead of asking the provider again. Report provider
parameter rejections and exhausted reservations through `Config.Permanent`.

## 7. Deliver

Write user-visible replies into the outbox in the transaction that completes
the window (`outbox`). A send whose result is unknown (timeout) stays `unknown`
and is reconciled by a status query — never re-sent.

## Storage

[`schema/host.sql`](../schema/host.sql) is reference PostgreSQL DDL for the
tables above. It has no foreign keys (each table has its own retention) and
builds indexes `CONCURRENTLY`.

## Example: Multica

Multica embeds the loop as a selectable engine next to its built-in kernel. Both
take the same frozen configuration, durable model and Host, so switching engines
changes only the decision loop:

| Duty | Multica |
| --- | --- |
| Admission, windows, leases | `employee_scene_job` and its workers |
| Wake assembly | the built-in context builder (`WakeEntries`) |
| Tools, permissions, receipts | the EmployeeLoop Host and its tool journal |
| Durable model | the job's model journal |
| Delivery | the DingTalk response outbox |
| Turn policy, repair text | `TurnTools` / `RepairText` from the built-in kernel's rules |

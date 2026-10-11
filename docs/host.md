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

The coalescing timings in `event/events.json` (addressed messages 250 ms quiet,
1 s at most, 16 items; observed ones 12 s, 30 s, 20) are a default profile
tuned for a chat employee, not part of the contract. Override a type by
registering it again:

```go
c := event.Standard()
spec, _ := c.Lookup("message.addressed")
spec.Coalesce = &event.Coalesce{QuietMs: 250, MaxWaitMs: 250, MaxItems: 16} // a new value: Lookup shares the old one
err := c.Register(spec)
```

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

Dispositions are a closed set: `reply`, `quiet`, `dispatched`, `waiting`.
A result that decides `dispatched` needs a receipt (the accepted work), even
from a tool that is not an `Effect`; without one the wake fails with
`ErrMissingReceipt`.

`TurnTools` narrows the tools of each model call (for example, drop a
first-turn-only tool after the first call); a call to a tool not offered is
rejected like an unknown tool.

`PublicArgs` lists the string arguments that people will read, so
`Config.ValidateText` checks them before any call of the batch runs, as it
checks plain and final text. Unset, it checks an argument named `reply`, as
v0.2 did.

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

`FinalTextHost.AcceptFinalText` optionally accepts a plain answer as the reply
when the wake requires a terminal tool: by default only the first response,
before any tool ran. `Config.FinalTextAllowed` decides instead, from the call
number and the outcomes so far (for example, an answer after reads only); an
answer it allows is checked and accepted as the first one is.

`Config.Progress` decides which successful calls are progress. A batch with
progress resets the consecutive error count; a batch without adds one to it,
as a failed batch does. Unset, every successful call counts.

**The turn of a call.** `ToolCall` carries no turn number; derive it. A call's
turn is the `ModelCall.Attempt` of the response that carried it: keep the
latest attempt from `Config.OnModelCall` (or count `Generate` calls in the
durable model) in the wake's state and read it in `Execute`. The loop runs a
batch only after the attempt that returned it and before the next one, so the
latest attempt is always the right one. Attempts count from 1 in every `Run`,
and a re-claim that replays the model journal reproduces them. Native call ids
are unique within one response only, hence the turn in the key.

**Per-wake state.** A response passes, in this order: `OnModelCall`; the
loop's checks (call identity and arguments, offered tools, terminal and
exclusive rules, schemas, `PublicArgs` text); `ValidateBatch`; then either
`OnBatchRejected` and a repair, or `OnBatchAccepted`, which receives the batch
once before the first `Execute` of its calls. A Host whose policy hooks
(`TurnTools`, `ValidateBatch`, `FinalTextAllowed`, `Progress`) depend on what
the model did records accepted batches there, never rejected ones; accepted
final text is not a batch. Keep that state per `Run`: a re-claim that replays
the model journal starts empty and calls `OnBatchAccepted` again for every
batch it accepts.

## 6. Make the model durable

Wrap the Eino model so every request/response is journaled
(`window_model_turns`) and a re-claim replays the recorded response for an
identical request instead of asking the provider again. Report provider
parameter rejections and exhausted reservations through `Config.Permanent`.
The model receives the loop's own transcript messages, and v0.2 and v0.3 send
byte-identical requests (`loop/testdata/parity.golden.gz`), so a wake begun by
one version can be re-claimed by the other during a rolling deploy. The Host's
build list picks the Eino version under the loop, so on every eino or
eino-employee-loop bump run, from the Host module,
`go test -run TestParityWithV021 github.com/D1-2004/eino-employee-loop/loop`.

The model the Host passes to `loop.New` must:

- send **one provider request per `Generate`** (one attempt) and never retry
  inside: turn the SDK's retries off. The loop counts every attempt against
  `MaxPhysicalAttempts` and asks again itself, with a repair instruction;
- take its tools from `WithTools`, which the loop calls right before every
  attempt with that attempt's tools (all of them, or what `TurnTools` offers).
  Options never carry tools. `ToolInfo.Extra[loop.ExtraParameters]` holds each
  tool's JSON Schema as given. An attempt with no tools uses the unbound model;
- force a tool call when the attempt passes
  `model.WithToolChoice(schema.ToolChoiceForced)` (`RequireNativeToolCall`;
  OpenAI `tool_choice: "required"`). The loop passes no other option;
- return the provider's message with its own call ids (non-blank, unique in
  the response), type `function`, JSON object arguments and `finish_reason`
  `tool_calls`, `stop` or empty (anything else is repaired). Usage in
  `ResponseMeta.Usage` is summed. `Stream` is never called.

## 7. Deliver

Write user-visible replies into the outbox in the transaction that completes
the window (`outbox`). A send whose result is unknown (timeout) stays `unknown`
and is reconciled by a status query — never re-sent. Keep `Outcome.Receipts`
and `ToolOutcomes` when `Run` fails too: they may hold committed effects.

## Classify failures

Match `Run`'s error and the errors the hooks receive with `errors.Is`. The
texts are the v0.2 texts but carry details; they are not an API.

| Sentinel | Meaning | Then |
| --- | --- | --- |
| `ErrIncompleteResponse`, `ErrInvalidToolCall`, `ErrInvalidArguments`, `ErrUnknownTool`, `ErrExclusiveTool`, `ErrTerminalConflict`, `ErrTerminalRequired`, `ErrFinalTextRejected` | a response was rejected before any call ran | repaired; the last one is joined into the budget error |
| `ErrModelBudget` with `ErrConsecutiveModelErrors`, `ErrModelResponseBudget` or `ErrPhysicalModelBudget` | the budget ran out | the wake ends |
| `ErrToolRefused` (the Host's) | refused before any effect | the model may correct it |
| `ErrToolFailed`, `ErrMissingReceipt`, `ErrTerminalConflict`, `ErrNoTerminalDecision` | an effect failed or has no proof, or the results disagree | the wake ends after the batch |

Errors from the Host's own hooks (`ValidateBatch`, `ValidateText`, provider
errors) pass through unchanged: classify them with the Host's sentinels.
`ErrMissingReceipt` always comes wrapped in `ErrToolFailed`.

Three names come from Multica, where the loop began, and stay so that what
v0.2 Hosts stored keeps matching: the `employee loop:` prefix of the texts,
`FinalTextOrigin` (`host_final_text`) and `ExtraParameters`
(`employee_loop.parameters`).

## Check conformance

`loop/hosttest` runs this contract from the Host's own tests, against its real
Host and model adapter over fakes:

- `Target.Effect`: a receipt, also when ctx is cancelled right after the
  commit (the fake transport calls `hosttest.Committed`), and a replay of the
  same call returns it without a second effect;
- `Target.Refused`: `ErrToolRefused`, nothing committed;
- `Target.FinalText`: commits once;
- `Target.Wake`: a scripted wake decides as expected, every successful effect
  call commits, and a re-claim sends the same requests and commits nothing;
- `Target.ReusedID`: two responses that reuse one native id commit two
  effects (§5). `Wake` runs a scripted model, so take the turn from
  `OnModelCall`;
- `Model`: one request per `Generate`, the bound, narrowed and unbound tools,
  forcing, the response shape, the provider's ids and, if the fake reports
  it, usage.

What happens after `Run` returns (keeping the outcome, delivery) stays with
the Host's own tests. The `local` package is the reference: its tests run
every check but `ReusedID`, as its only effect ends the wake.

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

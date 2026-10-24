> This guide covers the reusable kernel/example. For the application and M1–M7 ownership, see [ARCHITECTURE.md](../../ARCHITECTURE.md).

# Integrating the loop

Code examples use the explicit `loop` import alias for package `decision`:

```go
import loop "github.com/D1-2004/eino-employee-loop/kernel/decision"
```


[中文](integration.zh-CN.md)

For teams that run the loop inside their own platform: what a Host must
implement, what state and services it needs, which inputs drive it, how to
check it, how to swap it in next to an existing engine and how to evaluate it.
The decision contract retains the v0.3 behavior. Import it from
`kernel/decision` and pin the revision validated by your Host; earlier revisions
use the previous source layout. The contracts it builds on are in [host.md](host.md),
[events.md](events.md) and [local.md](local.md) and are not repeated here.

**Terms.** A *Host* is the platform that runs an employee: it admits events,
assembles context, executes tools, stores state and delivers messages. A
*wake* is one decision: the Host hands the loop the inputs of one session
window and the loop runs until it reaches a terminal decision (`reply`,
`quiet`, `dispatched`, `waiting`) or fails. To *freeze* is to fix a wake's
configuration, messages and engine once, when the wake is first claimed; a
re-claim reads the frozen copy. A *journal* is the durable record of a wake's
model requests and responses and of its tool results; a re-claim replays it
instead of asking the provider or running an effect again. A *replica marker*
is a versioned string such as `<name>:<N>` that every server replica
advertises (for example in its heartbeat row); the Host compares the markers
of all live replicas and enables a new rule only when every one of them
advertises it. Bumping a marker means raising `N`.

## 1. What the module is

| It is | It is not |
| --- | --- |
| A Go library: `loop.New(cfg, model, host).Run(ctx, wake)` decides one wake | A service, queue, worker or scheduler |
| The model ↔ tools loop of one wake: batch validation, receipts, terminal decisions, budgets and repairs | A tool implementation: permissions, effects and receipts belong to the Host |
| A declarative event catalog (`event`) that classifies and routes inputs | An admission store: deduplication, windows, leases and cancellation belong to the Host |
| An engine that can replace another one that takes the same frozen inputs | A prompt builder: the Host puts persona, history and memory into `Wake.Messages` |
| A judge of complete responses: one `Generate` per provider attempt | A model gateway or a streamer: the Host's model owns timeouts, the journal and any streaming |

## 2. Boundary and state

The loop decides one wake; the Host does everything with side effects
([kernel overview](README.md), [host.md](host.md)).

**Minimum Host.** These are the pieces you implement or supply:

| Piece | Defined in |
| --- | --- |
| Admission: build an `event.Event` and route it with `event.Catalog.Route` (`event.Standard()` plus your registered types) | [events.md](events.md), [host.md §1](host.md#1-admit-the-event) |
| A `loop.Wake{Identity, Messages}`; `Identity{Tenant, Employee, Scene, Receipt}` comes from trusted admission data, never from message text | [host.md §3](host.md#3-assemble-the-wake) |
| The `[]loop.Tool` of the wake and a new `loop.Config` for every `Run` | [host.md §4](host.md#4-offer-tools) |
| `loop.Host.Execute(ctx, Identity, ToolCall) (ToolResult, error)` | [host.md §5](host.md#5-execute-calls) |
| Optional: `loop.FinalTextHost.AcceptFinalText(ctx, Identity, text)` | [host.md §5](host.md#5-execute-calls) |
| A durable Eino `model.ToolCallingChatModel`: one provider request per `Generate`, SDK retries off | [host.md §6](host.md#6-make-the-model-durable) |

Host policy plugs in as functions of frozen data and the state they are
handed:

| Hook | Signature | Use |
| --- | --- | --- |
| `TurnTools` | `func(call int, outcomes []ToolOutcome) []string` | Narrow the tools of a provider attempt |
| `ValidateArguments` | `func(Tool, map[string]any) []string` | Your argument rules instead of the built-in schema subset; problems reject the batch as `ErrInvalidArguments` |
| `ValidateBatch` | `func(call int, outcomes []ToolOutcome, calls []ToolCall) error` | Your batch rules; the last check of a batch and a pure one |
| `ValidateText` | `func(string) error` | Public text: plain replies, final text and each tool's `PublicArgs` |
| `ValidateDecision` | `func(Decision) error` | Which terminal decisions your Host results may carry; a refused one fails its call and then the wake (final text: at once) |
| `FinalTextAllowed` | `func(call int, outcomes []ToolOutcome) bool` | Which plain answers may end the wake |
| `Progress` | `func(ToolCall) bool` | Which successful calls reset the error streak |
| `RepairText` | `func(call int, outcomes []ToolOutcome, err error) string` | The instruction appended after a rejected response |
| `Permanent` | `func(error) bool` | Provider errors that end the wake at once |

`call` is the provider attempt (from 1, the `ModelCall.Attempt`); `outcomes`
are the Host results so far, in call order, read-only. Observers
(`OnModelCall`, `OnToolCall`, `OnBatchAccepted`, `OnBatchRejected`,
`OnModelFailure`) never change the wake and must not block. If your rules need
more than `call` and `outcomes`, record accepted batches in `OnBatchAccepted`
(never rejected ones) and build a new `Config` for every `Run`. Keep business
rules in the Host and feed them through these hooks; the module has no
business vocabulary.

### State

The loop keeps nothing between runs. During one `Run` it holds the transcript,
the budget counters and the current batch in memory; a re-claim starts empty
and replays the journal. Everything durable belongs to the Host;
[`docs/reference/host.sql`](../reference/host.sql) has reference tables.

| State | Why | Reference | Without it |
| --- | --- | --- | --- |
| Event receipts per `(employee, source, id)` with a fingerprint | Channels deliver at least once | `event_receipts` | Answers twice |
| Scene directory | One stable scene id per employee, tenant and place | the Host's own | Cross-tenant leaks |
| Windows, leases, attempts | Coalescing, one wake per session at a time, re-claim after a crash | `session_windows` | Concurrent or out-of-order replies |
| Frozen wake and configuration (and engine) | A re-claim sends identical requests | `session_windows.wake` | Journal conflicts |
| Model journal | A re-claim does not ask the provider again | `window_model_turns` | Double cost, a different answer |
| Model attempt reservations | A crash or re-claim cannot multiply provider requests | Insert the `window_model_turns` row (response NULL) before sending; cap rows per window | Double cost, budget bypass |
| Tool records keyed by `(window, turn, native id)` | Each effect runs once | `window_tool_records` | Duplicate sends or dispatches |
| Outcome | A failed wake may hold committed effects | `session_windows.outcome` | Lost effects |
| Outbox: `pending`, `unknown`, `delivered`, `failed` | An unknown send is reconciled, never re-sent | `outbox` | Duplicate or lost messages |
| History, memory, people | Context of the next wake | the Host's own | No continuity |

### Dependencies

| Dependency | Required | Notes |
| --- | --- | --- |
| An OpenAI-compatible chat endpoint with native tool calls | Yes | `tool_choice: "required"` if you set `RequireNativeToolCall`; SDK retries off ([host.md §6](host.md#6-make-the-model-durable)) |
| Go 1.26.1 or later, CloudWeGo Eino | Yes | Versions in `go.mod`; your build list picks Eino |
| Storage for the state above | In production | `docs/reference/host.sql` is PostgreSQL, without foreign keys |
| A channel that receives and delivers messages | Yes | Host-owned |
| Langfuse | No | The Host (or `local`) writes traces; the loop only calls observers |

## 3. Conformance

Run [`kernel/decision/hosttest`](../../kernel/decision/hosttest/hosttest.go) from your Host's tests
against your real Host and model adapter, with fake transports (a fake
channel, an outbox in a test database, an `httptest` provider;
[host.md](host.md#check-conformance) has the contracts):

```go
target := hosttest.Target{Host: host, Identity: id, Effects: outbox.Len}
target.Effect(t, loop.ToolCall{NativeToolCallID: "c1", Name: "reply", Arguments: args})
target.Refused(t, loop.ToolCall{NativeToolCallID: "c2", Name: "reply", Arguments: unknownSource})
target.FinalText(t, "hello")
target.Wake(t, cfg, messages, steps, loop.Decision{Kind: loop.Reply, Reply: "hi"})
target.ReusedID(t, cfg, messages, note, last, loop.Decision{Kind: loop.Quiet})
hosttest.Model(t, adapter, provider)
```

- `Effects` reports how many effects your Host has committed so far (outbox
  rows, messages your fake channel sent).
- Have the fake transport call `hosttest.Committed(ctx)` right after each
  commit. Under `Effect` it cancels the context at that moment, which is how
  the check proves that a late cancel still returns the receipt; elsewhere it
  does nothing.
- `hosttest.Model` needs a `hosttest.Provider` with `Requests` (the requests
  received so far) and `FailNext` (fail the next request the way an SDK would
  retry, for example with HTTP 500); `Usage` is optional.
- `Effect`, `FinalText`, `Wake` and `ReusedID` need a Host state in which
  their calls have not run yet: give each check a fresh window or test
  database.

| Capability | `hosttest` |
| --- | --- |
| Effect tools: a receipt, also when `ctx` is cancelled right after the commit | `Effect` |
| Idempotent effects keyed by `(window, turn, native call id)`; two responses may reuse `call_0` | `Effect`, `ReusedID` |
| Refusals before any effect (`loop.ErrToolRefused`) | `Refused` |
| Final text, committed once | `FinalText` |
| Terminal tools, decisions, a re-claim that sends the same requests and commits nothing | `Wake` |
| Durable model: one request per `Generate`, tools from `WithTools`, forcing, provider call ids | `Model` |

A production Host must pass every check, `ReusedID` included: a journal keyed
by the native id alone fails it, because providers reuse ids such as `call_0`
across responses. The `local` runner passes all but `ReusedID`, because its
only effect ends the wake.

**Final text.** Set `Config.AcceptFinalText` (with `RequireTerminalTool`) and
implement `FinalTextHost.AcceptFinalText`, which commits once. Without
`FinalTextAllowed`, only the first call, before any tool ran, may end with
plain text. With it, the hook decides for every call, so include the
first-call case yourself. `FinalTextLimit` (default 8000 bytes) applies to the
trimmed text.

Also required, checked by your own tests:

- Reserve each provider attempt durably before sending it (see State) and
  report an exhausted reservation through `Permanent`.
- Keep `Receipts` and `ToolOutcomes` when `Run` fails; deliver through the
  outbox.
- Error texts reach the model: refusals and tool errors enter the transcript
  and the next request, so redact hosts, credentials and internal ids.
- If a synchronous caller stopped waiting, deliver the reply through the normal
  channel; never leave it only in a slot nobody reads.
- Send each tool's schema from `ToolInfo.Extra[loop.ExtraParameters]`. This
  is required whenever a schema uses something Eino's typed copy
  (`ParamsOneOf`) cannot hold, such as draft-07 tuple `items` or a draft-04
  boolean `exclusiveMinimum`, for which it keeps only a bare object schema; it
  is recommended in all cases.
- Build `Tool.Schema` by decoding JSON, or supply `ValidateArguments`: the
  built-in check reads `enum` only as `[]any`, so an enum built in Go as
  `[]string` is shown to the model but enforced only after a JSON round trip.
- Classify errors with `errors.Is` and the exported sentinels, never by text.

Checks to run:

- In the module: `go test -race ./...`.
- After every Eino or module bump, the parity test from
  [host.md §6](host.md#6-make-the-model-durable), with `-v`: look for
  `--- PASS: TestParityWithV021`, since a version without the test prints "no
  tests to run" and still exits 0.
- With two engines: drive both from one script, freeze their request bytes,
  Host calls and outcomes in a golden file, and list the remaining differences
  in a test that fails when the list goes stale.

## 4. Drivers

Every input is an `event.Event`; its type fixes the class and mode
([events.md](events.md)). Map your inputs like this:

| Your input | Type | Session | Notes |
| --- | --- | --- | --- |
| A direct message, a mention, or a reply to the employee's message | `message.addressed` | interactive | The actor is a human, authenticated by the Host |
| The same person continues right after a delivered reply | `message.follow_up` | interactive | Decide from delivery receipts, not from text |
| An answer to the employee's question or card | `human.response` | interactive | |
| A reaction to the employee's message | `reaction.added` | interactive | |
| A group message nobody addressed to the employee | `message.observed` | observation | Needs a channel that delivers every group message; without one, report observation cases as not testable, never as passed |
| A member joined or left; a watched resource changed | `member.changed`, `resource.changed` | observation | Listening, preemptible |
| Background work accepted, started, steered or terminal | `execution.accepted`, `execution.started`, `execution.steered`, `execution.terminal` | host lane | Facts; no wake |
| Progress of background work | `execution.progress` | follow_up per task | The executor must emit it (below) |
| A finished run's result | `execution.result` | follow_up per task | A structured result (below) |
| A routine or an observation window is due | `routine.due`, `observation.due` | routine, observation | |
| The employee was enabled or disabled; a subscription or policy changed | `employee.enabled`, `employee.disabled`, `subscription.changed`, `policy.changed` | host lane | Disabling revokes observation and lifecycle windows; subscription or policy changes revoke observation |
| The employee was bound to a new scene | `scene.bound` | lifecycle | |
| Anything else | your own type | | Register it; unknown types are rejected |

**Background work.** The loop never infers progress from an executor's logs,
stdout or reasoning: the executor must call an explicit progress API, once per
stage, with an idempotent id (a retry sends the same content). Keep the
employee the only voice in the source conversation: the executor uploads files
and reports receipts, it does not send. Decide the executor contract
deliberately; each strict rule has a cost:

| Rule | Why | Cost | Prefer |
| --- | --- | --- | --- |
| The result is exactly one JSON object, nothing before or after it, allow-listed fields, no nulls | Control data comes only from one strict decode | A correct result with a sentence before the JSON is rejected: no summary, the goal stays open, attached files are dropped | A schema-checked "submit result" tool in the executor, or local validation with one rewrite; keep a diagnostic summary for near misses |
| Progress only through an explicit API call | No private reasoning leaks; the employee words what people see | A model that never calls it leaves long work silent | Also report structured stage events from the runner, and let the Host draft progress from them |
| The executor never posts to the source conversation | One voice, no duplicate replies | A prompt-only rule is broken occasionally | Enforce it in the executor's tooling, not only in the prompt |
| Executor credentials live as long as the run | Calls back to the Host keep working | A short-lived token makes long runs fail their final report | Renew the token, or issue it for the executor's lifetime |

## 5. Swapping engines

- **One prepared wake.** Prepare each wake once in the Host (frozen
  configuration, identity, initial messages, per-call rules) and hand every
  engine the same prepared wake, durable model and Host. Each engine returns
  the same outcome shape. Keep the per-call rules in one Host object that your
  existing engine calls directly and this loop calls through its hooks, so a
  rule is never written twice.
- **Choose and freeze.** Choose the engine per employee or template (an A/B
  test is then a setting, not a deploy) and freeze it into the wake snapshot at
  the first claim; replay a snapshot only on its frozen engine.
- **Fail closed.** If your existing engine has rules that act inside a batch
  while it runs, which no hook expresses, never freeze this loop for a
  configuration that uses them, and refuse such a wake in the adapter too.
- **Version the gate.** Let new snapshots choose this loop only when every
  live replica advertises the same replica marker. Raise the marker's number
  whenever a frozen snapshot would produce different requests or accept
  different responses, to a number no branch has used: one number must never
  name two rule sets.
- **Drain across versions.** Before a deploy or rollback that changes the
  marker: switch employees back to the existing engine, wait until no
  unfinished wake is frozen on this loop, deploy and check that every replica
  advertises the new marker, then switch back. Changing the setting alone needs
  no drain.
- **Streaming.** The loop judges complete responses only. To send an early
  frame (a first piece of feedback, say) stream inside your model: check and
  act on the frame there, return the complete message, and let the batch run
  the same native id again so `Execute` returns the recorded result
  ([host.md §6](host.md#6-make-the-model-durable)). If the loop rejects the
  full response, the early effect stays committed, so act early only on a call
  that passes the rules its batch will meet. Put that model decorator in the
  Host, so both engines stream the same way.

## 6. Evaluating a Host

The module ships no eval harness; cases and the harness are your own.

| Tier | What it shows | How |
| --- | --- | --- |
| Unit and conformance | The module and your Host keep the contract | §3 |
| Local, scripted | Deterministic decisions without an LLM | `examples/dingtalk -channel console -model-script …` ([local.md](local.md#local-debugging)) |
| Local, live | Chat behaviour with a real model and real chat | DingTalk only: the same cases through an account (the `dws` CLI) and through a DingTalk Stream Mode robot ([local.md](local.md)); on other platforms, run live cases through your own Host |
| A/B in the Host | This loop is no worse than your current engine | Both engines on the same cases, behind your real Host |

Know what each tier does not show. The local runner exercises the module, a
channel and the traces, not your Host's rules. A wake that calls the model
once runs the per-call hooks only with call 1 and no outcomes, so your rules
for later calls stay untested. Golden and parity tests prove equal requests
for scripted inputs, not equal behaviour with a live model. After any change
to your Host rules or the engine adapter, run the A/B again end to end.

Record one **wake record** per wake, all from `loop.Outcome`, the `Identity`
and the ids the Host put into the wake:

```json
{
  "wake_id": "w-17", "scene": "scene-3", "source_message_ids": ["m-41"],
  "event": {"type": "message.addressed", "lane": "foreground", "session": "interactive"},
  "decision": "reply", "reply": "C2", "model_calls": 1,
  "tools": [{"name": "reply", "native_call_id": "call_0", "executed": true, "refused": false,
             "error": "", "receipt": "r-9", "args": {"text": "C2"}}],
  "work_refs": [], "run_refs": []
}
```

Each case declares what it needs (transport, Host capabilities, evidence): a
missing transport or capability blocks the case, and missing evidence makes
the check vacuous, never a pass. A minimal case (the field names are an
example, not a schema the module defines):

```json
{
  "id": "dm-receipt-01",
  "needs": {"transport": ["direct_message"], "host": [], "evidence": ["wake_records"]},
  "steps": [
    {"id": "ctx", "actor": "lead", "text": "Candidates are {C1}, {C2} and {C3}; receipts arrived for {G1} and {G2}.", "wait": {"mode": "silence", "window_s": 30}},
    {"id": "ask", "actor": "lead", "text": "Which candidate has no receipt? Reply with the id only.", "wait": {"mode": "reply"}}
  ],
  "judge": {
    "criteria": "Replies once with {MISS} and nothing else.",
    "checks": [{"steps": ["ask"], "replies": [1, 1], "include_all": ["{MISS}"]},
               {"steps": ["ask"], "max_model_calls_per_wake": 3}]
  }
}
```

A/B practice: give each engine its own conversation or reset history between
cases, since cases sharing a conversation contaminate each other and the
engine that runs second pays for it; alternate the order between rounds; read
the engine setting back and check the engine in every trace; confirm tracing
is on before the run; run at least 3 valid attempts per case and discard
attempts with a restart or deploy in their window. An example "no worse" bar
to adapt: no more hard failures; no case the current engine always passes and
this loop mostly fails; zero private-text leaks; no new known gaps; at most
10% more model calls per wake; first visible reply at most 1 s slower at P50,
with the latency method (paired by step or not) fixed before the run.

## 7. Observability

Use the trace names of the reference runner
([local.md](local.md#langfuse)); it also emits `agent_id`,
`employee_job_kind`, `employee_source_message_id` and, on failure,
`employee_failure`. A Host with one engine may trace from `OnModelCall` and
`OnToolCall`, as `local` does. A Host that swaps engines should emit from the
durable model and its Host wrapper, so both engines produce the same trace
shape (streaming parameters included) and an A/B comparison filters by
metadata only. A wake that ends on accepted final text has no tool
observation in the reference runner: read `employee_decision`, not the tool
list.

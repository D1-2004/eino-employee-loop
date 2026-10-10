# Tools

[中文](tools.zh-CN.md)

The model acts only through tools, and the Host executes every tool. This
document describes the tool contract (`contract/tool.go`), how the kernel
validates and budgets calls (`kernel`), the standard tools (`toolhost`) and
which methods each service implements. The generated list of standard tools
is in [catalog.md](catalog.md#standard-tools); the wire format of a spec is
[schema/tool-spec.schema.json](../schema/tool-spec.schema.json).

- [Tool contract](#tool-contract)
- [Kinds and terminal decisions](#kinds-and-terminal-decisions)
- [Receipts](#receipts)
- [Refusals](#refusals)
- [Batch validation](#batch-validation)
- [Budgets](#budgets)
- [The standard tool host](#the-standard-tool-host)
- [Methods per service](#methods-per-service)

## Tool contract

A `contract.ToolSpec` is supplied by the Host; the kernel never hardcodes a
vendor tool.

| Field | Meaning |
| --- | --- |
| `name` | Unique within a wake. |
| `kind` | `read`, `effect` or `terminal`. |
| `description` / `description_key` | Model-facing text, or the i18n key that `i18n.Bundle.ToolDescriptions` resolves for the wake's locale. Host-authored text wins. |
| `parameters` | JSON Schema object of the arguments. |
| `terminal` | Fixed disposition of a terminal tool; effect tools may also declare the disposition they return. |
| `exclusive` | Must be the only call in its batch. |
| `lanes`, `sessions` | Where the tool may be offered (`contract.ToolsFor`); empty means everywhere. |
| `ports` | Storage ports behind the tool (`tasks`, `memory`, `conversation`, `outbox`, `journal`, `executor`, `config`). |
| `timeout` | Bound of one execution; zero uses 30 s for reads and terminals, 120 s for effects. |

A call is a `contract.ToolCall{native_id, name, arguments, turn}`. The
provider's native call id is the correlation id. Providers may reuse ids in
later model turns, so the kernel stamps the zero-based model turn on every
call: `(turn, native id)` identifies a call within a wake and, together with
the window, is the Host's idempotency key. Provider call ids starting with
`host:` are rejected (reserved for the Host).

The Host answers with a `contract.ToolResult`:

- `content`: returned to the model as the tool message. `toolhost` uses
  compact JSON of machine fields, never prose.
- `receipt`: proves a committed effect.
- `terminal`: a `Decision{kind, reply}` that ends the wake.

## Kinds and terminal decisions

| Kind | Side effect | Receipt | Ends the wake |
| --- | --- | --- | --- |
| `read` | none | no | no |
| `effect` | durable | required | when it returns a terminal decision |
| `terminal` | none of its own (the decision is the result) | no | yes |

Dispositions (`contract.Disposition`):

| Disposition | Meaning | Reply text |
| --- | --- | --- |
| `reply` | answered; nothing else to do | required (non-empty) |
| `quiet` | nothing to say | never delivered |
| `dispatched` | background work accepted; the result arrives later as events | optional acknowledgement; requires a receipt |
| `waiting` | a question was asked; the answer arrives as `human.response` | the question |

Several terminal results in one batch must share one disposition; their
distinct replies are joined. Batch validation rejects calls whose specs
declare different dispositions before anything runs, and results that
disagree at run time fail the wake with `ErrTerminalConflict`. A wake
that ends without a disposition fails with `ErrNoDisposition`. A `reply`
result never proves delivery; `toolhost` says so in its content
(`"delivery_proven": false`), and `dispatch_task` says the result is not
proven yet (`"result_proven": false`).

In the foreground lane the model may also answer with plain text, which
becomes a `reply`. Background lanes (and foregrounds with
`ForegroundRequiresTerminal`) require a terminal call; a Host may accept a
first plain answer through `contract.FinalTextHost`, validated on the same
path as the reply tool.

## Receipts

- An effect without a receipt fails with `ErrMissingReceipt`; so does a
  `dispatched` decision without one.
- Receipts are collected in `Outcome.Receipts` with the native call id and
  tool name. `Outcome.ToolOutcomes` keeps every result in call order,
  including effects committed alongside an error.
- `contract.Host.Execute` must deduplicate effects by identity plus native
  call id and must return the receipt of a committed effect even when the
  context is cancelled right after the commit.

`toolhost` receipts: `dispatch_task`, `continue_task`, `steer_task` and
`stop_task` return the task ref; `ask_human` returns `<window>/<turn>/<call>`;
`memory_capture` and `memory_forget` return the record id.

## Refusals

A refusal is an error wrapping `contract.ErrToolRefused`, returned before
any effect. The kernel puts `{"error": ...}` into the tool message and lets
the model correct the call within the remaining budget. A refusal that
carries a receipt is treated as a failed effect.

`toolhost` refusals are `*toolhost.Refusal{Code}` with machine codes
(`toolhost.RefusalCode(err)`):

| Code | Cause |
| --- | --- |
| `unknown_tool`, `tool_unavailable`, `tool_not_offered_in_session` | the tool is not implemented, its ports are missing, or it is not offered in this lane/session |
| `window_mismatch` | the identity names another window |
| `unknown_source_ref` | `source_ref` is not an event of this window |
| `no_requester` | the source event has no authenticated human actor |
| `empty_text` | a required text argument is blank |
| `forbidden`, `task_state`, `task_not_found`, `invalid_request` | Task Manager answered `ErrForbidden`, `ErrTaskState`, `ErrNotFound`, `ErrInvalidRequest` |
| `task_not_in_scene`, `not_requester` | the task belongs to another scene, employee or requester |
| `bad_read_ref`, `read_ref_not_in_window`, `stale_read_ref` | task controls need a `read_ref` from `read_task` in the same window; `stale_read_ref` is the Task Manager's `ErrStaleRevision` (the task moved past that revision) |
| `no_running_task`, `ambiguous_task`, `task_not_running` | `steer_task` without `task_ref` found no or several running tasks, or the Task Manager answered `ErrTaskState` to the steer |
| `bad_quote`, `memory_quota`, `bad_memory_kind`, `unknown_record` | memory provenance, quota, kind or record problems |
| `section_unavailable` | `read_context` section not served |

Other errors are not refusals. A failed effect, or any error that comes
with a receipt, fails the wake and the Hub retries the window; a failed read
is reported to the model like a refusal. `contract.ErrUnavailable` (a Task
Manager that is closed or overloaded, HTTP 503) and transport failures are
such retryable errors. On replay a recorded refusal keeps
its exact text and its class (see the journal rules in
[architecture.md](architecture.md#journals-replaying-a-wake)).

## Batch validation

Before any call of a native batch executes, the kernel checks:

1. Every call has a non-empty native id, unique in the batch, of type
   `function`, not starting with `host:`, and JSON object arguments.
2. Every tool is offered in this wake.
3. An `exclusive` tool is alone in its batch.
4. The `terminal` dispositions declared by the batch's specs agree
   (`ErrTerminalConflict`), so a batch like `[dispatch_task, reply]`
   commits nothing.
5. Arguments satisfy the tool's schema (portable subset: `type`,
   `required`, `additionalProperties: false`, `properties`, `enum`,
   `minLength`, `maxLength`, `minItems`, `maxItems`, `items`;
   `kernel.ValidateArguments`).
6. A string `reply` argument passes the Host's `TextGuard`.
7. The Host's `BatchValidator`, if any, accepts the whole batch.

A rejected batch executes nothing and never enters the transcript; it
costs its provider attempt and counts as a consecutive error, and the model
receives a localized repair instruction.

## Budgets

`contract.Budget` bounds one wake:

| Limit | Default | Counts |
| --- | --- | --- |
| `max_responses` | 3 | accepted model responses (a valid batch counts even when every call was refused) |
| `max_consecutive_errors` | 3 | provider errors, rejected responses and batches without a successful call, in a row |
| `max_attempts` | 3 | every provider request |

A response that makes progress resets the consecutive-error count.
Exhaustion returns `ErrBudget` joined with the specific limit
(`kernel.ErrResponseBudget`, `ErrConsecutiveErrors`, `ErrAttemptBudget`).
`KernelRunner.Budgets` sets a budget per lane; `Options.Permanent`
classifies provider errors that end the wake at once.

## The standard tool host

`toolhost.New(Ports{Tasks, Memory, Conversation, Journal}, opts...)`
implements every tool of the standard catalog (13 tools) over contract
ports; the standard catalog is exactly what this package implements, and a
test keeps them equal. A tool is offered only when every port it names is
configured (`Host.Specs()`); the tools that name `journal`
(`dispatch_task`, `continue_task`, `steer_task`, `stop_task`) need
`Ports.Journal`, which must be the journal the wake records through
(`KernelRunner.Journal`). `dispatch_task` creates one task from `goal` and
`prompt`; the module has no multi-step plans.

Tools for flows the module does not implement (plans, collections, scene
configuration) belong to the embedding platform. `toolhost` never offers a
tool it does not implement, even when `WithCatalog` lists it; the platform
executes it with its own `contract.Host` and ships its description in its
own pack. [`examples/hostext`](../examples/hostext) is a sample profile
(`continue_plan`, `describe_capabilities`, `scene_config_get`,
`create_collection`, `set_scene_participation`); see
[events.md](events.md#host-extension-profile).

`Host.ForWindow(win, tenant)` binds an executor to one claimed window:

- `source_ref` must name an event of that window; the requester is that
  event's authenticated human actor, never model output.
- Task controls (`continue_task`, `stop_task`) need a `read_ref`
  (`<task>@<revision>`) returned by a `read_task` that the window's journal
  recorded. The journal is the authority, so a re-claimed window keeps its
  reads in any process. The host sends the revision as
  `TaskControl.ExpectedRevision` and checks neither revision nor state
  itself: the Task Manager answers a known idempotency key with its stored
  receipt first, then rejects a changed task (`stale_read_ref`) or a state
  that does not allow the control (`task_state`, `task_not_running` for a
  steer). A control that was applied but whose response was lost is
  therefore replayed into its receipt on re-claim, never refused as stale
  and run again under a new key.
- Every Task Manager mutation uses the idempotency key
  `<window id>/<model turn>/<native call id>`.
- `memory_capture` stores only a verbatim quote of the speaker's own
  message and compacts the bag once when the quota is hit.

## Methods per service

| Service | Implements | Called by |
| --- | --- | --- |
| Employee Loop | `contract.EventSink.Publish` (`scene.Hub`, `employeeloop.Loop`) | gateways, Task Manager, Host |
| Employee Loop Host | `contract.Host.Execute`; optional `FinalTextHost.AcceptFinalText`, `BatchValidator.ValidateBatch`, `TextGuard` | kernel |
| Task Manager | `contract.TaskManager`: `Dispatch`, `Get`, `Find`, `Steer`, `Stop`, `Continue` | Loop Host tools |
| Task Manager executor | `taskmgr.Executor.Execute(job, reporter)`; `Reporter.Progress` | Task Manager |
| Event Gateway | `contract.Deliverer.Deliver` | Loop delivery worker |

Tool to Task Manager method, in `toolhost`:

| Tool | Method |
| --- | --- |
| `dispatch_task` | `Dispatch` |
| `find_tasks` | `Find` |
| `read_task` | `Get` |
| `continue_task` | `Get`, `Continue` |
| `steer_task` | `Find` (without `task_ref`), `Get`, `Steer` |
| `stop_task` | `Get`, `Stop` |

# Host alignment and state ownership

This contract describes responsibility and integration boundaries. It does not
claim wire compatibility with a particular Host SDK or deployed service.
Physical package names are implementation components, not additional authorities.
The local application is a runnable reference assembly.

The [domain model contract](domain-model.md) separates entities from these code
responsibilities. The standalone reference is one Owner with fixed Scenes;
Host business Sessions require trusted assembly, not a native checkpoint alias.
`Options.AuthorizeScope` adds current Host scope checks within the static Profile.
It does not implement dynamic actors, capability merging or action authorization.

## Business responsibilities

| Responsibility | Current implementation | Boundary |
| --- | --- | --- |
| Foreground Interaction | `employee/chat` | Admit the current source, converse, query and issue authorized business commands. An immediate answer may go directly to Delivery without creating a business Task. |
| TaskManager | `employee/task`, including Review; `employee/execution` as its execution bridge | One Task/Goal/Run/Wait authority. Review proposes; validated state commands commit. Execution adapters supply physical facts. |
| ContextBuilder | `employee/context` | Compile an authorized, bounded view from original sources, task facts and selected projections. A view is neither a permanent permission grant nor a Memory store. |
| Memory and learning | `employee/owner` supplies evidence-based local projections | Six independent processing roles are implemented. They are not a complete external Memory service or its read/write/curate/forget protocol. |
| Delivery | `employee/interaction` | Compose output, freeze an intent, reconcile Outbox state and verify provider receipts. Delivery does not decide Goal completion. |
| Application assembly | `employee/entry` and the importing app | Select implementations, exact bindings and capabilities; own resource lifetime. Entry is not a sixth business responsibility. |
| Shared runtime | `employee/kernel` | Eino Agent invocation, checkpoints, model/tool journals and bounded decision mechanisms. These do not own business completion. |

Task execution and Review can remain separate source components inside the same
TaskManager responsibility. Renaming directories does not establish behavioral
parity. The package named `interaction` currently implements Delivery; it is not
the foreground Interaction responsibility implemented in `chat`.

Context, Memory, personal attention and task waits must not be collapsed. Personal
attention is selection input; Task Wait/Watch/Occurrence is a durable delegation;
physical subscriptions are a Host responsibility. Owner workers process admitted
evidence independently. Another role's successful write is not an implicit
prerequisite. A role that explicitly cites another result must verify that exact
result and its audience/source validity.

## Choose one Task authority

For a given accepted Task and its frozen backend binding, choose one composition:

| Composition | Task authority | Integration point |
| --- | --- | --- |
| Complete local application | `employee/task/state.Manager` | The app injects storage, services, execution and delivery Providers into `entry.Open`. |
| Existing Host owns business tasks | The Host's existing task manager | Reuse the needed `employee/kernel` mechanisms and capability adapters. Do not start a second standalone task manager for the same work. |
| Complete external EL backend | That backend's task manager | Register one complete `entry.Options.Backends` implementation. Implement required capabilities and map reads/continuations explicitly. Do not also run standalone state or a competing Outbox. |

A complete service's TaskCreate API is not an atomic execution API. Calling it
under the standalone Run bridge would nest two sets of Goal/Review/recovery
rules. A Host's delegated background-execution API can be an execution adapter
only when it supplies execution facts, preserves the frozen operation mapping,
and leaves business Goal authority with the selected task manager.

The backend registration seam exists. A working adapter to any specific external
service still requires implementation and acceptance; registration alone does
not establish it. SDK/root/kernel/provider versions must be fixed as a tested
set. Keep old implementations until their frozen work drains. Never silently
fall back to a different backend after an ambiguous operation.

## Host contracts that must survive adaptation

1. **Identity and addressing.** Authenticated caller, agent identity, source
   reference, business context, provider channel and delivery target are separate
   values. A transport endpoint or local provider name is not an agent identity.
   An opaque Host reference is not parsed to invent tenant/requester/permission.
   Map Host identities to `contract.Scope` in trusted app code. Recheck current
   authorization, source and audience when reading, resuming and delivering.
2. **Execution.** Keep the immutable Task/Run/submit-key/operation binding.
   `Submit/Lookup/Poll/Cancel` describe this library's Run interface; another Host
   may expose create/steer/resume/status with different DTOs. Unsupported control
   must fail explicitly. A successful cancel request, absent lookup or finished
   model is not physical exit proof. Unknown submission is reconciled under the
   original identity; it does not authorize a new writer or successor Run.
3. **Continuation.** Preserve the original Task, current Goal/revision, human
   source and exact waiting target. Resume the admitted continuation; do not
   create replacement work to bypass a blocked state. Completed Goal revisions,
   current business authorization and old-writer exit remain separate checks.
4. **Business state and views.** A Run result, a checkpoint and a final business
   outcome have different meanings. Working state is private resumable state,
   not a user-facing success result. An integration must expose authoritative
   current state with version/provenance and mark incomplete or stale reads.
   Do not label `LatestRun.Result` as a reviewed final outcome.
5. **Delivery.** Freeze the destination, content/resource versions, effect key
   and provider binding. Keep one scheduling Outbox and one provider operation
   ledger with distinct ownership. Lookup must not send. `provider_accepted`,
   `delivered` and `local_applied` remain distinct. Unknown effects retain their
   original identity and require reconciliation rather than a new send ID.
6. **Memory.** Preserve owner/subject scope, original attribution, source
   revisions, current grants and forgetting/revocation fences. A summary is a
   projection; it neither grants actions nor proves a task completed. External
   memory credentials and persistence belong to the app's connector Provider.
7. **Persistence and lifetime.** A storage adapter must implement transactions,
   CAS, event/receipt atomicity, scoped checkpoints and fenced leases. A CRUD
   wrapper around another database is insufficient. Factories do not close
   borrowed State/Artifacts; the composition root owns them. Open, worker Start,
   readiness and Close must remain explicit lifecycle decisions.

## Current coverage and gaps

The local reference verifies one Task authority, revision/exit checks, scoped
sources and audience, independent Owner processing, native interruption/resume,
Outbox unknown handling and actual file receipts. The Eino runtime is used for
Agent invocation, checkpoint/resume and journaled model/tool effects.

The following are not established by those local checks:

- A specific Host's authenticated AgentRef/Context/channel mapping or private
  wire SDK compatibility. `Scope` is the library's model, not that SDK's DTO.
- A versioned Task WorkingState, ordered input watermarks and an independent
  final-outcome query equivalent to a richer existing Host task system.
- A full Memory connector with external retrieval, record mutation, behavior
  rules, forgetting barriers and asynchronous summary semantics.
- Rich channel negotiation, message revisions, interactive action callbacks,
  retention-window reconciliation, platform files and real IM delivery.
- Full foreground/background budget, dependency/collection, steer/resume and
  durable subscription protocols of a particular service.
- An external database/queue adapter preserving all of that service's lease and
  transaction behavior, or a complete service backend accepted end to end.

These gaps are explicit adapter or framework follow-ups, not reasons to add
parallel task stores, a second Outbox or platform-specific SDKs inside employee.
Start with one complete integration route and exercise its real minimum path:
source admission → original Task → execution → reviewed outcome → actual target
receipt. Include unknown submission/delivery, restart, late answers and stopping
an active writer. Tie evidence to the exact versions and environment. Scripted
local evaluation proves mechanisms, not online service parity.

See [architecture](../../ARCHITECTURE.md), [module assembly](../module-composition.md),
[executor](executor-port.md), [delivery](delivery-envelope.md),
[audience](audience-isolation.md) and [evaluation](../../evals/README.md).

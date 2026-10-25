# Whole TaskManager Provider

Status: implementation candidate, acceptance pending. A Host may already own Task/Goal/Run,
execution, Review, timing and delivery. In that case the SDK must use that single
authority instead of wrapping another business Task around it.

Reuse the existing ModuleRegistry and exact-version Profile selection. The
builtin/default module retains the standalone local behavior. An external module
provides the complete required command/query/advancement/recovery surface and
declares host-driven lifecycle. Task values are authorized projections, not rows
to mirror in the SDK database. Preserve original task identity and stable operation
keys; unknown effects are queried, never retried under a new identity.

Opening an authority/query port precedes Context construction; binding the builtin
runtime follows once its Context/Runner dependencies exist. This is a specific
two-phase module assembly, not a generic dependency injection container. External
mode must not construct/start the SDK Manager, Bridge, TaskWorker, Review/Timing
workers or task-result projector. Conflicting local cognitive modules reject
rather than creating a hybrid authority.

Delivery ownership is separate from final transport. Provide a narrow intent
sink for Chat replies/ACK/questions and delegate to the Host's existing Outbox in
external mode; do not create SDK business intents and forward them to another
business Outbox. Local default keeps its original intent authority. Native Eino
permissions, source/read handles and effect-integrity checks remain enforced.

Freeze the selected TaskManager Provider revision with admitted configuration.
A later selection cannot reinterpret old Tasks, controls, receipts or recovery.
The legacy default must preserve its old serialized identity, not silently add
a new builtin field to every historical Profile.

## Local acceptance and integration boundary

Use an independent external authority to exercise actual create/control/wait/
progress/recovery and delivery. Assert zero SDK task, work-event/receipt, execution,
review-hold, timing and Outbox business writes in external mode, and zero local
worker factory calls. Duplicate/unknown commands, current revocation, cross-scope
reads and reopen with another current Provider must keep the original authority.
Default local native execution/Review/Outbox must still pass.

An adapter to an existing platform is a separate acceptance slice. New inputs
freeze the exact engine/family/provider before preparation; old admitted and
in-flight work retains its original family. Rollback changes only future input.
A configuration flag or a source compile is not a running-version proof.

Shared rollout is intentionally deferred while this contract is being polished.
A local test must be bound to its authenticated owner and dedicated employee/
conversation; other users keep their existing embedded path. No global switch,
unrelated tenant mutation, production action or extra live task is part of this
Provider extraction. A one-command local launcher and the same independent quality
cases are required before considering a separately authorized rollout.

## Binding and original-operation contract

`entry.TaskManagerBinding` supplies `Queries`, `Operations`, `Timing`, `Recovery`,
`Intents`, `Messages` and `Closer`, with `HostDriven=true`. Query and intent ports
are separate so the caller receives only its required surface. The factory gets
the frozen Profile and current-scope authorization function; it does not receive
the SDK business database. A custom binding must be complete; incomplete bindings
and conflicting local loops or delivery transport reject during Open.

`task.Operation` carries the original scope, source, native call identity and the
specific dispatch/control/answer arguments. Before submitting, the SDK asks the
same authority for `LookupOperation`. The result must bind the original operation
key, exact request SHA256 and scope. `accepted` reuses the actual receipt;
`unknown` remains unresolved; only an authoritative `not_started` permits the
original operation to execute. A mismatched key/hash/scope rejects. This protocol
uses the Host's existing transaction and operation facts, not a second SDK ledger.

The default absence of `Modules.TaskManager` retains the old serialized Profile
and builtin implementation. External mode requires only the prompt roles that
the SDK actually runs; hosted execution/Review does not require fabricated local
role instructions.

# Executor port

M2 `modules/task` owns Task, Goal and Review semantics; M7 Entry assembles it. An executor owns one physical Run
identified by the complete Scope, Task ID, Run ID and stable submit key. A
provider must not create another Task or decide that a Goal is complete.

The Host selects an exact executor ModuleRef through ModuleRegistry; Profile
freezes that ref and an existing directory cannot silently switch its core
executor. A factory receives scoped State, authorization, owner identity and the optional
local native Run function; it does not receive Work, Artifacts or all Options. These are trusted Go capabilities, not a sandbox or a permission
grant to model-generated code.

`execution.Port` provides Submit, Lookup, Poll and Cancel. Poll does not receive
a local callback: the default Local executor receives its Run function when
assembled, and a remote provider can poll its own service. Entry validates every
returned identity before applying a fact. Submit is idempotent under the frozen
request; retrying the same key with changed configuration or input is rejected.
Entry journals the frozen request and compares returned Task/Frame/configuration
against it. Artifact facts must use canonical paths under
`tasks/<owner-key>/<task-id>/` with valid SHA256 and nonnegative byte counts.
Review subsequently reads actual artifacts and verifies their hashes. A provider
must materialize remote outputs into this scoped artifact capability; it cannot
use Owner memory or another Task's file as completion evidence.

Timeout and NotFound are not exit proof. Ambiguous submission enters RunUnknown
and is resolved by Lookup; it is never resubmitted merely because a remote lookup
returns NotFound. Poll/Cancel can report pending work, but a successor or Review
requires a durable terminal fact with Exited=true. Cancellation must preserve the
exact Task/Run/key and any known execution ID.

Entry commits RunUnknown before calling Submit, so a crash after intent cannot
be mistaken for a never-attempted submission. This intentionally favors safety:
an unresolved provider absence stays blocked until Lookup resolves it or Cancel
returns a durable fence/exit proof. Close does not turn it into a terminal fact.

Local Cancel runs under the Entry leadership lease and excludes active local
invocations. If a key was never submitted, it transactionally stores a cancellation
tombstone. Any late Submit with that key returns the tombstone and cannot invoke
the kernel. The resulting NeverSubmitted record is a durable cancellation proof,
not a physical execution receipt. Work records that proof without inventing an
execution ID or inspecting executor storage. The tombstone is committed before
Work advances; a crash between those steps replays the same proof. Thus the
cross-module operation does not need a shared private storage namespace or a
transaction spanning model/network I/O.

Providers returning NeverSubmitted must guarantee the same durable key fencing:
transient absence, a timeout or an eventually consistent NotFound is insufficient.
Providers unable to prove absence return unknown/pending and keep the Run blocked.
Provider records and proof capabilities are trusted Host facts, not model output.

Advance and Stop remain Local-only compatibility helpers. Entry only uses Port.
Close belongs to Entry's provider lifetime, after workers stop. Providers must release their resources without changing an unknown Run into an exit fact.

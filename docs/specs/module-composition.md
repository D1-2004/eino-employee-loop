# Typed module composition

The standalone Host owns one Work aggregate and all business commits. A module
factory is trusted compiled Go code selected by an exact immutable name/version;
JSON never installs code. Default references resolve to builtin@1. Explicit
unknown references are errors, without fallback.

`Profile.Modules` freezes Storage, Context, Executor, Review, Scheduler and Owner
selections. The registry has a separate typed factory for each kind. Model and
Delivery keep their existing ports. Core state/storage/context/executor/scheduler
selection is a directory binding; changing it requires an explicit migration,
not merely a new profile revision. Review and Owner use the work's frozen profile.
Old registered factories must remain available until old work is drained.

Factories have separate minimal dependency types, never the Host `Options` or a
universal service container. Storage receives its directory and a Profile snapshot.
Context receives stores, authorization/audience policy and a read-only task reader;
Executor receives State, lease identity, authorization and the native Run callback.
Review receives authorization and a Profile snapshot, and only proposes changes.
Scheduler alone receives the Work manager plus the source reader needed to validate
signals. Owner receives stores, its Context contract, authorization, lease identity,
Profile snapshot and model/plugin callbacks bound to each observation's frozen
configuration. Profile slice values are copied, and Context's task reader does not
expose the manager through its concrete dynamic type.

The zero-registration local assembly remains the default. Exact-version registries
are an advanced Host replacement mechanism for implementations that need it, not
mandatory configuration for ordinary local use. These narrower types remove ambient
write/configuration access without introducing another container or parallel state.

Storage is opened first, then leadership, Work, Context, delivery, Executor, Scheduler
and Owner. Dependencies must be ready before workers start; failures close already
owned resources in reverse order. Host closes module resources after workers and
explicit operations have stopped. A module must not close shared State/Artifacts.

Review modules propose a typed Review from a frozen Task/frame; only Host applies
the proposal after current authorization/source/revision checks. Executor methods
report submission, lookup, polling and cancellation facts; no business completion.
Context checks audience before model access and again on recovery. Owner roles may
be registered as trusted policies; their exact instructions are frozen per job,
and profile revision binds policy configuration. Scheduler operates on the same
Work aggregate. A replacement cannot create its own parallel Task/Goal database.

Optional business backends publish capabilities; operations on an unsupported
capability return ErrUnsupported, including failures/diagnostics. Open checks any
explicitly required capabilities and enabled Owner loops before accepting work.

A first Open reserves its directory module selection even if initialization later
fails. Retry the same selection, or inspect and choose a fresh private directory;
there is no implicit rollback of possibly partially initialized external state.

Factories and plugins have no arbitrary hot unload or automatic migration. Version
selection plus retaining old factories supports recovery; it is not an unload API.
Native subagents remain trusted code with explicitly scoped models, permissions,
budgets and lifecycle supplied by their factories.

## References

The native boundary follows the pinned Eino v0.9.21 ChatModelAgentConfig, Runner,
CheckPointStore and handlers/callbacks rather than a custom workflow interpreter.
Storage follows the existing Host atomic state/receipt/event pattern. The
Executor separation follows the execution-vs-business-completion distinction in
this project's Host contract. These patterns preserve recovery and ownership;
adding factories is not permission to weaken those contracts.

## Default local assembly and service capabilities

`localapp.Open` from `app/local` (explicit import alias `localapp`) is the default composition root: SQLite, Markdown, native Eino,
one Work aggregate, local execution and the existing outbox. Ordinary callers
supply identity/profile, directory and model; no module registration is required.
The application supplies `DefaultStorage`, `BuildServices` and `PrepareTools`;
M7 imports no concrete adapters. Advanced `entry.Open` hosts supply their own
assembly functions and storage factory.
`localapp.DefaultProfile` enables the six Owner workers and local delivery loops.

`services.Services` in `ports/services` groups Config, Messages, Resources, Tools and Files. Existing
execution.Port and interaction.Port remain the execution/delivery seams. Local
services return real admitted sources and verified artifacts; they do not claim
platform history completeness or mint remote access URLs. Individual services
may be overridden without rebuilding the business runtime. The default native
executor can opt into the read-only host tool catalog via frozen Profile.HostTools.
The catalog is persisted per execution and changes during recovery are rejected.
Effectful tools require a separate trusted journal adapter; the convenience read
catalog does not manufacture effect receipts.

Business scope, opaque admission binding and delivery destination are distinct.
PrepareIntent resolves delivery routing outside transactions once, then freezes
it in the existing outbox. Provider acceptance is pending reconciliation rather
than applied delivery. No Task API is nested underneath the atomic execution seam.

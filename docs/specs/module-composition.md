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

Factories receive State/Artifacts, the one Work manager, scoped authorization,
Context, model/native dependencies and lifecycle identity as appropriate. Storage
is opened first, then leadership, Work, Context, delivery, Executor, Scheduler and
Owner. Dependencies must be ready before workers start; failures close already
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

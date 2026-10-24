# Host service boundary

The public `employee/ports/services` package (Go name `services`) describes service capabilities, not another runtime.
`execution.Port` remains the execution boundary and `interaction.Port` remains
the durable Outbox's delivery boundary. A host adapter must not create a parallel
Task aggregate, execution journal or Outbox.

## Identity and injection

`Call` contains the business Scope, optional admitted SourceReceipt, Task/Run and
an opaque host Binding. These are locators. `services.Bindings.Authorize` must check
the trusted caller and binding against actual authority on every call; accepting
arbitrary Scope strings is not authorization. Binding contains no credentials.
Every supplied SourceReceipt is read again before releasing a result. A frozen
request is evidence, not a continuing permission grant.

`Services` separates Config, Messages, Resources, Tools and Files. Hosts can
replace individual groups. Providers receive only read interfaces and a config
snapshot function, not Entry Options, a writable Work Manager or the database.
`services.Bindings.Overrides` applies these replacements before default tools bind;
their reads use the final Messages and Resources providers. Explicit tool
replacement is respected. Every replacement provider owns its authorization,
freshness and output contract just as a remote adapter does.

The default `NewLocal` in `app/local/providers/services/local` uses existing admitted sources, Task facts and artifacts.
It owns no dependencies and opens no external network connections. Config is a
detached snapshot of the host's real profile, with an explicit immutable version.
Message lookup requires exact IDs and reports local admitted-ledger coverage and
missing IDs. It never claims to expose complete platform conversation history.
File/resource reads bind Scope, Task, Run, path and content hash, and read bytes
back before returning. The content hash is the immutable local version. A changed
file fails, even when its path still exists. Resources return bytes, not invented
HTTPS URLs or reusable access grants. Unsupported capabilities return errors.

## Tools

`DiscoverTools(Call)` returns named public JSON schemas. `CallTool(Call, ToolCall)`
keeps host authority outside model arguments. The local provider exposes only
`host_read_source` and `host_read_artifact`, delegating to the same checked service
reads. `app/local/providers/services/eino` binds the discovered read-only catalog to native Eino
tools. `app/local` supplies this binder through Entry's `PrepareTools`; Entry
imports no concrete service adapter.

This convenience adapter accepts read tools only. Effectful tool providers must
be bound through the host's native-call journal: a trusted nonempty CallID,
deduplication against that Call identity, and a durable receipt for a committed
effect are mandatory. The adapter rejects effects rather than inventing CallIDs.
Unknown outcome is not a failed effect and does not authorize blind retry.

## Online alignment

The examined Buddy source baseline `b036794e` uses Host + opaque AgentRef/AgentCall
and task-level BackgroundProvider operations. The examined candidate `0a8ce071`
uses task-scoped CallContext and execution-level Run/Command operations. These
are different transport contracts. Public adapters map their trusted bindings
explicitly; neither private implementation nor private dependencies belong here.

Common semantics retained by this boundary are trusted admission, exact request
identity, immutable resource facts and explicit missing/unsupported results.
Delivery target is separately frozen by the delivery adapter; business Scope
does not infer an external conversation address. Provider accepted, process exit,
Task Review completion and message visibility remain distinct facts.

Local conformance does not establish real DWS connectivity or deployed parity.

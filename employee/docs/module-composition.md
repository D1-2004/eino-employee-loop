# Composing an Employee Loop application

The [seven framework modules](../../ARCHITECTURE.md) live directly under
`employee/{chat,task,context,owner,execution,interaction,entry}`. M7 assembles and
manages lifetime; each business module owns its prompts, tools and algorithms.
One `state.Manager` in `employee/task/state` owns Task/Goal/Run transitions.

## Default runnable application

`app/local` is a reference executable (`package main`), not an importable library.
It supplies SQLite, Markdown, native Eino, local atomic execution, file delivery,
service readers and the native read-tool binder. Run from the repository root:

```sh
go run ./app/local -profile evals/cases/local/allloops-profile.json \
  -dir /tmp/el-services-demo -script evals/cases/local/allloops-script.json \
  replay -in evals/cases/local/allloops-input.ndjson
```

The application assembles `DefaultStorage`, `DefaultExecutor`, `DefaultDelivery`,
`BuildServices` and `PrepareTools` for `employee/entry.Open`. Identity, scenes,
model and enabled loops come from the accepted profile. A supplied profile is
honored without silently enabling new features; increment its revision when
changing configuration. Ordinary local use requires no module registration.

To embed the framework in another service, import `employee/entry` and assemble
your own Providers. Read the local application as a recipe; do not import its
`package main`. Concrete Providers belong to the application, while typed
contracts and business state remain in `employee`.

Execution's read-tool catalog is frozen per Run. Recovery rejects a changed
catalog; a read tool cannot be relabeled as an effect. Effectful tools require a
trusted native-call journal adapter. Replacing the Config service does not change
the accepted profile, model, budgets or frozen task configuration.

Caller-supplied service clients are borrowed; the caller owns their lifetime.
Factory-created instances follow the owned-resource Close contract below.

## Aligning service boundaries

| Service group | Default local implementation | Online adapter responsibility |
| --- | --- | --- |
| Config | Immutable Profile snapshot; frozen task config when Task-bound | Map exact configuration version and trusted binding |
| Messages | Exact admitted-source IDs, explicit missing IDs and coverage | Authenticate actor/conversation; preserve provider timestamps and gaps |
| Resources | Task/Run/path/hash-bound content bytes | Map immutable version/hash and expiring access; no guessed URL |
| Tools | Read-only discover/call and native schemas | Permissions, stable native call ID, durable receipts for effects |
| Files | List real artifacts and verify their contents | Map Task/Run file identity and access grants |
| Execution | Existing Submit/Lookup/Poll/Cancel/Close | Translate atomic Run/Command facts, not a second business Task |
| Delivery | Existing outbox with typed Target/Payload and staged receipts | Resolve and authorize target; query actual provider outcome |

The current service and a newer candidate differ in agent/task binding and
background Task vs atomic Run APIs. These ports align semantic boundaries;
they do not claim wire compatibility or a working deployed adapter. See
[Host service contract](specs/host-services.md).

Remote delivery needs all of Delivery, ResolveDelivery and AuthorizeDelivery.
The resolver freezes a destination once. The authorization hook checks that exact
frozen destination before Send; changing the resolver cannot redirect a replay.
Provider acceptance remains pending until Lookup proves delivery. Task facts retain
stage, transport and fingerprints, so local-file application cannot be mistaken
for platform delivery. Local rejects rich resources/cards it does not implement.

## Advanced module replacement

`Profile.Modules` selects independent Storage, Context, Executor, Review, Scheduler
and Owner implementations. Omitted references select `builtin@1`; an explicit
unknown name/version fails before workers start. `entry.ModuleRegistry` has typed
`RegisterStorage`, `RegisterContext`, `RegisterExecutor`, `RegisterReview`,
`RegisterScheduler` and `RegisterOwner` methods. Duplicates and builtin overrides
are rejected. Existing `Models`, `Delivery`, `Callbacks` and `Plugins` remain the
model, delivery and native extension ports.

Applications import the framework API, not another application's `package main`:

```go
import "github.com/D1-2004/eino-employee-loop/employee/entry"

runtime, err := entry.Open(ctx, entry.Options{
    Directory: privateDir,
    Profile: profile,
    Models: models,
    DefaultStorage: storageFactory,
    DefaultExecutor: executorFactory,
    DefaultDelivery: deliveryFactory,
    BuildServices: serviceBuilder,
    PrepareTools: nativeToolBinder,
})
if err != nil { return err }
defer runtime.Close()
```

The factory and binder variables above are supplied by the embedding application.
For concrete signatures and an assembled recipe, read
[`app/local/local.go`](../../app/local/local.go). The local application's Providers
live in `app/local/providers`; a different host can implement the same contracts
in its own application. The framework never imports the reference application.
A separately registered exact-version factory replaces the selected default;
omitting a required Provider is an assembly error, not permission to select a
hidden local implementation.

The reference memory storage is process-local. It is suitable for conformance and
ephemeral embedding, not crash recovery or independent-process coordination.

## Keep authority in the Host

| Port | Replacement supplies | Host retains |
| --- | --- | --- |
| State / Artifacts | Transactional CAS, atomic event/receipt writes, scoped checkpoints and fenced leases; conditional file writes | One business aggregate and effect identity; projection verification |
| Context | Admit/read/build/revalidate and explicit audience checks | Trusted identity and audience grant; no inference from scene names |
| Executor | Submit/Lookup/Poll/Cancel/Close facts on one Run | Request snapshot, unknown handling, Run/Goal ownership, artifact and result checks |
| Reviewer | Typed proposal from the frozen Task and verified frame | Source/permission/revision checks and `ApplyReview` commit |
| Owner | Observe/Tick/Documents for the selected version | Frozen profile, sources and role policy; native extension composition |
| Scheduler | Conditions/occurrences/attention on existing Work | Same Task aggregate, occurrence and authorization contracts |
| Delivery | Send/Lookup receipts | Durable intent, unknown lookup obligation and accepted outcome |

Each factory has its own small dependency type. Storage gets directory/profile;
Context gets read-only Tasks; Executor gets State, owner identity, authorization,
Artifacts, read-only Tasks, native Context and an Agent Runner; Review gets
authorization and a frozen profile;
Scheduler gets the shared Work and a source reader; Owner gets its required
stores and frozen model/plugin functions. No factory receives the whole Options.
The reference app builds its native executor from those dependencies. A remote
Executor may use its own service and must not create a second business Task/Goal API.

A Reviewer only returns a proposal. The Host checks its original Task revision,
Goal revision and Run ID, revalidates the input, then applies the proposal. An
execution summary or provider exit alone is not evidence of business completion.
Trusted factories must supply their actual business review policy.

The builtin Owner in `employee/owner` uses independent native agents. Custom roles can be assembled
with `owner.Options.RolePolicies` inside an exact-version Owner factory;
selected policy text is frozen in the observation. A profile role must be
registered by that selected Host implementation. No arbitrary role is silently
mapped to an existing role.

## Recovery, resources and migration

Storage/Context/Executor/Scheduler selection is bound to the private directory in
`module-selection.json`, independently of the chosen State adapter. Changing that
selection requires an explicit migration. The first Open reserves this selection
even if later initialization fails; retry the same selection or use a fresh
private directory after inspecting the failed initialization. Changing the storage provider cannot
hide the original binding. Existing unmanifested builtin directories can continue
with builtin modules; this does not authorize automatic conversion to a new store.

Review and Owner resolve from each work item's frozen profile, including old
versions after reopening with a newer profile. Keep old factories available until
that work drains. Missing factories fail explicitly; there is no latest-version
fallback, hot unload or automatic migration. Factories for old and new Owner
versions continue to tick their own queues.

Factories are ready before workers start. Entry stops and joins workers/explicit
operations before closing modules, then Artifacts, the leadership lease and State.
Module implementations with resources implement `io.Closer`; Entry owns their
close, including partial initialization failures. They must not close shared
State/Artifacts. Review resources are scoped to one proposal.

A custom complete Backend declares `Capabilities() []entry.Capability`. Entry
checks declarations against implemented operations and `RequiredCapabilities`
before acceptance. Unsupported Schedule, Signal, Owner, Retry and Diagnostics
return `entry.ErrUnsupported`; unsupported diagnostics are not an empty success.

## Audience and plugins

Owner projections are evidence-cited views. By default only same-scene evidence
may enter a model. An injected `AudiencePolicy` can explicitly allow transfers
within the same workspace/tenant/employee/requester; it cannot override those
mandatory boundaries. A mixed document with any unauthorized evidence is omitted
whole. Old frozen files without audience-bound evidence fail closed. Withdrawal
and grant changes are checked again on recovery and immediately before external
Executor submission.

`Profile.Plugins` applies to builtin Owner agents as well as ordinary native
roles. CLI `-list-plugins` discovers compiled plugins; `audit@v1` emits structural
lifecycle metadata to stderr without messages, outputs or credentials. Plugin
factories remain trusted Go: ReadOnlyAgents is not a sandbox, and child agents
need their own scoped models, budgets and cleanup. See [plugin contract](specs/plugins.md).

## Verification and remaining scope

Conformance checks exercise actual SQLite/Markdown and independent memory state,
real Entry file effects, native checkpoints, independent Executor/Reviewer/Owner
implementations, old versions and rejection cases. Scripted models make these
mechanism checks deterministic; they do not certify real-model quality, actual
DWS delivery or full Buddy protocol parity. Rich DWS delivery, platform identity,
source reading, budget/finalization and typed business waits remain separate work.

Native composition follows Eino's [ChatModelAgent](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/chat_model/)
and [Runner/extension](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/)
contracts, checked against the pinned v0.9.21 source. No custom JSON workflow
interpreter is introduced.

The [advanced module diagram](architecture/module-composition.png) shows the lower-level replacement seams.

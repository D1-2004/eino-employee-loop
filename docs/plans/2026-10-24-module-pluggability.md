# Module boundaries and replacement delivery

Status: implemented and locally accepted. Baseline: public main `6dfba1f`; this is a local SDK delivery,
not a deployment or certification of Buddy parity. The private review and live
comparison remain evidence of the outstanding business protocol gaps.

## Result and boundary

Fix audience isolation and extension coverage, then make the standalone Host's
Executor, Context, Review, Owner, Scheduler and storage replaceable through typed,
versioned factories. One Work aggregate remains authoritative. The default is
SQLite + Markdown + native Eino; a second independent memory storage and an
external-style Executor must pass the same behavioral boundary checks.

No IM sends, shared environment changes or production deployment. Do not reproduce
all missing Buddy business protocols in this structural change. GitHub delivery,
if performed, uses Git Data API and preserves concurrent main updates.

## Design before implementation

See `../specs/module-composition.md`, `../specs/audience-isolation.md`,
`../specs/plugins.md`, `../specs/executor-port.md`, `../specs/storage-ports.md`.
Use Eino's existing ChatModelAgentConfig/Runner/CheckPointStore and standard
handlers/callbacks for cognition. Follow dependency inversion at the Host port:
typed atomic operations, explicit exact selection, no hidden latest/fallback,
no second Task state machine. Existing fixed Eino v0.9.21 source is the native
execution reference; no performance-only redesign.

## Acceptance and verification

- Audience: actual State/artifacts → model input, source evidence and frozen-frame
  revalidation; private cross-scene material excluded, explicit Host grant revoked
  at use time; no inference of audience from payload or scene name.
- Plugins: actual Entry Chat and Owner execution with selected factory, old frozen
  configuration recovery, unknown version rejected, CLI built-in catalog usable.
- Executor: full Entry Task → independent Provider → Host Review → local delivery;
  unknown/NotFound/cancel without exit never creates a successor; local recovery
  remains idempotent. No nested business Task API.
- Storage: same real Work/outbox/CAS/rollback/checkpoint/lease contracts on SQLite
  and independent memory adapters. Memory is a process-local test/reference choice,
  not crash durability or multi-host production validation.
- Composition: exact selected factories resolved at Open before workers; unsupported
  optional backend capability is an error; independently replace key modules without
  editing Host implementation; frozen modules never silently change on resume.
- Checks: scoped tests during development, full public Go tests/vet and selected
  race checks after integration; local CLI scripted E2E proves assembly and actual
  state/file effects, not real-model quality or real DWS delivery.

## Progress

- Plan/spec: defined.
- Implementation: complete. Audience, plugins, Executor, storage and typed composition integrated.
- Local verification: full `go test ./...`, `go vet ./...`, and race checks for
  Entry/Context/Owner/Execution/Work/Storage/Journal/Interaction/CLI passed.
  The same contract exercises independent storage and execution implementations.
  Additional review found and fixed pre-submit revocation checks on recovered
  requests and partial Backend factory resource cleanup; regressions pass.
- CLI acceptance: actual command/binary + SQLite/md + scripted model produced
  one completed Task, one Run, one applied local-file delivery and six Owner
  documents with audit@v1; replay changed none of those counts. Evidence retained
  privately at `/private/tmp/el-modules-20261024-0vxe7avj`; no secrets or actual
  user conversations are included in this repository.
- Integration/publication: this change targets GitHub main through Git Data API;
  the final handoff records the verified ref/commit. No server dependency upgrade
  or shared integration branch deployment is included.
- Online deployment/business acceptance: not applicable to this SDK change.

## Follow-up: simple local assembly and online service seams

Status: implemented and locally accepted after user clarified flexibility, online interface alignment
and a default local assembly. Baseline is public main 2bbb2cc.

Result: narrow per-module dependencies without whole Options, one explicit local
preset, independently replaceable capability services and a richer existing
delivery envelope. Advanced versioned registry remains for long-lived recovery;
default users do not register six factories. No second business aggregate/outbox.

Reference: current Buddy's fixed deployed source and candidate service contracts
were compared separately. They differ in agent/task call binding and background
Task vs atomic Run execution. Generic groups guide seams, not wire compatibility.
Private DTO mapping stays in internal consumers, no private dependency or copied
implementation is introduced into this public module.

Acceptance: local preset -> actual file work/Review/outbox + six Owner workers;
service Config/Message/Resource/Tool/File calls return real local facts and explicit
coverage; override one provider without rebuilding other modules; target and
provider-accepted/delivered receipts retain exact identity and unknown behavior.
Real-model quality and live service interoperability are not inferred from these
mechanism checks. Scoped checks, full tests/vet and relevant race checks required.
No IM writes, new subscriptions, service deployment or server dependency upgrades.

Follow-up results:

- Deleted the all-purpose dependency bag. Each module receives its required
  interfaces, detached profile and scoped functions; no complete Options.
- `localapp.Open` and `DefaultProfile` supply the normal SQLite/Markdown/native
  composition; CLI uses the same root. Five Host service groups are independently
  replaceable; execution/delivery retain their existing ports and state owners.
- Read-only Host tools are actually called by native execution, with their catalog
  frozen per Run. Effects are explicitly unsupported by this convenience adapter.
- Existing Outbox now freezes typed destination/payload and retains provider
  accepted vs delivered vs local applied evidence. Current target authority is
  checked separately. Task facts preserve stage/transport/fingerprints.
- Review caught an exported service lifetime gap: all borrowed clients now use
  Entry's existing cancellation/join boundary, reject calls after Close, and
  retain genuine effect receipts alongside cancellation. Regression checks pass.
- Full Go tests, vet, relevant race suites and actual CLI replay passed. Default
  assembly produced 1 completed Task, 1 Run, 6 Owner documents, 1 local-applied
  file delivery and no duplicate on reopening/replay. The CLI's native executor
  uses the read-source service before creating the real artifact. Private evidence:
  `/private/tmp/el-local-services-20261024-8i1e1hcg/summary.json`.
- Protocol alignment is at capability/authority/fact boundaries. No live service
  adapter, actual DWS delivery, private dependency, server upgrade or deployment
  is claimed. Read-only convenience services do not emulate unsupported upload,
  expiring network access, rich cards or effect tools.

## Architecture-aligned source layout

Status: implemented and locally accepted after user rejected the accumulated root packages. Baseline
94c6675. This change must match the seven business modules in source ownership,
not merely move the same monolithic Host behind new directory names.

Target:
- modules/chat (M1): admissions, chat tools and foreground continuation.
- modules/task (M2): Task/Goal/Run state, Review and authorized scheduling.
- modules/context (M3): source views and audience/revocation gates.
- modules/owner (M4): independent Owner roles and evidence projections.
- modules/execution (M5): atomic provider protocol and native local execution.
- modules/interaction (M6): message composition, delivery, packaging and outbox.
- modules/entry (M7): public lifecycle, module selection and orchestration only.
- kernel: generic Eino runtime, decision/schema helpers, model journal and sessions.
- ports: external capability/storage contracts; adapters: concrete implementations.
- app/local: the default SQLite/Markdown application composition; cmd/el: one CLI.
- examples/dingtalk: the separate lightweight DingTalk example, no competing cmd.
- contract: small shared identity/profile/input values, with no business services.

Existing Go package imports change; pinned consumers retain their old versions.
No old-path forwarding packages are added and no internal server dependency is
upgraded. Persisted JSON, receipt identity, native checkpoint versions and journal
keys must remain unchanged. Historical docs/reviews become an explicit archive,
while README/ARCHITECTURE and current guides show one supported reading path.

Acceptance: all seven module implementations own their prompts/tools/algorithms;
Entry contains no business tool schemas or policies. Kernel cannot import business
modules; business modules cannot import Entry/app/concrete adapters. Verify package
imports, builds and current contract tests, native interruption/restart, unknown
submit/delivery, default CLI six-Owner file delivery and replay. Also reopen a
pre-refactor state directory with the new binary. No IM/network business writes,
new service behavior, production deployment or performance-only optimization.

Physical layout results:
- Seven business packages now own their implementations. M1 owns input/chat
  tools; M2 owns Review/timing/progression; M5 owns request and native execution
  protocol; M6 owns expression/package/Outbox. M7 contains only assembly/lifetime
  and delegates diagnostics/recovery to module APIs.
- Concrete storage/services/native-catalog choices moved into app/local with
  adapters; M7 has no adapter imports. Go package names also match directories.
- Architecture guards prevent kernel->business imports, business->entry/app/adapter
  imports, M7 adapter imports and reintroduction of business schemas/private job
  namespaces into Entry. They caught and removed a journal hash dependency on M3.
- README and ARCHITECTURE show the same source tree; generic SDK guides and
  historical reports are distinct. Previous reviews/evals live under docs/archive.

Architecture-layout acceptance:
- Full `go test ./...`, `go vet ./...`, and race suites across all business modules,
  app/local, kernel, ports, concrete adapters and CLI passed at the new paths.
- A pre-refactor 94c6675 binary created a genuine native interruption in SQLite;
  the new binary resumed that exact target and delivered the answer, preserving
  the original question delivery. Replaying did not add another message.
- A completed pre-refactor six-Owner directory reopened and replayed unchanged.
- Fresh seven-module CLI: one completed Task, one Run, six Owner documents, one
  local-file result; repeated replay was unchanged. Scripted models were used,
  with actual SQLite/Markdown, tools, checkpoints and file receipts.
- Evidence: `/private/tmp/el-seven-modules-20261024-56dv01ie/summary.json` (old and
  new binary SHA256 included). No business network writes or deployment.
- Integration review found and fixed partial Reviewer-factory cleanup during
  extraction; its regression now passes. No required verification is outstanding.
- GitHub publication targets main through Git Data API; final handoff records
  the verified commit. The server remains pinned to its earlier library version.

## Three top-level responsibilities (2026-10-25)

Status: implemented and locally accepted with the user's explicit layout: app, employee, evals.
The employee framework contains the seven business modules and its kernel,
contracts, ports and internal helpers. app/local is the runnable reference app
and owns every local environment Provider. evals owns scenarios, fixtures and
acceptance reports. No root modules/kernel/ports/adapters/cmd/examples/docs trees
remain beside these three responsibilities; standard root metadata stays.

Move concrete local execution and file-delivery Providers out of the framework,
not only storage/service adapters. Entry receives those through its existing
assembly contract. The local CLI is `go run ./app/local`. Existing behavior,
record/checkpoint versions, effect keys and on-disk directories remain stable.
Go imports change; pinned online consumers are not upgraded or deployed.

Acceptance: exact top-level responsibilities, no employee production imports of
app/evals, actual local Provider assembly, full checks and default CLI scenarios,
old-native-interruption recovery and unchanged completed-directory replay.


Three-root results and acceptance:
- The only non-hidden root directories are app, employee and evals. All seven
  business modules, kernel, ports, contracts, internal helpers and framework docs
  live under employee. There is no extra modules wrapper.
- app/local is a package-main reference executable. Its providers implement
  storage, services/tools, native execution, file delivery, channel mapping and
  optional session storage. Entry receives typed factories; Employee production
  code has no app/evals imports or concrete local Provider construction.
- evals/run.py builds the reference app and exercises six-Owner output,
  local-file receipts, native question/answer continuation, wrong-target rejection,
  cross-process reopen and replay deduplication. The final run passed:
  `/var/folders/l1/212dhfpd5ms5gq470kjk_zr80000gn/T/eel-local-acceptance-zdkebsbh/summary.json`.
- Full `go test ./...`, `go vet ./...` and `go test -race ./...` passed.
- A 94c6675-era binary created a native pending question; the new binary resumed
  its precise target with the original checkpoint and retained both question and
  answer deliveries. Fresh Task/six-Owner execution, unchanged replay, and a
  completed old state directory also passed. Evidence:
  `/private/tmp/el-three-roots-20261025-k9g854jn/summary.json`.
- Local acceptance used scripted models and actual SQLite/Markdown, checkpoint
  and file effects. No real IM/API business writes, consumer upgrades or online
  deployment were performed. Go package imports and CLI paths intentionally move;
  no old-path forwarding wrappers are retained.
- Current diagrams and guides describe these three roots. Historical records
  remain tied to their original versions. GitHub main publication uses Git Data
  API with a verified tree and non-forced ref update.


## Shared design alignment and root docs (2026-10-25)

Status: implemented and source-reviewed; affected checks passed. The user permits
docs at the root and requests alignment
with Multica's core Direct design and the private EL refactor design.

Scope: move framework documentation to root docs, update active links and the
layout guard, and establish one responsibility/ownership/integration contract.
Read the actual pinned Multica dependencies and fixed private EL main/candidate
sources; distinguish design targets, source implementation and deployed behavior.
Do not move private source, platform credentials or internal protocol details into
the public package. Keep exact private-version evidence in the private workspace.

Acceptance: app/employee/evals/docs roots; source dependency direction unchanged;
active document links and affected architecture test pass; a traceable mapping
covers Task/Run/Review, execution, continuation/stop, context/identity, delivery,
Owner and composition. Record equivalent parts and concrete gaps rather than
claiming end-to-end integration. This slice changes documentation/layout checks;
real IM, deployment, consumer dependency upgrades and wire-adapter implementation
are outside this slice. Existing runtime evidence remains version-specific.


Alignment results:
- Root responsibilities are app, employee, evals and docs. Documentation, current
  diagrams, guides and contracts moved out of employee; source imports did not
  change. Historical records retain their original version claims.
- Added specs/host-alignment.md: five business responsibilities mapped onto the
  existing components, with execution inside Task authority and Entry as assembly.
  Foreground Interaction and the package named interaction (Delivery) are now
  explicitly distinguished. Owner processing is not advertised as full Memory.
- The contract identifies three integration choices and forbids two Task/Goal
  authorities for one accepted task. Complete service TaskCreate is distinguished
  from delegated execution. Identity/context/channel/target, physical exit,
  continuation, Outbox, leases and current authorization retain separate meanings.
- WorkingState/input-watermark/Outcome, external Memory, rich delivery and full
  service budget/control protocols are explicit remaining gaps. The private
  evidence matrix records fixed source versions separately from deployed state.
- Current relative links passed across 28 documents; the updated architecture
  guard and `go test ./employee/entry ./app/local` passed. Production Go files are
  unchanged; runtime E2E is not applicable to this docs/test-only slice. Prior
  runtime evidence is retained without claiming fresh online acceptance.
- No consumer upgrade, private remote write, live service interaction or
  deployment was performed. GitHub publication uses the non-forced Git Data API.


## Runnable SDK assembly, eval mode and Provider review (2026-10-25)

Status: implemented; design/source review and Linux local acceptance passed.
Native macOS launch remains limited by observed host AMFI rejection. Acceptance
is design alignment plus actual local startup
and repeatable evaluation. The project is DingTalk IM oriented; renaming the
repository/module is deliberately deferred. Use native Eino Extensions,
middleware, callbacks and subagents with typed Provider composition. Do not clone
a dynamic service container or introduce another business loop.

Scope: app/eval replay mode using the same reference app; reusable default
assembly for another server consumer; role-specific versioned model/effort
Provider selection and frozen journal identity; storage schema/transaction review;
consumer convergence map for Multica and the independent EL repository.
The default app must remain credential-free under replay and work on a portable
Linux build. Actual external deployment has no designated target in this slice;
record build/run evidence separately from remote service acceptance.

Acceptance: local full Task/six Owner/delivery and native continuation/replay run;
new eval mode returns useful failure evidence; role model/effort selection and
restart retain frozen settings; typed modules/native Extensions continue working;
SDK defaults can be embedded without importing a main package; review existing
storage tables and fix only proven blockers. Run appropriate full checks for the
new runtime selection and regression risks. No consumer upgrade, private remote
write, production deployment or real IM send is authorized by this work.


Runnable assembly results:
- app/eval is a pure Go replay/evidence mode that invokes the same local CLI.
  The old Python entry delegates to it. No second Employee business loop exists.
- app/local/providers/defaults.Configure exposes the reference assembly to server
  hosts and respects supplied typed factories. Native extension composition stays
  within Eino types; no dynamic service container, plugin DSL or hot-unload system.
- Frozen Profile.ModelBindings now select Provider/version/model/effort per role;
  old JSON and legacy journal identities remain compatible. Explicit identities
  bind physical model journal requests, including Owner jobs and restart.
- Built and executed a no-base-image Linux/arm64 bundle/container, non-root and
  offline. Final full replay passed 111 assertions. Corrupt input failed clearly;
  previous evidence was preserved. Independent review found one output-directory
  race, reproduced it and verified the atomic exclusive-claim correction.
- Full unit/contract tests, vet and race passed; later eval ownership correction
  passed its affected race checks. Current links passed across 32 documents.
- Native macOS latest child launch was rejected by AMFI (kernel signing logs);
  failed evidence remains preserved. Do not claim that native path passed or
  silently retry unknown effects. Local acceptance uses the Linux container.
- Tables were reviewed with query-plan evidence; no local P1 blocker. Server
  projection, body growth, event-index and migration/fencing gaps remain recorded.
- Two consuming services were compared and a gradual shared-kernel/projection
  route recorded privately. They retain their own Task authority until an explicit
  complete ownership migration. Their dependency/API upgrades and online parity
  are not completed by this SDK change.
- Evidence and limits: evals/reports/2026-10-25-sdk-local-assembly.md. No remote
  environment was selected or deployed; production and actual IM remain untouched.

## Review corrections and internal feedback (2026-10-25)

Status: implemented and locally verified; publication and feedback handoff follow.
User requests a code/functionality review and explicitly
authorizes sending the CR and findings to the internal service maintainer.
Review reproduced one SDK P2: Owner runtime advertised extension AgentAsTool
but rejected its valid nonterminal model call before execution. Correct the
Owner offered-tool validation while preserving terminal/source/authority fences;
exercise actual child invocation, final commit and reopen deduplication.
The internal eval wrapper also allowed binary/fixture flag overrides and ambient
modfile/overlay/replace graph drift. Restrict wrapper flags and build overrides,
verify reviewed SDK/Eino selection, and include the nested module in root checks.

Acceptance: focused Owner/native extension and recovery checks, affected full
checks/local Linux replay, new immutable public SDK version pinned by the internal
CR, wrapper counterexamples rejected, remote source/CR readback. Then send one
review-request message via the verified personal DingTalk profile to the unique
maintainer and verify actual delivery. No merge, deployment or ongoing monitor.


Review correction results:
- Actual Entry probe reproduced the advertised Owner AgentAsTool never being
  invoked. Owner now freezes all offered schemas via ToolsReady, accepts valid
  nonterminal calls, and preserves original strict single terminal/evidence parse.
- New actual child→commit→reopen test proves one child invocation and unchanged
  documents/model counts after replay. Unknown tools, bad arguments, duplicate
  IDs, mixed terminal, invalid citations, name collision and revoked sources are
  blocked. Existing typed module/native extension contracts remain unchanged.
- Full test/vet/race suites passed after the correction. The internal wrapper
  separately restricts args, rejects ambient build flags/all module replacements,
  fixes Eino version and includes the third nested module in root checks.
- Sender is the verified default personal DingTalk/prod profile. Name resolution
  found a human and a similarly named digital employee; existing maintainer
  identity evidence plus exact full-name resolution selects the human. Only a
  dry-run has occurred until the corrected immutable SDK is pinned by the CR.

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

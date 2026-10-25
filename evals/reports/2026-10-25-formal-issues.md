# Formal issue review and correction, 2026-10-25

Candidate: the commit containing this report, based on `9b429be31a5d`.
This is SDK/reference-application acceptance, not deployment or Buddy parity.
The fresh query found nine open reports (#8–#16); earlier empty-backlog records
are historical. Grouped issues are referenced, not automatically closed.

| Issue | Implemented behavior and evidence | Remaining acceptance |
| --- | --- | --- |
| #8 | `serve` reports/aggregates worker errors and continues to the next valid message; fatal storage/output errors still stop it. Real bad→good message regression. | Deployment behavior is not tested. |
| #9 | Shared DWS `--group`/`--ref-sender` syntax; matching SUCCESS/new-message receipt and async query checks; rejects virtual/foreign/reused identities. Fake CLI and installed v1.0.63-beta.1 syntax checks. | No live IM canary; no claim against every CLI version. |
| #10 | Explicit proven-no-effect tool refusals feed native Eino correction; rejected terminal and later same-batch effects do not finish/run. Actual Review, waiting steer and file CAS correction tests. Post-commit ACK/Outbox failures remain hard errors. | Real-model quality remains separate. |
| #11 | Failed/paused/stopped status and optional control acknowledgements use existing idempotent Outbox. | Strict real-model M02 is pending. |
| #12 | Durable job sequence gives per-scene FIFO; legacy timestamp fallback and replay tests. | No distributed scheduler claim. |
| #13 | Context Frame defaults to 128KiB, history20/tasks12/field1KiB; Current appears once; whole-source omissions have gaps; mandatory input is never silently clipped. Bounded task summaries retain routing metadata. | Instructions/later tool results/Owner payload are not a universal token bound. |
| #14 | Budget rejection precedes active-writer cancellation; exhausted Review persists visible pause/reason/next instructions and actual exit proof. | No grant/extend/resume budget API; never automatically adds quota. |
| #15 | Per-scene execution removes global chat serialization. Native Eino middleware propagates cancellation to actual model/tool I/O; default model HTTP timeout is2m. Blocking I/O, recovery and cross-scene tests. | Cancellation requires cooperative Providers; remote physical exit is a separate fact. Event-driven polling enhancement deferred. |
| #16 | Reachable gorilla/websocket advisory GO-2026-6278 fixed with v1.5.3; reference dedupe/echo maps bounded4096/512; echo uses confirmed native IDs. `govulncheck` reports no reachable vulnerability in channel application. | Production persistent Inbox/restart dedupe and real-model prompt/judge quality remain pending. |

Additional P2: diagnostics and immediate Retry reconstruct historical Owner
instances from frozen admitted profiles before any worker tick; exact missing
factories fail explicitly. Distinct namespaces and race regressions cover it.

The changes keep existing Task authority, native Eino Runner/extensions and the
existing Outbox. No dynamic plugin container, second business state machine or
automatic budget grant was added. Context source projection changed the Owner
payload to `current_source_id`; the deterministic reference adapter resolves it
by exact scope/source identity, preserving six-Owner evaluation behavior.

## Verification

After the final ACK correction, full `go test -p 1 ./...`, `go vet ./...` and
`go test -race -p 1 ./...` passed. Native Entry ACK regressions cover Host errors
`NotFound`, `ErrState`, `ErrBudget` and `ErrStale`: one Task, one Run, one model
call, with no correction retry after commit. Control ACK failure is covered too.

A fresh Linux/arm64 bundle from this candidate ran offline (`--network none`) as
UID65532 in a scratch container. All 111 replay assertions passed:
Task/Run/Review, six actual Owner files, verified file receipts, reopening without
duplicate effects and exact native interruption recovery. The evaluator reports
provided-binary provenance as unknown; source identity is recorded separately by
the candidate source manifest, rather than inferred from its executable filename.

- CLI SHA256: `6c791e73bc025cf25988f46ddfcc324cfba98a1bd527d52d4ca2b213bbc6dae2`.
- Eval SHA256: `99ac778599aea439aa54b3cb5a6f3c3116320c8e0ae0c0fb978b5e217f812a8b`.
- Source manifest SHA256 (Go sources/tests, module files, fixtures, bundle recipe):
  `073cc537d0cd6e854e1d04ae7fd7c6a7933ea8163d15fbebef3c95e247ac6f3b`.

Raw summary, command outputs, state and source manifest are retained by the
reviewing developer; no real conversations, credentials or internal source were
published.

The native macOS executable launch previously hit host AMFI signature rejection;
failed evidence was retained. Linux bundle execution is the actual supported
local replay acceptance path here. No deployment, service migration, live model
or IM write occurred in this review. Internal consumer validation is reported in
its separate CR after pinning the immutable published revision.

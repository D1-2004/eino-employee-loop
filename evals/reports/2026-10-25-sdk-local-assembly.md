# SDK local assembly acceptance — 2026-10-25

Scope: DingTalk IM oriented SDK composition, a local replay evaluator, typed
reference Provider defaults and per-role model/effort configuration. Native Eino
Runner/checkpoint/Extensions remain the execution mechanism. No private SDK,
service upgrade, live IM write or production deployment was performed.

## Candidate and accepted environment

The candidate builds on public `a7752bb` and the changes described in the current
[Plan](../../docs/plans/2026-10-24-module-pluggability.md). Runtime evidence records
binary/fixture hashes; a provided binary is honestly labelled source-provenance
unknown. The bundle separately retains `go version -m` build provenance, including
the dirty source state. Publication is a later Git Data API step.

The reproducible runtime acceptance is a **local Linux/arm64 container**, using
only the two static binaries, fixtures and public CA roots. No Go/Python/git are
installed in the image. It runs as UID 65532 with network disabled.

- Final image: `employee-local:20261025-sdk-final`.
- Image manifest list: `sha256:7d05affae7e194fd10f5c1bfece2cc9af7043904dbb9b5e7cd5a83664ed1d882`.
- CLI SHA256: `4a71bd67448d9578c45391b89172cd252437f5708ba5a010f3093d552509901f`.
- Evidence: `/private/tmp/employee-container-final-evidence-20261025/summary.json`.
- Result: both allloops and ask cases passed, with 111 fact assertions.

Verified behavior: a single Task/Run, proven executor exit, Review completion,
real file and package hashes, six Owner projections with original citations,
verified local-file receipt, cross-process state replay without duplicates,
native interruption checkpoint, wrong-target rejection with unchanged state,
exact answer continuation and no repeated question or delivery.

Negative acceptance used malformed input in a copied fixture bundle. The suite
returned exit 1 with passed=false; reusing its output returned exit 2 and preserved
the original summary bytes. Evidence:
`/private/tmp/employee-container-negative-20261025/summary.json`.
A review also reproduced concurrent evaluators mixing an empty output directory;
the retained O_EXCL `.eval-owner` claim now gives a single owner. Race testing
covers 16 concurrent claims for new and existing empty directories.

## Engineering checks

Full `go test -p 1 ./...`, `go vet ./...` and `go test -race -p 1 ./...` passed.
The subsequent output-claim correction passed its affected `app/eval` race suite.
Native extensions, factory overrides and embedding of defaults are covered by
existing Entry/app checks plus an external-package host-open/reopen test.

Per-role binding tests exercised actual native Chat/Executor/Review and Owner
jobs across Entry reopen. The old frozen Provider/version/model/effort survived
new admission settings; a changed effort did not replay another binding's model
response. A local HTTP fixture observed Eino sending the selected model and
reasoning_effort. This is not a real remote-model quality test.

## Observed host limitation and acceptance boundary

Some earlier macOS native Go-eval runs passed. Subsequent native child binaries
were killed before application output; the host kernel logged an AMFI
unrecoverable CT signature issue. The final failed native evidence is retained at
`/private/tmp/employee-native-eval-final-20261025/summary.json` and
`/private/tmp/employee-native-eval-existing-binary-20261025/summary.json`.
Those runs are **failed/unaccepted for native macOS**, not successful negative
cases. No host security policy was changed or bypassed. Linux container results
above are the accepted reproducible local path.

The [storage review](../../docs/reviews/2026-10-25-storage-design.md) found no
proven P1 blocker for the local single-Entry reference. Bounded service queries,
large payload storage, database fencing and migration still gate a production
storage adapter. WorkingState/Outcome, external Memory and rich channel parity
remain explicit integration gaps. Neither existing server consumer was upgraded.

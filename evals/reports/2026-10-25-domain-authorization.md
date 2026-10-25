# Domain model and current authority review

Candidate: the commit containing this report, based on `ffc7e1a`.

The review keeps the existing source layout and one Task aggregate. The
[domain contract](../../docs/specs/domain-model.md) separates entities, application
services and durable reliability records; a new SVG/PNG diagram shows that view.
Host Session assembly, capability merging, full external Memory and Run/Turn
mapping are explicit integration requirements rather than invented wire APIs.

## Narrow runtime correction

Standalone previously checked only its immutable Profile at the shared scope
authorization boundary. `Options.AuthorizeScope` now lets the Host narrow current
access without changing frozen identity/configuration. The callback is never
persisted; Open performs no authority/network preflight. Complete external
backends reject this standalone-only option rather than silently ignoring it.

Borrowed Host clients, including injected Providers, recheck Scope before calls
and before returning data. A committed tool result retains only its actual
receipt plus the error after revocation; it is never relabeled no-effect.
Provider-owned opaque bindings and resource/action authorization stay with the
Provider. Existing source/audience/delivery checks and native Eino execution
remain in place.

## Verification

- Full `go test -p 1 ./...` and `go vet ./...` passed.
- Full Entry `go test -race ./employee/entry -count=1` passed, including existing
  lifecycle, frozen recovery, module replacement and local default contracts.
- Actual native model output is rejected when the Host revokes access before its
  dispatch tool; no Task or executor call is created. Reopening frozen admission
  uses the new Host callback, not an old grant.
- Reference instances for two Owners in the same named scene retain separate
  scope checks. An allowing hook cannot widen any static identity/scene field.
- Injected reads cannot release bytes after revocation. An injected committed
  effect preserves its receipt; a revoked subsequent call never reaches it.
- A fresh Linux/arm64 scratch container ran offline as UID65532: all 111
  replay assertions passed, including six Owners, real file receipts, native
  recovery and unchanged replay. This validates the default nil-hook assembly.
- Diagram rendered successfully and was visually inspected; changed current
  documentation links resolve.

CLI SHA256: `4b88ea9b9d4d1e2f1b9dc35d39bddf72dfb120acaf4736ed6c4b3b570fcfb795`.
Eval SHA256: `5239965e18ad3f37015daea18b407655a8eef71e00e747a9a9043e8d34559745`.
Go sources/tests, module/fixture/bundle source manifest SHA256:
`e71b11d2a061ce367f1755f804440c2b1b2c8102c04f564da86ac31402ca80f1`. Raw summary, state, command outputs and manifest are retained privately.
The evaluator honestly marks a supplied binary's source provenance unknown;
the reviewing candidate/source manifest is separate evidence.

These checks do not establish a deployed Host Session resolver, dynamic group
operators in a single Entry, real connectors/identity delegation, capability
merge, action authorization or live IM. Revocation gates subsequent uses; it
does not prove forced remote process exit. No migration or deployment occurred.

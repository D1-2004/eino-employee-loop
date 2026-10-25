# Local reference application

This directory is a runnable `package main`. It assembles the reusable
[`employee` framework](../../ARCHITECTURE.md) with concrete local Providers.
Other applications import `employee/entry` and supply their own Providers;
they do not import `app/local` as a library.

From the repository root, use a fresh private state directory:

```sh
go run ./app/local -profile evals/cases/local/allloops-profile.json -dir /tmp/el-demo -script evals/cases/local/allloops-script.json replay -in evals/cases/local/allloops-input.ndjson
go run ./app/local -profile evals/cases/local/allloops-profile.json -dir /tmp/el-demo status
```

`-script` substitutes model decisions. Task transitions, SQLite writes, file
execution, artifact verification, Owner evidence and Outbox reconciliation run
through the actual local Providers. A second replay deduplicates the same input;
it must not create replacement tasks or repeat confirmed effects.

`main.go` owns CLI/model setup. `local.go` supplies `DefaultStorage`,
`DefaultExecutor`, `DefaultDelivery`, `BuildServices` and `PrepareTools` to Entry.
`providers/` contains storage, services, execution, delivery, channel and session
file implementations. The employee framework never imports these Providers.

For real models, configure the accepted profile/model and local credentials as
described in the [CLI guide](../../docs/local-entry.md). Local file
receipts do not establish real DingTalk delivery. DWS capture replay maps input
records explicitly; it does not authenticate a platform user or send IM messages.

More: [assembly/replacement](../../docs/module-composition.md),
[behavior contracts](../../docs/specs/README.md),
[evaluation cases](../../evals/README.md).


Replay acceptance runs through `go run ./app/eval`. For another server, use the
importable `providers/defaults.Configure(entry.Options)` assembly or supply your
own typed factories; see [local/eval/server assembly](../../docs/local-server-assembly.md).
Models can vary by loop role and effort through immutable
[model bindings](../../docs/specs/model-composition.md). The module import path is
unchanged while the project name remains under discussion.

Real-model HTTP requests default to a two-minute timeout. Set
`LLM_REQUEST_TIMEOUT=45s` (any positive Go duration) to override it; cancellation
can stop a request sooner. See the [cancellation contract](../../docs/specs/cancellation.md).

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
described in the [CLI guide](../../employee/docs/local-entry.md). Local file
receipts do not establish real DingTalk delivery. DWS capture replay maps input
records explicitly; it does not authenticate a platform user or send IM messages.

More: [assembly/replacement](../../employee/docs/module-composition.md),
[behavior contracts](../../employee/docs/specs/README.md),
[evaluation cases](../../evals/README.md).

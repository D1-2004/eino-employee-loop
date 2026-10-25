# One SDK assembly for local, eval and server hosts

This SDK targets DingTalk IM employees. Native Eino Runner, checkpoints,
Extensions/middleware, callbacks and AgentAsTool provide execution mechanisms;
Task state, source authority and provider receipts remain business contracts.
No second plugin runtime or general service locator is needed.

## Run locally

```sh
go run ./app/local -profile evals/cases/local/allloops-profile.json -dir /tmp/employee-demo -script evals/cases/local/allloops-script.json replay -in evals/cases/local/allloops-input.ndjson
go run ./app/eval
```

Use a fresh directory for an independent local run. Eval uses the same application
binary and actual SQLite/Markdown effects, not a second simulated business loop.
Scripted decisions need no model key, gateway or DingTalk account. Eval produces
machine-readable evidence of Task, Owner, artifacts, receipts, interruption and
replay. Real model quality and remote IM delivery are separate acceptance paths.
On macOS, a `signal: killed` before application output may be an AMFI/code-signing
rejection of a newly built temporary executable; preserve that failed run and
inspect the host signing logs. Do not count it as a case rejection or silently
retry effects. The Linux container path below provides a reproducible alternative
without changing machine security policy.

## Embed in another server

The importable reference assembly is `app/local/providers/defaults`; it is not a
framework dependency. A server can import it deliberately or implement its own
Providers. It never imports the `app/local` main package:

```go
opts := defaults.Configure(entry.Options{
    Directory: privateDirectory,
    Profile: profile,
    Models: modelProviders.Models,
    Modules: moduleFactories,
    Services: hostServices,
})
app, err := entry.Open(ctx, opts)
```

The host checks `err`, owns app.Close, and explicitly chooses Start for background
workers or Drain for bounded evaluation. It authenticates input/identity before
Submit. `Configure` only fills absent default factories; explicitly supplied
Storage, Executor, Delivery, BuildServices and PrepareTools are retained.
`Services` overrides are honored before native tools are built. The shared
assembly does not add another Task authority or Outbox.

Use `Profile.Modules` and the typed registry to select versioned Storage,
Context, Executor, Review, Scheduler and Owner factories. `Profile.ModelBindings`
selects model Provider/version/model/effort by exact loop role. Frozen work keeps
its admitted selection on recovery. A plugin groups Eino middleware, callbacks
and read-only subagents; it does not redefine these service contracts.

## Run the same application on Linux

A bundle has two static binaries, fixtures and public CA roots; no Go compiler,
Python runtime, git checkout or private credentials are required at runtime.

```sh
./app/local/build-bundle.sh /tmp/employee-linux arm64
# On a matching Linux development host, from any working directory:
/tmp/employee-linux/employee-eval --binary /tmp/employee-linux/employee --fixtures /tmp/employee-linux/cases
```

Select `amd64` for an x86 Linux host. The output must be empty; a retained atomic
`.bundle-owner` claim prevents concurrent builds from mixing architectures or
partial artifacts. A failed build keeps its claim; choose a fresh output path. Build provenance is in `provenance.txt`;
evaluation records the binary/fixture hashes. Existing data directories must not
be shared by independent active Entry instances or rebound to another Provider.

The bundle includes a container recipe requiring no base-image download:

```sh
docker build -t employee-local:dev /tmp/employee-linux
docker run --rm --network none --entrypoint /usr/local/bin/employee-eval employee-local:dev --binary /usr/local/bin/employee --fixtures /opt/employee/cases
```

The image runs as a non-root user. Eval creates private scratch state; mount a
writable private volume when running a persistent local worker. Pass model
credentials at runtime for real model use; they are not bundled. This recipe is
a portable reference app, not a deployment of a particular service.

## Two server consumers, one state owner

An existing service can first reuse native Eino invocation, Extensions,
checkpoint and journal mechanisms while retaining its existing task authority,
transactions and Outbox. Context compilation and read-only projections can
converge next. Changing Task write ownership is a later complete route change,
not adding a second Manager below an existing TaskCreate.

A complete independent EL service is an alternative Backend, not a Run executor.
Pin its root/API/engine/provider versions together. Provider IDs, HTTP/local
channels, business identity and delivery targets are separate bindings. The
reference local schema is not a drop-in production schema: bounded ready/due
queries, payload storage, fenced ownership and migrations must match the chosen
server. See the [storage review](reviews/2026-10-25-storage-design.md).

The [Host alignment contract](specs/host-alignment.md) records the remaining
WorkingState/Outcome, Memory and rich delivery gaps. Passing the local path does
not certify full online framework parity.

## Design references

[DSH services](https://github.com/deepseek-ai/deepseek-harness/blob/master/docs/user/develop/framework/service.md)
illustrates capability dependency and readiness; its
[plugin tutorial](https://github.com/deepseek-ai/deepseek-harness/blob/master/docs/user/develop/basic/index.md)
illustrates scoped cleanup. We adopt those principles through typed factories and
owned resource lifetimes, not a clone of Cordis or live hot unload.
[Eino ADK extensions](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/)
provide the native middleware/Agent extension mechanisms used here.

# Eino Employee Loop

[中文](README.zh-CN.md)

An Employee Loop built on [CloudWeGo Eino](https://github.com/cloudwego/eino).
The source tree follows the seven business modules. `app/local` assembles their
default local implementations; a host can replace capability ports or selected
modules without introducing another Task state machine.

```text
modules/
├── chat/          M1  Frontline conversation, admission and business commands
├── task/          M2  Worker, Review, timing and Task/Goal/Run state
├── context/       M3  Authorized context, source references and frozen views
├── owner/         M4  Attention, memory, dreaming, tone, reflection and summary
├── execution/     M5  Atomic Run bridge and native local execution
├── interaction/   M6  Composition, messages, Outbox and delivery receipts
└── entry/         M7  Application lifecycle and module assembly

app/local/             Default local assembly: SQLite + Markdown + Eino
ports/                 External service and persistence contracts
adapters/              Concrete storage, service and channel implementations
kernel/                Reusable Eino runtime and decision APIs
contract/              Shared identity, profile, input and failure values
cmd/el/                Product CLI
examples/              Local replay and optional DingTalk integration examples
```

Run the complete local example from the repository root; use a fresh state directory:

```sh
go run ./cmd/el -profile examples/local-el/allloops-profile.json -dir /tmp/el-demo -script examples/local-el/allloops-script.json replay -in examples/local-el/allloops-input.ndjson
```

This uses scripted model decisions with real local persistence and file effects.
A local-file receipt proves a local write; it does not establish DingTalk delivery
or full behavioral parity with an online Buddy service.

- [Architecture and import migration](ARCHITECTURE.md) — module ownership and dependencies.
- [Default assembly and replacement](docs/module-composition.md) — `app/local`, ports and lifecycle.
- [Local CLI guide](docs/local-entry.md) — replay, tasks, recovery and model setup.
- [Behavior contracts](docs/specs/README.md) — authority, execution, storage, plugins and delivery.
- [Kernel APIs](docs/kernel/README.md) — decision, events, workflow and generation.

MIT licensed. Import paths changed with the module layout; old root paths have no
compatibility wrappers. See the migration table in [ARCHITECTURE.md](ARCHITECTURE.md#import-migration).

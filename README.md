# DingTalk Employee Loop

[中文](README.zh-CN.md)

An Employee Loop SDK for DingTalk IM, with native Agent execution provided by
[CloudWeGo Eino](https://github.com/cloudwego/eino).
Code and evaluation have three roots; documentation is kept separately:

```text
app/                         Runnable reference applications and their Providers
├── local/                   SQLite + Markdown + Eino + local execution/delivery
├── eval/                    Deterministic replay and evidence verification
└── dingtalk/                Lightweight DingTalk channel example

employee/                    Reusable Employee Loop framework
├── chat/                    M1  Frontline conversation and business commands
├── task/                    M2  Worker, Review, timing and Task/Goal/Run state
├── context/                 M3  Authorized context and frozen source views
├── owner/                   M4  Six evidence-based Owner processing roles
├── execution/               M5  Atomic Run bridge and execution contracts
├── interaction/             M6  Composition, Outbox and delivery contracts
├── entry/                   M7  Lifecycle and module assembly
├── kernel/                  Shared Eino runtime and decision APIs
├── ports/                   External service and persistence contracts
├── contract/                Shared identity, profile, input and failure values
└── internal/                Framework implementation helpers

evals/                       Cases, evaluation runner and versioned evidence
docs/                        Architecture, guides and contracts
```

Run the local application from the repository root with a fresh state directory:

```sh
go run ./app/local -profile evals/cases/local/allloops-profile.json -dir /tmp/el-demo -script evals/cases/local/allloops-script.json replay -in evals/cases/local/allloops-input.ndjson
```

Scripted decisions use real local persistence and file effects. Local-file receipts
prove local writes; they do not prove DingTalk delivery or full online Buddy parity.

- [Architecture and import migration](ARCHITECTURE.md)
- [Local, eval and server assembly](docs/local-server-assembly.md)
- [Run the local application](app/local/README.md)
- [Embed and assemble the framework](docs/module-composition.md)
- [Behavior contracts](docs/specs/README.md)
- [Host alignment and integration boundaries](docs/specs/host-alignment.md)
- [Evaluation cases and evidence](evals/README.md)

Import `employee/entry` to embed the framework and supply your own Providers.
`app/local` is an executable, not an application library. MIT licensed.

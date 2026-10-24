# Employee Loop architecture

The physical source tree follows the existing M1–M7 business architecture.
Start at `app/local`, then read the module whose behavior you want to change.
Entry assembles modules and owns application lifetime; the business implementations
live in their corresponding module directories.

![Seven module source boundaries](docs/architecture/module-composition.png)

## Seven business modules

| Module | Directory | Owns | Must not own |
| --- | --- | --- | --- |
| M1 Chat | `modules/chat` | Frontline prompt, tools, input admission and business commands | Another Task store or delivery truth |
| M2 Task | `modules/task` | Worker, Review proposal, timing and attention; `state` owns Task/Goal/Run/Wait/Occurrence transitions | Provider-specific execution state |
| M3 Context | `modules/context` | Source reads, authorized views, citations, frozen frames and revalidation | Authority inferred from model text |
| M4 Owner | `modules/owner` | Six independent owner-processing roles and evidence-cited projections | Business completion or new task authority |
| M5 Execution | `modules/execution` | Atomic Run submission, lookup, poll, cancellation and native local execution | Task/Goal completion decisions |
| M6 Interaction | `modules/interaction` | Expression/package/message composition, durable Outbox, send/lookup receipts | A second Task state machine |
| M7 Entry | `modules/entry` | Profile validation, dependency wiring, registry, startup, shutdown and public application API | Module-specific prompts, tool catalogs or business algorithms |

Task `state` is the single business aggregate. A Review proposes a transition;
only a validated state command commits it. Execution exit and delivery visibility
are separate facts. Provider acceptance alone establishes neither Goal completion
nor message delivery.

## Application, ports and implementations

```text
cmd/el → app/local → modules/entry
                         │ assembles
              M1 chat · M2 task · M3 context
              M4 owner · M5 execution · M6 interaction
                         │ use
                 kernel / ports / contract
                         ↑ implement
                       adapters
```

- `app/local`: the default application recipe. It selects SQLite, Markdown,
  local service readers, Eino models, atomic local execution and file delivery.
  Ordinary callers need no module registry. This composition root supplies
  `DefaultStorage`, `BuildServices` and `PrepareTools`; M7 imports no adapters.
- `ports/services`: Config, Messages, Resources, Tools and Files contracts.
  The execution and delivery ports live beside their business modules.
- `ports/storage`: typed transactional state, checkpoints, leases and artifacts.
  Implementations must preserve atomicity and recovery, not just expose JSON CRUD.
- `adapters/storage/{sqlite,markdown,memory}`: persistent local defaults and an
  independent process-local implementation.
- `adapters/services/local`: local admitted-source and artifact service readers.
- `adapters/services/eino`: native read-only tool-catalog binding to service ports.
- `adapters/channel`: NDJSON and captured-DWS input mapping; identity mapping
  is explicit and is not platform authentication.
- `adapters/sessionfile`: the optional workflow's local session store.
- `contract`: shared Scope, Profile, Input and Failure values. It contains no
  worker, service locator or behavior implementation.
- `kernel`: the shared `Agent` invocation boundary and native tool binding.
  Modules supply their own prompt, tools and current authorization check.
- `kernel/eino`: native Runner, checkpoint/resume and extension composition.
  `kernel/journal` owns physical model call accounting. Neither owns Task state.
- `kernel/{decision,event,workflow,generation}`: reusable SDK capabilities.
  Their existence does not create another product runtime.
- `examples/dingtalk`: optional lightweight channel example (`main.go` and
  `runner`), formerly `cmd/eel` and `local`. The product CLI is `cmd/el`.

Go package names match directory names: `local`, `services`, `context`, `owner`,
`state`, `eino` and `decision`. Examples may explicitly alias imports for clarity,
such as `localapp "…/app/local"`, `host "…/ports/services"` and
`loop "…/kernel/decision"`; these are aliases, not the declared package names.

## Default execution path

1. M7 validates identity/configuration, acquires ownership and opens dependencies.
2. M1 admits the source and uses M3 to build a frozen authorized model view.
3. M1 submits a business command to M2; the same Task aggregate owns all progress.
4. M2 submits an atomic Run through M5 and validates returned execution facts.
5. Review in M2 checks evidence and proposes the next business transition.
6. M6 freezes an intent, then reconciles its actual delivery receipt.
7. M4 processes admitted evidence independently; its projections return through
   M3's audience and source checks before any model can read them.

`app/local` selects concrete defaults once. Factories receive their own typed
dependencies rather than the whole application Options. Storage, Context,
Executor, Reviewer, Scheduler and Owner may be independently selected by exact
registered version; model, service and delivery ports can be injected directly. Advanced hosts calling
`entry.Open` supply their own storage factory, service builder and native tool binder;
Entry does not select concrete adapters on their behalf.
These replacement seams serve the seven modules; they are not additional modules.

## Recovery and online alignment

Frozen source, configuration, stable effect keys and precise module/plugin versions
bind admitted work. Old factories must remain available until their work drains.
There is no arbitrary hot unload, latest-version fallback or automatic migration.
Changing core provider binding requires explicit migration.

Unknown submission/delivery is reconciled at the original provider. An Eino
checkpoint restores cognition; it does not prove an external effect. Current
permission and source/audience checks still apply on recovery.

The service ports align with online responsibilities and critical semantics.
They do not claim wire compatibility with every deployed interface. Real DWS
identity, rich delivery, platform source access and full Buddy budget/wait protocols
require specific adapters and acceptance against the deployed version. Local
mechanism tests and file receipts do not establish that acceptance.

## Import migration

All paths below are relative to `github.com/D1-2004/eino-employee-loop/`.
This is a breaking source-layout change; old root imports have no compatibility
wrappers. Existing consumers pinned to an older revision are unaffected until
upgraded. No online consumer upgrade or deployment is implied.

| Previous import/directory | Current import/directory |
| --- | --- |
| `entry` | `modules/entry` |
| `work` | `modules/task/state` |
| `contextbuilder` | `modules/context` |
| `ownerloop` | `modules/owner` |
| `execution` | `modules/execution` |
| `interaction` | `modules/interaction` |
| `agent` | `kernel/eino` |
| `loop` (including `looptest`, `hosttest`) | `kernel/decision` (same subpackages) |
| `event`, `journal`, `generation` | `kernel/event`, `kernel/journal`, `kernel/generation` |
| `workflow` | `kernel/workflow` |
| `workflow/filestore` | `adapters/sessionfile` |
| `storage` | `ports/storage` |
| `storage/{sqlite,markdown,memory}` | `adapters/storage/{sqlite,markdown,memory}` |
| `host` (contracts) | `ports/services` |
| `host` (local implementation) | `adapters/services/local` |
| `channel` | `adapters/channel` |
| `localapp` | `app/local` |
| `local`, `cmd/eel` | `examples/dingtalk/runner`, `examples/dingtalk` |

Current usage: [default assembly](docs/module-composition.md),
[local CLI](docs/local-entry.md), [behavior contracts](docs/specs/README.md).
Earlier reviews and acceptance evidence are retained under
[docs/archive](docs/archive/README.md) with their original version/path meaning.

# Current behavior contracts

These contracts apply to the current business components and shared runtime. Read
[ARCHITECTURE.md](../../ARCHITECTURE.md) for ownership and physical package names,
and the [assembly guide](../module-composition.md) for Go usage.

| Contract | Owner |
| --- | --- |
| [Domain model and Host assembly](domain-model.md) | Entities, services, durable records and current-scope authorization |
| [Correctable tool refusals](tool-feedback.md) | Explicit no-effect rejection and native Eino correction |
| [Task feedback](task-feedback.md) | Review validation, control acknowledgements and visible status |
| [Context bounds](context-bounds.md) | Serialized Frame limits, source gaps and bounded summaries |
| [Chat concurrency](chat-concurrency.md) | Persistent FIFO and per-scene execution |
| [Cancellation](cancellation.md) | Actual cooperative I/O cancellation and model HTTP timeout |
| [Model composition](model-composition.md) | Frozen per-role Provider/model/effort and native model journals |
| [Prompt composition](prompt-composition.md) | Frozen role instructions, Host provenance and model-visible configuration projection |
| [Host alignment and state ownership](host-alignment.md) | Business responsibilities, integration choices and explicit parity gaps |
| [Local runtime](local-el-runtime.md) | M7 lifecycle and the M1–M6 collaboration |
| [Module composition](module-composition.md) | M7 exact-version assembly and resource lifetime |
| [Whole TaskManager Provider](task-manager-provider.md) | Host-owned Task/Run/Outbox authority and original-operation recovery |
| [Audience isolation](audience-isolation.md) | M3 Context and M4 Owner evidence |
| [Executor](executor-port.md) | M5 atomic physical Run boundary |
| [Delivery envelopes](delivery-envelope.md) | M6 Outbox and provider receipt stages |
| [Host services](host-services.md) | `employee/ports/services` and trusted host adapters |
| [State and artifacts](storage-ports.md) | `employee/ports/storage` and storage adapters |
| [Plugins](plugins.md) | M7 registry and native Eino extension composition |

A contract is not an online acceptance record. Version-specific reviews and
validation results remain in [evals](../../evals/README.md); historical framework
plans remain in [the archive](../archive/README.md).

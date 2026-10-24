# Current behavior contracts

These contracts apply to the current seven-module implementation. Read
[ARCHITECTURE.md](../../ARCHITECTURE.md) for ownership and physical package names,
and the [assembly guide](../module-composition.md) for Go usage.

| Contract | Owner |
| --- | --- |
| [Local runtime](local-el-runtime.md) | M7 lifecycle and the M1–M6 collaboration |
| [Module composition](module-composition.md) | M7 exact-version assembly and resource lifetime |
| [Audience isolation](audience-isolation.md) | M3 Context and M4 Owner evidence |
| [Executor](executor-port.md) | M5 atomic physical Run boundary |
| [Delivery envelopes](delivery-envelope.md) | M6 Outbox and provider receipt stages |
| [Host services](host-services.md) | `ports/services` and trusted host adapters |
| [State and artifacts](storage-ports.md) | `ports/storage` and storage adapters |
| [Plugins](plugins.md) | M7 registry and native Eino extension composition |

A contract is not an online acceptance record. Version-specific reviews and
validation results remain in [the archive](../archive/README.md).

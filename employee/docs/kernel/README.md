# Reusable kernel APIs

These APIs can be embedded independently of the default Employee Loop application.
For the seven business modules and default local assembly, start at the
[repository README](../../../README.md).

| API | Source | Guide |
| --- | --- | --- |
| Single-wake decision | `employee/kernel/decision` | [Host](host.md), [integration](integration.md) |
| Event classification and routing | `employee/kernel/event` | [Events](events.md) |
| Optional multi-turn workflow | `employee/kernel/workflow` | [Workflows](workflows.md) |
| Structured decision graph | `employee/kernel/generation` | [Generation](generation.md) |
| Lightweight DingTalk example | `app/dingtalk` | [Local example](local.md) |

中文：[事件](events.zh-CN.md)、[Host](host.zh-CN.md)、
[集成](integration.zh-CN.md)、[钉钉例子](local.zh-CN.md)。

The old [English](overview.md) / [Chinese](overview.zh-CN.md) overviews are retained
as historical SDK descriptions. Their original path references describe the
previous source layout; the [migration table](../../../ARCHITECTURE.md#import-migration)
is authoritative for current imports.

# 与平台集成

[English](integration.md)

平台可以在三个深度上采用 Employee Loop。每个深度都把平台的业务逻辑留在平台一侧：本模块只负责排序、校验和路由决策。

- [选择深度](#choosing-a-depth)
- [深度 1：内核](#depth-1-the-kernel)
- [深度 2：场域 Hub 加平台端口](#depth-2-the-scene-hub-with-platform-ports)
- [深度 3：服务](#depth-3-the-services)
- [示例：Multica](#example-multica)
- [检查清单](#checklist)

<a id="choosing-a-depth"></a>
## 选择深度

| 深度 | 平台保留 | 本模块提供 | 入口 |
| --- | --- | --- | --- |
| 1. 内核 | 受理、会话、日志、送达、工具、上下文 | 一次唤醒：模型循环、批次校验、预算、终止决策 | `kernel.Run` |
| 2. Hub | 工具、上下文、Host、存储实现 | 受理、路由、会话、领取顺序、围栏、发件箱流程 | `scene.Hub` + `scene.KernelRunner` |
| 3. 服务 | 网关与执行器 | 通过 HTTP 提供完整的 Loop | `cmd/employee-loop`、`transport/httpapi` |

<a id="depth-1-the-kernel"></a>
## 深度 1：内核

平台把一切注入到一次唤醒中：

```go
res, err := kernel.Run(ctx, kernel.Options{
    Model:           chatModel,      // 任意 Eino model.ToolCallingChatModel
    Host:            platformHost,   // contract.Host：执行每个工具，负责回执
    Tools:           specs,          // 已解析说明的 []contract.ToolSpec
    Budget:          contract.Budget{MaxResponses: 8, MaxConsecutiveErrors: 3, MaxAttempts: 11},
    RequireTerminal: true,
    Validator:       platformBatchRules, // 可选 contract.BatchValidator
    TextGuard:       platformTextRules,  // 可选 contract.TextGuard
    FinalText:       platformFinalText,  // 可选 contract.FinalTextHost
    Permanent:       isPermanentProviderError,
    Observer:        tracing,            // kernel.Observer
}, kernel.Wake{
    Identity:   contract.Identity{Tenant: t, Employee: e, Scene: scene, WindowID: jobID},
    Transcript: frozenMessages,      // 或 Instructions + Blocks
})
```

- **上下文。** 可以传入平台已渲染好的 `Transcript`（恰好一条 system 消息，且在最前），也可以传入 `Instructions` 加 `ContextBlock`，由内核以带本地化标签的 user 角色数据渲染。上下文块从不授予权限。
- **工具。** 把平台工具转换为 `contract.ToolSpec`：`kind` 取决于工具是否提交效果或结束唤醒，`parameters` 为 JSON Schema，需要时设置 `exclusive`。希望由平台自己的截止时间控制时设置 `timeout`。
- **Host。** `Execute(ctx, identity, call)` 必须根据身份（从不根据参数）检查权限，按身份加原生调用 id 对效果去重，并为每个已提交的效果返回回执。
- **持久性。** 每次领取在 `contract.Journal` 之上创建 `kernel.NewWakeJournal(journal, lease)`，用它的 `Model` 和 `Host` 包装模型与 Host，并从它的 `FrozenTranscript` 开始重新领取的唤醒；或保留平台自己的日志，并在 Host 与模型适配器中重放。
- **离线测试。** `kernel/kerneltest` 编排模型响应；`kernel/example_test.go` 中的 `ExampleRun` 是一个带平台工具的完整唤醒。

<a id="depth-2-the-scene-hub-with-platform-ports"></a>
## 深度 2：场域 Hub 加平台端口

平台实现存储端口（`EventLog`、`SessionQueue`、`Journal`、`Outbox`；见 [storage.zh-CN.md](storage.zh-CN.md)），并把自己的 Host 接入 `KernelRunner`：

```go
runner := &scene.KernelRunner{
    Model:    chatModel,
    Hosts:    func(win contract.Window) contract.Host { return platform.HostFor(win) },
    Tools:    platformSpecs,          // 由 ToolsFor 按通道和会话筛选
    Journal:  journal,
    Persona:  trustedInstructions,
    Context:  platform.FreezeContext, // 记忆、历史、资源，作为上下文块
    Budgets:  map[contract.Lane]contract.Budget{contract.LaneBackground: {MaxResponses: 2, MaxConsecutiveErrors: 2, MaxAttempts: 3}},
    Messages: bundle,                 // i18n 覆盖
}
hub := &scene.Hub{Employee: id, Catalog: catalog, Log: log, Queue: queue, Outbox: outbox,
    Runner: runner, OnHostEvent: platform.ApplyHostEvent}
worker := &scene.Worker{Hub: hub, Deliverer: platform.Channel, Concurrency: 4}
```

- 把平台输入作为事件发布（`hub.Publish`），标准目录缺少的类型自行注册（[events.zh-CN.md](events.zh-CN.md)）。
- `OnHostEvent` 在不唤醒模型的情况下应用 host 通道的事实（回执、终态事实、修订）。它必须对同一回执幂等：受理没有完成时，重复发布的事件会再次执行它。
- `SessionQueue.Complete` 在同一个受围栏保护的事务中写入窗口的投递，请在持有发件箱的存储上实现它。
- `toolhost` 可以在平台工具旁提供标准工具：提供 `toolhost.Host.Specs()` 加上平台自己的规格，并在绑定的 Host 中按名字分派调用。
- `scene/example_test.go` 中的 `ExampleKernelRunner` 是可运行的版本。

<a id="depth-3-the-services"></a>
## 深度 3：服务

运行 `cmd/employee-loop`（或装配 `employeeloop.New`），并让平台同时作为网关（向 `POST /v1/events` 发布，提供 `POST /v1/deliveries`）和 Task Manager（提供 `/v1/tasks...`，回发 `execution.*` 事件）接入。见 [services.zh-CN.md](services.zh-CN.md)。内存存储使这个深度适合单进程部署；Loop 端口的持久化实现通过 `employeeloop.Config.Ports` 传入（参考 DDL 见 [storage.zh-CN.md](storage.zh-CN.md)）。一个 Task Manager 可以服务多个 Loop：它的事件带 `employee`，经 `contract.SinkResolver` 路由。

<a id="example-multica"></a>
## 示例：Multica

[Multica](https://github.com/multica-ai/multica) 的 EmployeeLoop 在深度 1 上嵌入本模块。它的 `server/internal/service/einoengine` 用 Multica 冻结的对话记录、工具注册表、持久化模型和 Host 在 `kernel.Run` 上运行一次唤醒；受理、日志、提示词装配和送达都留在 Multica。按 Agent 的设置（`employee_loop_engine`：`multica` 或 `eino`）为新的唤醒选择内核；唤醒会冻结自己的引擎。该设置以及宣告其变化的事件都归 Multica 所有；本模块没有引擎切换。

### 作业种类到事件类型

Multica 的作业（`server/internal/employeeentry`）为了追踪被分类为契约事件（`einoengine.Classify`）。`plan.step_completed` 和 `collection.ready` 是 Multica 自己的扩展类型，注册在它的目录上，文案在它自己的语言包中（形如 [`examples/hostext`](../examples/hostext)）：

| Multica 作业种类 | 契约事件类型 | 通道 / 会话 |
| --- | --- | --- |
| `message` | `message.addressed` | foreground / interactive |
| 来源事件为原生主动群聊事件的 `message` | `message.observed` | background / observation |
| `human_response` | `human.response` | foreground / interactive |
| `task_wake`（`execution.result`） | `execution.result` | background / follow_up |
| `task_wake`（`execution.progress`） | `execution.progress` | background / follow_up |
| `task_wake`（`execution.follow_up`） | `plan.step_completed` | background / follow_up |
| `task_wake`（`collection.ready`） | `collection.ready` | background / follow_up |
| `task_wake`（`routine.decision`、`routine.group_news`、`webhook.decision`） | `routine.due` | background / routine |
| `task_wake`（`routine.group_proactive`） | `observation.due` | background / observation |
| `task_event_batch` | `execution.progress`，有结果项时为 `execution.result` | background / follow_up |
| `group_observation` | `observation.due` | background / observation |
| 其他 `task_wake` 种类 | `task_wake.<kind>`，不在目录中，因此不会路由 | - |

### 组件到服务

| 服务 | Multica 组件 |
| --- | --- |
| Event Gateway | 钉钉原生订阅与入站处理（`handler/dws_native_*`、`dwseventsource`）、A2A 入口；通过 `employeedelivery` 以及结果/通知发送器送达 |
| Employee Loop | `employeeentry`（受理、类型化的场域作业、租约）、场域入口 Worker（`handler/employee_scene_entry_worker.go`）、`service/employeeloop`（Host 契约与内置内核）或 `service/einoengine`（本内核）、员工记忆与上下文构建 |
| Task Manager | `employeetask`（Employee Task、执行、纠正）、任务唤醒的生产者（`handler/employee_task_wake.go`）以及执行任务的运行时（daemon、云沙箱） |

名称指撰写时的 Multica 仓库；该映射说明的是意图，不是稳定的 API。

<a id="checklist"></a>
## 检查清单

- [ ] 场域 id 是生成的，从不是提供方的原始会话 id。
- [ ] 行为者来自已认证的数据；模型从不提供身份或权限。
- [ ] 每个效果工具都返回持久回执，并按身份加原生调用 id 保持幂等。
- [ ] 工具结果是机器字段；用户可见文本来自 i18n 语言包或模型的回复。
- [ ] Task Manager 只通过发布事件回报。
- [ ] Deliverer 只在明确拒绝时包装 `contract.ErrDeliveryRejected`；`unknown` 状态的投递通过状态查询核对（`contract.DeliveryReconciler`、`Hub.Reconcile`），不重发。
- [ ] 自定义类型、会话和工具的语言包在每个随附语言中都存在（[i18n.zh-CN.md](i18n.zh-CN.md)）。
- [ ] 行为可以用 `kerneltest` 模型和假实现离线覆盖。

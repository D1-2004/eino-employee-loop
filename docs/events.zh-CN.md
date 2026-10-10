# 事件

[English](events.md)

Employee Loop 的每个输入都是一个 `contract.Event`。本文说明事件如何分类和路由。从目录生成的标准类型完整列表见 [catalog.zh-CN.md](catalog.zh-CN.md)；线上格式见 [schema/event.schema.json](../schema/event.schema.json)。

- [信封](#the-envelope)
- [分类](#classification)
- [路由不变量](#routing-invariants)
- [引用](#references)
- [合窗](#coalescing)
- [标准类型的生产者](#producers-of-the-standard-types)
- [Host 扩展配置](#host-extension-profile)
- [自定义事件类型](#custom-event-types)

<a id="the-envelope"></a>
## 信封

| 字段 | 必填 | 含义 |
| --- | --- | --- |
| `id` | 是 | 在生产者内稳定。重放 `(source, id)` 时内容必须相同。 |
| `source` | 是 | 生产者命名空间，例如 `dingtalk.im`、`local.stdin`、`task-manager`。 |
| `employee` | 否 | 接收该事件的员工的 Loop。为空表示接收它的 Loop；Loop 拒绝其他员工的事件（`unknown_employee`）。服务多个员工的生产者（共享的 Task Manager）要设置它，以便 `contract.SinkResolver` 路由事件。 |
| `type` | 是 | 选择 `TypeSpec`，例如 `message.addressed`。 |
| `class`、`mode` | 否 | 受理时从目录填入；生产者给出的值与目录不一致会被拒绝。 |
| `scene` | 依类型 | `SceneRef{id, kind, tenant, provider, locator}`。`id` 是唯一的状态键；`locator` 是不透明的提供方身份。 |
| `actor` | 依类型 | 来自已认证数据的 `Actor{id, kind, display_name}`。`kind` 为 `human`、`employee`、`system` 或 `integration`。 |
| `occurred_at` | 是 | RFC 3339 时间。 |
| `locale` | 否 | 人类内容的 BCP 47 标签；决定唤醒使用的语言。 |
| `refs` | 否 | 关联引用（见[引用](#references)）。 |
| `text` | 否 | 人可见的内容。 |
| `payload_schema`、`payload` | 否 | 生产者自定义数据及其 schema 名，例如 `taskmgr.execution/v1`。 |

网关必须生成场域 id（例如 `gateway/dws.HashSceneResolver` 对命名空间和会话 id 做哈希），绝不能把提供方的原始会话 id 当作 `scene.id`。

<a id="classification"></a>
## 分类

每个类型由两个相互独立的问题分类。

**类别：发生了什么。**

| 类别 | 覆盖 |
| --- | --- |
| `business` | 场域业务：消息、表情回应、回答、成员变化、提供方资源（文档、日程、审批）。 |
| `execution` | 在途工作：已受理、已开始、进展、被纠正。从不声称工作已完成。 |
| `completion` | 终态事实：一次执行的结果、终止状态（平台的扩展类型中还有例如完成的计划步骤、关闭的征集）。 |
| `schedule` | 时间：例行任务到点、旁听窗口到期、提醒与截止时间。 |
| `enablement` | 能力与生命周期：启用、停用、绑定到场域、订阅或策略变化。 |

**模式：员工为什么收到它。**

| 模式 | 又称 | 含义 |
| --- | --- | --- |
| `triggered` | 被动触发 | 指向员工，或汇报员工自己的工作：@ 提及、私聊、回复员工的消息、回答员工的问题、员工的任务结束。 |
| `listening` | 主动监听 | 因为员工订阅、旁听或计时而收到：没有指向员工的群消息、提供方推送、例行任务、旁听窗口。 |

标准目录在这张表中的分布如下（每格列出类型；每个选项见 [catalog.zh-CN.md](catalog.zh-CN.md)）：

| | triggered（被动触发） | listening（主动监听） |
| --- | --- | --- |
| business | `message.addressed`、`message.follow_up`、`human.response`、`reaction.added` | `message.observed`、`member.changed`、`resource.changed` |
| execution | `execution.accepted`、`execution.started`、`execution.progress`、`execution.steered` | - |
| completion | `execution.result`、`execution.terminal` | - |
| schedule | - | `routine.due`、`observation.due` |
| enablement | `employee.enabled`、`employee.disabled`、`subscription.changed`、`policy.changed`、`scene.bound` | - |

<a id="routing-invariants"></a>
## 路由不变量

`TypeSpec.Validate` 与 `Catalog.Route` 强制：

1. 只有**被动触发的业务**事件进入**前台**，且总在 `interactive` 会话中，行为者必须是 `human`。
2. **主动监听**事件从不进入前台。
3. 前台窗口从不可抢占。
4. **host 通道**类型没有会话；前台和后台类型总有会话，后台类型使用非交互的会话种类。
5. 会话通道要求场域（`requires_scene: true`）；只有 host 通道类型可以省略。
6. 带 `session_key` 的类型按该引用划分会话；缺少该引用的事件会被拒绝。
7. 未知类型被拒绝（`ErrUnknownType`）：Loop 从不猜测路由。生产者的 `class`/`mode` 必须与目录一致。
8. `coalesce` 要求 `max_items > 0` 且 `0 <= quiet_ms <= max_wait_ms`；每个类型都有 `summary_key`（i18n 键）。

错误对应线上错误码：`invalid_event`、`unknown_type`、`unknown_scene`（HTTP 400），`unknown_employee`（HTTP 404），`conflict`（HTTP 409）；见 [services.zh-CN.md](services.zh-CN.md#errors)。

<a id="references"></a>
## 引用

`refs` 把事件与其他对象关联起来。常用键（`contract.Ref*`）：

| 键 | 使用者 | 含义 |
| --- | --- | --- |
| `task` | `execution.*`（以及任务流程的扩展类型） | 任务引用；`follow_up` 的会话键。 |
| `run` | Task Manager 事件 | 执行引用；memtask 使用 `<task>.r<N>`。 |
| `routine` | `routine.due` | 例行任务引用；`routine` 的会话键。 |
| `reply_to` | 业务消息 | 事件所回复的提供方消息。 |
| `window` | Host 事件 | 事件所指向的窗口。 |

引用只是数据。身份与权限来自 `actor` 和 Host，从不来自引用或正文。

<a id="coalescing"></a>
## 合窗

带 `coalesce` 规则的类型让同一会话的突发事件共享一次唤醒。memstore 按以下方式实现（持久化存储必须一致）：

- 第一个事件打开窗口，`due_at = now + quiet_ms`。
- 同一会话之后的事件，在窗口少于 `max_items` 项且 `now < opened_at + max_wait_ms` 时加入该打开的窗口；随后 `due_at = min(now + quiet_ms, opened_at + max_wait_ms)`。
- 窗口在达到 `max_items` 时封口（`ready`），或在领取时发现 `due_at` 已过而封口。
- 类型没有规则（或 `max_items <= 1`）的事件总是开启自己的就绪窗口，即使会话已有打开的窗口。

标准规则：被找和追问的消息、执行进展和结果合窗 250 ms（最多 1 秒、16 项）；被旁听的群消息合窗 12 秒（最多 30 秒、20 项）。

<a id="producers-of-the-standard-types"></a>
## 标准类型的生产者

| 生产者 | 类型 |
| --- | --- |
| Event Gateway（`gateway/dws`、`gateway/stdin`） | `message.addressed`、`message.observed`、`reaction.added` |
| Host（平台） | `message.follow_up`、`human.response`、`member.changed`、`resource.changed`、时间与启用类类型 |
| Task Manager（`taskmgr`，source `task-manager`） | `execution.accepted`、`execution.started`、`execution.progress`、`execution.steered`、`execution.result`、`execution.terminal` |

`gateway/dws` 在钉钉消息是私聊、提及员工（提供方 id、提供方的提及标记或正文中的 `@名字`）或回复员工的消息时，将其归为 `message.addressed`；其他群消息归为 `message.observed`；对员工消息的负面表情回应归为 `reaction.added`。`gateway/stdin` 把每一行输入都发布为 `message.addressed`。

<a id="host-extension-profile"></a>
## Host 扩展配置

本模块没有实现的流程（计划、征集、提醒、配置切换）所用的类型属于嵌入平台，而不属于本模块：本模块中没有组件产生它们，因此标准目录和 Loop 的 i18n bundle 都不包含它们。平台在自己的目录上注册类型（`Catalog.Register`），通过 `toolhost.WithCatalog` 和自己的 `contract.Host` 提供工具，并在自己的语言包中提供文案（`i18n.Extend`）。[`examples/hostext`](../examples/hostext) 是这样做的示例配置：`plan.step_completed`、`collection.ready`、`task.reminder` 以及工具 `continue_plan`、`describe_capabilities`、`scene_config_get`、`create_collection` 和 `set_scene_participation`，带自己的 `plan` 引用（`hostext.RefPlan`）和 `hostext.*` 键。

<a id="custom-event-types"></a>
## 自定义事件类型

在装配时注册类型：

```go
cat := contract.StandardCatalog()
err := cat.Register(contract.TypeSpec{
    Type: "ticket.assigned", Class: contract.ClassBusiness, Mode: contract.ModeTriggered,
    Lane: contract.LaneBackground, Session: "ticket", SessionKey: "ticket",
    RequiresScene: true, SummaryKey: "event.ticket.assigned",
})
```

- 用上面的两个问题选择类别和模式；通道由不变量决定。
- 新的后台会话种类可以是任意字符串；在 i18n 语言包中加入 `session.<kind>`，使用 `KernelRunner` 时再加入 `scene.session.<kind>`（会话角色）。
- 在每个随附的语言中加入 `summary_key` 的文本（见 [i18n.zh-CN.md](i18n.zh-CN.md)）。
- `Register` 会替换同名的已有类型；标准名称请保留标准含义。
- [schema/type-spec.schema.json](../schema/type-spec.schema.json) 描述了以数据形式维护目录时的 `TypeSpec`。

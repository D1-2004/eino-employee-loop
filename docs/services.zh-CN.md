# 服务与线上协议

[English](services.md)

Employee Loop 是三个服务之一。它们可以在同一进程中运行，也可以分开运行；跨边界的每个值都是 `contract` 中的普通 JSON 类型，由 `transport/httpapi` 通过 HTTP 传输。每种消息体的 JSON Schema 见 [schema/](../schema/)。

```
Event Gateway ──Event──► Employee Loop ──DispatchRequest──► Task Manager
      ▲                       │  ▲                               │
      └──────Delivery─────────┘  └──────────Event (execution.*)──┘
```

- [边界](#boundaries)
- [接口](#endpoints)
- [错误](#errors)
- [认证与限制](#authentication-and-limits)
- [线上幂等](#idempotency-on-the-wire)
- [Task Manager 事件序列](#task-manager-event-sequence)
- [部署拓扑](#deployment-topologies)
- [命令](#commands)

<a id="boundaries"></a>
## 边界

| 服务 | 拥有 | 暴露 | 调用 |
| --- | --- | --- | --- |
| Event Gateway | 提供方连接、场域解析、提供方消息 id | `contract.Deliverer`（`POST /v1/deliveries`）、`contract.DeliveryReconciler`（`POST /v1/deliveries/status`） | Loop 的 `EventSink` |
| Employee Loop | 受理、会话、窗口、唤醒、日志、发件箱、记忆、会话日志 | `contract.EventSink`（`POST /v1/events`） | `TaskManager`、`Deliverer` |
| Task Manager | 任务、执行、执行器、自己的事件发件箱 | `contract.TaskManager`（`/v1/tasks...`） | Loop 的 `EventSink` |

保持边界干净的规则（由 `internal/archtest` 以白名单方式强制：每个包都必须匹配一条规则，新的顶层包在有规则允许之前不能被 Loop 引用）：

- `contract` 不引用本模块的任何包；`i18n` 只引用 `contract`。
- Loop 包（`kernel`、`scene`、`toolhost`、`store`、`employeeloop`）只引用彼此、`contract` 和 `i18n`。
- 传输层只引用 `contract`；网关只引用 `contract` 和 `i18n`；任务管理器只引用 `contract`、`i18n` 和 `taskmgr`。
- 每个需要输出文字的包拥有自己的 i18n 语言包；Loop 的默认语言包只含 Loop 的文字（见 [i18n.zh-CN.md](i18n.zh-CN.md)）。
- Task Manager **只**通过向 Loop 发布事件来回报。Loop 从不轮询它的结果；只有工具需要时才读取任务状态（`Get`、`Find`）。
- 每个 Task Manager 事件都带 `employee`，因此一个 Task Manager 可以服务多个 Loop：`contract.SinkResolver` 把事件路由到对应员工的 Loop，Loop 拒绝其他员工的事件（`unknown_employee`）。

<a id="endpoints"></a>
## 接口

| 方法与路径 | 请求 | 成功 | 服务 |
| --- | --- | --- | --- |
| `POST /v1/events` | [Event](../schema/event.schema.json) | `202` [EventAck](../schema/event-ack.schema.json) | Loop |
| `POST /v1/tasks` | [DispatchRequest](../schema/dispatch-request.schema.json) | `200` [TaskReceipt](../schema/task-receipt.schema.json) | Task Manager |
| `GET /v1/tasks/{ref}?employee=` | - | `200` [TaskView](../schema/task-view.schema.json) | Task Manager |
| `GET /v1/tasks?employee=&scene=&requester=&text=&limit=` | - | `200` [TaskList](../schema/task-list.schema.json) | Task Manager |
| `POST /v1/tasks/{ref}/steer` | [TaskControl](../schema/task-control.schema.json)（`text` = 纠正内容） | `200` TaskReceipt | Task Manager |
| `POST /v1/tasks/{ref}/stop` | TaskControl | `200` TaskReceipt | Task Manager |
| `POST /v1/tasks/{ref}/continue` | TaskControl（`text` = 新提示，可选） | `200` TaskReceipt | Task Manager |
| `POST /v1/deliveries` | [Delivery](../schema/delivery.schema.json) | `200` [DeliveryAck](../schema/delivery-ack.schema.json)；网关没有发送时 `422 delivery_rejected` | Gateway |
| `POST /v1/deliveries/status` | Delivery | `200` [DeliveryStatus](../schema/delivery-status.schema.json)；从不发送 | Gateway |
| `GET /healthz` | - | `200 {"status":"ok"}` | 全部 |

细节：

- `EventAck.lane` 报告事件去向；会话通道会设置 `window_id`；重放时 `duplicate: true`。
- `Event.employee` 指明接收事件的 Loop。为空表示"接收它的那个 Loop"；Loop 对其他员工的事件返回 `404 unknown_employee`。
- 在控制接口上，消息体中为空的 `task_ref` 由路径填入；不一致时返回 `400 invalid_request`。
- `GET /v1/tasks` 要求 `employee` 与 `scene`；`limit` 必须是非负整数（memtask 默认 20，上限 200）。列表按新到旧排列，从不为 `null`。
- Task Manager 事件带 `payload_schema: "taskmgr.execution/v1"` 和 [ExecutionPayload](../schema/execution-payload.schema.json)。

每个处理器（`httpapi.NewEventHandler`、`NewTaskManagerHandler`、`NewDeliveryHandler`）包装契约接口，每个客户端（`NewEventSinkClient`、`NewTaskManagerClient`、`NewDelivererClient`）实现该接口，因此服务可以在进程内和远程对端之间切换而无需改代码。

<a id="errors"></a>
## 错误

错误格式为 `{"code": "...", "message": "..."}`（[Error](../schema/error.schema.json)）。每个错误码对应一个哨兵错误，因此 `errors.Is` 可以跨网络使用（`httpapi.ErrorCodes`）：

| 码 | HTTP | 哨兵错误 | 典型原因 |
| --- | --- | --- | --- |
| `invalid_event` | 400 | `contract.ErrInvalidEvent` | 缺少信封字段、class/mode 不一致、前台事件没有人类行为者、缺少会话引用 |
| `unknown_type` | 400 | `contract.ErrUnknownType` | 类型不在 Loop 的目录中 |
| `unknown_scene` | 400 | `contract.ErrUnknownScene` | 类型要求场域但 `scene.id` 为空 |
| `unknown_employee` | 404 | `contract.ErrUnknownEmployee` | 事件指向的员工不由该入口服务 |
| `invalid_request` | 400 | `contract.ErrInvalidRequest` | 缺少 Task Manager 字段、`limit` 非法、路径与消息体引用不一致；Loop 把它作为拒绝码 `invalid_request` 交还模型 |
| `delivery_rejected` | 422 | `contract.ErrDeliveryRejected` | 网关确定没有发送该投递（没有 locator、不属于本渠道的场域） |
| `bad_request` | 400 | `httpapi.ErrBadRequest` | JSON 格式错误 |
| `unauthorized` | 401 | `httpapi.ErrUnauthorized` | 缺少或错误的 bearer 令牌 |
| `forbidden` | 403 | `contract.ErrForbidden` | 非请求人或跨场域的控制 |
| `not_found` | 404 | `contract.ErrNotFound` | 未知任务（或其他员工的任务） |
| `conflict` | 409 | `contract.ErrConflict` | 幂等键或事件 id 被用于不同内容 |
| `task_state` | 409 | `contract.ErrTaskState` | 对已结束任务纠正/停止，对活跃任务继续 |
| `stale_revision` | 409 | `contract.ErrStaleRevision` | `expected_revision` 不是任务当前版本（在幂等键之后检查） |
| `lease` | 409 | `contract.ErrLease` | 租约已过期 |
| `too_large` | 413 | `httpapi.ErrTooLarge` | 消息体超过限制 |
| `quota` | 429 | `contract.ErrQuota` | 提供方或记忆配额 |
| `unavailable` | 503 | `contract.ErrUnavailable` | Task Manager 已关闭或正在停机；Loop 会重试该窗口 |
| `internal` | 500 | `httpapi.ErrRemote` | 其他情况 |

客户端重试建议：`400`、`401`、`403`、`404`、`409`、`413` 和 `422` 不改内容不可能成功；`429`、`503`、`500` 和传输错误可以退避重试，**且使用同一个幂等键或事件 id**。投递是例外，从不重试：`DelivererClient` 把 `delivery_rejected`、`bad_request`、`unauthorized` 和 `too_large` 视为明确拒绝（`contract.ErrDeliveryRejected`，投递变为 `failed`）；其他失败都让投递保持 `unknown`，直到状态查询给出结论。`memtask` 把 Loop 返回的 `invalid_event`、`unknown_type`、`unknown_scene`、`unknown_employee` 和 `conflict` 视为永久错误并跳过该事件；网关的 `gateway/dws.Permanent` 也这样处理。

<a id="authentication-and-limits"></a>
## 认证与限制

- 可选的共享 bearer 令牌（`httpapi.WithToken`，命令中的 `-token` 或 `EEL_TOKEN`）保护除 `/healthz` 外的所有接口。处理器以常量时间比较，失败时返回 `401` 并带 `WWW-Authenticate: Bearer`。
- 请求体默认限制为 1 MiB（`httpapi.WithMaxBody`）；客户端最多读取 16 MiB 的响应。
- 消息体中的身份（`actor`、`requester`）是调用方担保的数据。请把服务部署在私有网络中，或放在认证调用方的网关之后；令牌是共享密钥，不是按调用方的授权。

<a id="idempotency-on-the-wire"></a>
## 线上幂等

| 调用 | 键 | 重放 |
| --- | --- | --- |
| `POST /v1/events` | 每个员工内的 `(source, id)` | 内容相同：`202`，返回原回执且 `duplicate: true`；内容不同：`409 conflict` |
| `POST /v1/tasks` | `(employee, idempotency_key)` | 请求相同：返回已存回执，`created: false`；请求不同：`409 conflict` |
| 控制接口 | `(employee, idempotency_key)` | 控制相同：已存回执；控制不同：`409 conflict` |
| `POST /v1/deliveries` | `Delivery.id` | 网关不得重发结果未知的投递；`DelivererClient` 从不重试 |
| `POST /v1/deliveries/status` | `Delivery.id` | 只读：Loop 询问 `unknown` 投递（`Hub.Reconcile`）；`dws.Deliverer` 根据自启动以来发出的投递作答，否则答 `unknown` |

Loop 以 `<window id>/<model turn>/<native call id>` 生成 Task Manager 的键，因此重放的唤醒绝不会创建第二个任务。

<a id="task-manager-event-sequence"></a>
## Task Manager 事件序列

```
Dispatch / Steer / Continue ──► execution.accepted   (host 通道，每次执行一条)
执行开始                    ──► execution.started    (host 通道)
Reporter.Progress           ──► execution.progress   (follow_up 会话，seq 1..n)
Steer                       ──► execution.terminal (superseded)、execution.steered、execution.accepted
执行完成或失败              ──► execution.result     (follow_up 会话；text = 报告或失败原因)
每次执行结束                ──► execution.terminal   (host 通道)
Stop                        ──► execution.terminal   (cancelled，reason stopped)
```

每个事件的 source 为 `task-manager`，带任务的 `employee`，行为者为 `system`，带 `task` 与 `run` 引用、任务的场域与语言，id 为 `<task>/<run>/<kind>`。memtask 通过一个有序的内存队列发布，带上限的指数退避；尚未发布的事件在进程退出时丢失（持久化的 Task Manager 把它们保存在 `task_events` 中）。

<a id="deployment-topologies"></a>
## 部署拓扑

| 拓扑 | 方式 | 说明 |
| --- | --- | --- |
| 单进程 | `go run ./cmd/employee-loop -stdin` | 进程内 memtask 加 echo 执行器、stdin 网关；用于本地开发 |
| Loop 加远程对端 | `employee-loop -serve :8081 -tasks URL -deliver URL` | Task Manager 回报到 `-serve` 地址 |
| 三个进程 | `employee-loop`、`task-manager`、`dws-gateway` | 见[命令](#commands) |
| 一个 Task Manager，多个 Loop | `task-manager -loops e1=URL1,e2=URL2` | 事件按 `employee` 路由（`contract.SinkResolver`） |
| 内核嵌入平台 | 平台调用 `kernel.Run`，或用自己的端口运行 `scene.Hub` | 见 [integration.zh-CN.md](integration.zh-CN.md) |

扩展：`scene.Worker` 无状态，通过租约领取，因此多个 Loop 副本可以共享通过 `employeeloop.Config.Ports` 传入的持久化端口（参考 DDL 见 [storage.zh-CN.md](storage.zh-CN.md)，本模块不带实现）。内存存储只能单进程使用。Task Manager 独立扩展；Loop 只需要它的 HTTP 地址。

<a id="commands"></a>
## 命令

| 命令 | 参数 |
| --- | --- |
| `cmd/employee-loop` | `-serve`、`-tasks`、`-deliver`、`-stdin`、`-persona`、`-speaker`、`-employee`、`-tenant`、`-locale`、`-token`、`-dotenv` |
| `cmd/task-manager` | `-serve`（默认 `:8082`）、`-loop` 和/或 `-loops employee=URL,...`（至少一个）、`-delay`、`-locale`、`-token`、`-dotenv` |
| `cmd/dws-gateway` | `-cid`、`-kind`、`-profile`、`-self-id`、`-self-name`、`-transport`、`-loop`、`-serve`（默认 `:8083`）、`-namespace`、`-tenant`、`-employee`、`-reactions`、`-locale`、`-token`、`-dotenv` |
| `cmd/catalog-doc` | `-locale`、`-o` |

环境变量：`LLM_PROVIDER`、`LLM_MODEL`、`LLM_ACK_MODEL`、`LLM_BASE_URL`、`LLM_API_KEY`（别名 `DEEPSEEK_*`、`DASHSCOPE_*`、`OPENAI_*`；见 `providers/llm`），`EMPLOYEE_LOCALE`、`EEL_TOKEN`，以及钉钉网关的 `DWS_CID`、`DWS_KIND`、`DWS_PROFILE`、`DWS_SELF_ID`、`DWS_SELF_NAME`、`DWS_TRANSPORT`、`DWS_REACTIONS`、`EMPLOYEE_ID`。`.env.example` 列出了全部变量。

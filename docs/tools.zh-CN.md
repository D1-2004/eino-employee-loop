# 工具

[English](tools.md)

模型只能通过工具行动，所有工具都由 Host 执行。本文说明工具契约（`contract/tool.go`）、内核如何校验调用并计算预算（`kernel`）、标准工具（`toolhost`），以及各服务实现哪些方法。生成的标准工具列表见 [catalog.zh-CN.md](catalog.zh-CN.md)；工具规格的线上格式见 [schema/tool-spec.schema.json](../schema/tool-spec.schema.json)。

- [工具契约](#tool-contract)
- [种类与终止决策](#kinds-and-terminal-decisions)
- [回执](#receipts)
- [拒绝](#refusals)
- [批次校验](#batch-validation)
- [预算](#budgets)
- [标准工具 Host](#the-standard-tool-host)
- [各服务的方法](#methods-per-service)

<a id="tool-contract"></a>
## 工具契约

`contract.ToolSpec` 由 Host 提供；内核从不硬编码任何厂商工具。

| 字段 | 含义 |
| --- | --- |
| `name` | 在一次唤醒内唯一。 |
| `kind` | `read`、`effect` 或 `terminal`。 |
| `description` / `description_key` | 面向模型的文本，或由 `i18n.Bundle.ToolDescriptions` 按唤醒语言解析的 i18n 键。Host 自带的文本优先。 |
| `parameters` | 参数的 JSON Schema 对象。 |
| `terminal` | 终止工具固定的处置；效果工具也可以声明它返回的处置。 |
| `exclusive` | 必须是批次中唯一的调用。 |
| `lanes`、`sessions` | 工具可以在哪里提供（`contract.ToolsFor`）；为空表示所有地方。 |
| `ports` | 工具背后的存储端口（`tasks`、`memory`、`conversation`、`outbox`、`journal`、`executor`、`config`）。 |
| `timeout` | 单次执行的上限；为零时读取和终止工具用 30 秒，效果工具用 120 秒。 |

一次调用是 `contract.ToolCall{native_id, name, arguments, turn}`。提供方的原生调用 id 是关联 id。提供方可能在之后的模型轮次复用 id，因此内核给每个调用标上从零开始的模型轮次：`(turn, native id)` 在一次唤醒内标识一个调用，再加上窗口就是 Host 的幂等键。以 `host:` 开头的提供方调用 id 会被拒绝（保留给 Host）。

Host 以 `contract.ToolResult` 回答：

- `content`：作为工具消息返回给模型。`toolhost` 使用机器字段的紧凑 JSON，从不写自然语言。
- `receipt`：证明效果已提交。
- `terminal`：结束唤醒的 `Decision{kind, reply}`。

<a id="kinds-and-terminal-decisions"></a>
## 种类与终止决策

| 种类 | 副作用 | 回执 | 结束唤醒 |
| --- | --- | --- | --- |
| `read` | 无 | 否 | 否 |
| `effect` | 持久 | 必须 | 返回终止决策时 |
| `terminal` | 自身没有（决策就是结果） | 否 | 是 |

处置（`contract.Disposition`）：

| 处置 | 含义 | 回复文本 |
| --- | --- | --- |
| `reply` | 已回答，无需再做什么 | 必须（非空） |
| `quiet` | 无话可说 | 从不投递 |
| `dispatched` | 后台工作已受理；结果之后以事件送回 | 可选的确认语；必须有回执 |
| `waiting` | 已提问；回答以 `human.response` 送回 | 问题本身 |

同一批次中的多个终止结果必须是同一种处置；不同的回复会被合并。批次校验会在任何调用执行之前拒绝规格声明了不同处置的调用；运行时结果不一致则以 `ErrTerminalConflict` 使唤醒失败。没有处置就结束的唤醒以 `ErrNoDisposition` 失败。`reply` 结果从不证明已送达；`toolhost` 在内容中写明（`"delivery_proven": false`），`dispatch_task` 写明结果尚未证实（`"result_proven": false`）。

在前台通道，模型也可以直接用纯文本回答，它会成为 `reply`。后台通道（以及设置了 `ForegroundRequiresTerminal` 的前台）要求终止调用；Host 可以通过 `contract.FinalTextHost` 接受第一个纯文本回答，并按回复工具的同一路径校验。

<a id="receipts"></a>
## 回执

- 没有回执的效果以 `ErrMissingReceipt` 失败；没有回执的 `dispatched` 决策也一样。
- 回执连同原生调用 id 和工具名收集在 `Outcome.Receipts` 中。`Outcome.ToolOutcomes` 按调用顺序保留每个结果，包括与错误一起提交的效果。
- `contract.Host.Execute` 必须按身份加原生调用 id 对效果去重，并且即使 context 在提交后立即取消，也必须返回已提交效果的回执。

`toolhost` 的回执：`dispatch_task`、`continue_task`、`steer_task` 与 `stop_task` 返回任务引用；`ask_human` 返回 `<window>/<turn>/<call>`；`memory_capture` 与 `memory_forget` 返回记录 id。

<a id="refusals"></a>
## 拒绝

拒绝是包装了 `contract.ErrToolRefused` 的错误，在任何效果发生之前返回。内核把 `{"error": ...}` 放进工具消息，让模型在剩余预算内修正调用。带回执的拒绝按失败的效果处理。

`toolhost` 的拒绝是带机器码的 `*toolhost.Refusal{Code}`（`toolhost.RefusalCode(err)`）：

| 码 | 原因 |
| --- | --- |
| `unknown_tool`、`tool_unavailable`、`tool_not_offered_in_session` | 工具未实现、端口缺失，或不在此通道/会话中提供 |
| `window_mismatch` | 身份指向了另一个窗口 |
| `unknown_source_ref` | `source_ref` 不是本窗口的事件 |
| `no_requester` | 来源事件没有已认证的人类行为者 |
| `empty_text` | 必填的文本参数为空 |
| `forbidden`、`task_state`、`task_not_found`、`invalid_request` | Task Manager 返回了 `ErrForbidden`、`ErrTaskState`、`ErrNotFound`、`ErrInvalidRequest` |
| `task_not_in_scene`、`not_requester` | 任务属于另一个场域、员工或请求人 |
| `bad_read_ref`、`read_ref_not_in_window`、`stale_read_ref` | 任务控制需要本窗口内 `read_task` 得到的 `read_ref`；`stale_read_ref` 即 Task Manager 返回的 `ErrStaleRevision`（任务已越过该版本） |
| `no_running_task`、`ambiguous_task`、`task_not_running` | 不带 `task_ref` 的 `steer_task` 没找到或找到多个运行中的任务，或 Task Manager 对纠正返回了 `ErrTaskState` |
| `bad_quote`、`memory_quota`、`bad_memory_kind`、`unknown_record` | 记忆的出处、配额、种类或记录问题 |
| `section_unavailable` | `read_context` 不提供该部分 |

其他错误不是拒绝。失败的效果，或任何带回执的错误，会使唤醒失败，由 Hub 重试窗口；失败的读取像拒绝一样报告给模型。`contract.ErrUnavailable`（Task Manager 已关闭或过载，HTTP 503）和传输失败都属于这类可重试的错误。重放时，记录的拒绝保持原文和类别（见 [architecture.zh-CN.md](architecture.zh-CN.md#journals-replaying-a-wake) 中的日志规则）。

<a id="batch-validation"></a>
## 批次校验

在原生批次中的任何调用执行之前，内核检查：

1. 每个调用都有非空、在批次内唯一、类型为 `function`、不以 `host:` 开头的原生 id，参数是 JSON 对象。
2. 每个工具都在本次唤醒中提供。
3. `exclusive` 工具在批次中独占。
4. 批次中各工具规格声明的 `terminal` 处置一致（`ErrTerminalConflict`），因此像 `[dispatch_task, reply]` 这样的批次不会提交任何东西。
5. 参数满足工具的 schema（可移植子集：`type`、`required`、`additionalProperties: false`、`properties`、`enum`、`minLength`、`maxLength`、`minItems`、`maxItems`、`items`；`kernel.ValidateArguments`）。
6. 字符串类型的 `reply` 参数通过 Host 的 `TextGuard`。
7. Host 的 `BatchValidator`（如有）接受整个批次。

被拒绝的批次不执行任何调用，也不进入对话记录；它消耗本次提供方请求并计为一次连续错误，模型会收到本地化的修复指令。

<a id="budgets"></a>
## 预算

`contract.Budget` 限制一次唤醒：

| 限制 | 默认 | 计数对象 |
| --- | --- | --- |
| `max_responses` | 3 | 被接受的模型响应（即使批次中的调用全部被拒绝，合法批次也计数） |
| `max_consecutive_errors` | 3 | 连续出现的提供方错误、被拒绝的响应，以及没有任何调用成功的批次 |
| `max_attempts` | 3 | 每一次提供方请求 |

有进展的响应会把连续错误数清零。预算耗尽时返回 `ErrBudget` 与具体限制的组合（`kernel.ErrResponseBudget`、`ErrConsecutiveErrors`、`ErrAttemptBudget`）。`KernelRunner.Budgets` 按通道设置预算；`Options.Permanent` 识别应立即结束唤醒的提供方错误。

<a id="the-standard-tool-host"></a>
## 标准工具 Host

`toolhost.New(Ports{Tasks, Memory, Conversation, Journal}, opts...)` 在契约端口之上实现了标准目录中的全部工具（13 个）；标准目录就是本包实现的工具，由测试保持一致。只有当工具所列的端口全部配置时才会提供该工具（`Host.Specs()`）；列出 `journal` 的工具（`dispatch_task`、`continue_task`、`steer_task`、`stop_task`）需要 `Ports.Journal`，它必须是唤醒记录所用的同一个日志（`KernelRunner.Journal`）。`dispatch_task` 用 `goal` 和 `prompt` 创建一个任务；本模块没有多步计划。

本模块没有实现的流程（计划、征集、场域配置）所用的工具属于嵌入平台。`toolhost` 从不提供它没有实现的工具，即使 `WithCatalog` 列出了它；平台用自己的 `contract.Host` 执行，并在自己的语言包中提供说明。[`examples/hostext`](../examples/hostext) 是示例配置（`continue_plan`、`describe_capabilities`、`scene_config_get`、`create_collection`、`set_scene_participation`），见 [events.zh-CN.md](events.zh-CN.md#host-extension-profile)。

`Host.ForWindow(win, tenant)` 把执行器绑定到一个已领取的窗口：

- `source_ref` 必须指向该窗口中的事件；请求人是该事件已认证的人类行为者，从不来自模型输出。
- 任务控制（`continue_task`、`stop_task`）需要本窗口日志所记录的某次 `read_task` 返回的 `read_ref`（`<task>@<revision>`）。日志是权威来源，因此被重新领取的窗口在任何进程中都保留它的读取。Host 把该版本作为 `TaskControl.ExpectedRevision` 发出，自己既不检查版本也不检查状态：Task Manager 先对已知幂等键返回已存回执，再拒绝已变化的任务（`stale_read_ref`）或不允许该控制的状态（`task_state`，纠正为 `task_not_running`）。因此已生效但响应丢失的控制在重新领取时回放为它的回执，不会被误判为过期、再以新键执行一次。
- 每次 Task Manager 变更都使用幂等键 `<window id>/<model turn>/<native call id>`。
- `memory_capture` 只保存说话人自己消息的逐字引用，配额满时先压缩一次。

<a id="methods-per-service"></a>
## 各服务的方法

| 服务 | 实现 | 调用方 |
| --- | --- | --- |
| Employee Loop | `contract.EventSink.Publish`（`scene.Hub`、`employeeloop.Loop`） | 网关、Task Manager、Host |
| Employee Loop 的 Host | `contract.Host.Execute`；可选 `FinalTextHost.AcceptFinalText`、`BatchValidator.ValidateBatch`、`TextGuard` | 内核 |
| Task Manager | `contract.TaskManager`：`Dispatch`、`Get`、`Find`、`Steer`、`Stop`、`Continue` | Loop Host 的工具 |
| Task Manager 执行器 | `taskmgr.Executor.Execute(job, reporter)`；`Reporter.Progress` | Task Manager |
| Event Gateway | `contract.Deliverer.Deliver` | Loop 的送达 Worker |

`toolhost` 中工具与 Task Manager 方法的对应：

| 工具 | 方法 |
| --- | --- |
| `dispatch_task` | `Dispatch` |
| `find_tasks` | `Find` |
| `read_task` | `Get` |
| `continue_task` | `Get`、`Continue` |
| `steer_task` | `Find`（不带 `task_ref` 时）、`Get`、`Steer` |
| `stop_task` | `Get`、`Stop` |

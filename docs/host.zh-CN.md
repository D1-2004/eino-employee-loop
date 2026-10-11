# Host 要实现什么

[English](host.md)

循环负责决定；有副作用的事情都由 Host 来做。本页按一次唤醒经过的顺序列出 Host 的职责。

## 1. 受理事件

用**经过认证的**数据把提供方输入转成 `event.Event`（行为者、场域、租户来自 Host，绝不来自消息正文），然后 `catalog.Route(e)`：

- 每个 `(employee, source, id)` 存一条回执——`Fingerprint()` 相同的重放是重复，不同则是冲突（`event_receipts`）；
- 按类型的合窗规则把事件放进所属会话的打开窗口（`session_windows`）；
- 在同一事务中执行 `Admission.Cancel`；
- host 通道事件：直接应用事实（记录回执、关闭任务），不唤醒模型。

`event/events.json` 里的合窗时间（被找的消息：静默 250 ms、最长 1 秒、16 项；被旁听的消息：12 秒、30 秒、20 项）是按聊天员工调出的默认 profile，不属于契约。Host 重新注册该类型即可覆盖：

```go
c := event.Standard()
spec, _ := c.Lookup("message.addressed")
spec.Coalesce = &event.Coalesce{QuietMs: 250, MaxWaitMs: 250, MaxItems: 16} // 赋新值：Lookup 返回的旧值是共享的
err := c.Register(spec)
```

## 2. 运行窗口

窗口就是一个会话的一次唤醒。领取窗口时保证：同一会话的窗口按顺序运行，场域的前台窗口优先，场域前台在运行时可抢占窗口等待。运行期间持有租约。

## 3. 装配唤醒

在**首次领取**时冻结本次唤醒的消息：系统提示（人设、规则）、Host 选择的上下文（记忆、近期对话、任务状态——标注为数据，绝不作为指令），以及窗口里的事件。重新领取时复用冻结的消息（`session_windows.wake`），不重建，因此重放发出的请求完全相同。

## 4. 提供工具

`loop.Tool` 描述一种能力：`Schema`（参数，在任何调用前校验）、`Effect`（必须返回回执）、`Terminal`（总是以哪种处置结束）、`Exclusive`（批次中只能有它）、`Timeout`。一个典型员工提供：

| 工具 | 种类 | 用途 |
| --- | --- | --- |
| `reply` | 终态 `reply`，效果 | 回答当前消息。 |
| `stay_quiet` | 终态 `quiet` | 不发言并结束。 |
| `ask_human` | 终态 `waiting`，效果 | 向人提问并等待 `human.response`。 |
| `dispatch_task` | 效果（返回 `dispatched`） | 启动后台工作；结果以 `execution.*` 事件回来。 |
| `find_tasks`、`read_task` | 读取 | 查找员工自己的工作。 |
| `steer_task`、`stop_task`、`continue_task` | 效果，独占 | 控制在途工作。 |
| `memory_capture`、`memory_lookup` | 效果 / 读取 | 带出处地记住事实。 |
| `read_context` | 读取 | 按需读取更早的对话或资源。 |

处置是封闭集合：`reply`、`quiet`、`dispatched`、`waiting`。决定 `dispatched` 的结果必须带回执（已受理的工作），即使工具不是 `Effect`；没有回执时唤醒以 `ErrMissingReceipt` 失败。

`TurnTools` 收窄每次模型调用提供的工具（例如第一轮之后去掉只在首轮可用的工具）；调用本轮未提供的工具会像未知工具一样被拒绝。

`PublicArgs` 列出会被人看到的字符串参数，`Config.ValidateText` 在本批任何调用执行之前校验它们，就像校验普通回答和 final text 一样。不设置时校验名为 `reply` 的参数，与 v0.2 相同。

## 5. 执行调用

`Host.Execute(ctx, identity, call)`：

- 用 **Host 自己的**身份检查权限与租户围栏；
- 按 `(window, turn, native call id)` 对效果去重，并记录结果（`window_tool_records`）；已提交的效果即使 `ctx` 紧接着被取消也要返回回执；
- 在任何效果之前拒绝时，返回包装了 `loop.ErrToolRefused` 的错误；模型看到错误文本，可以自行纠正；
- 调用结束唤醒时返回 `Terminal`（已发送的回复、带确认的已受理派发）。

`FinalTextHost.AcceptFinalText` 可选：在要求终态工具的唤醒中，把普通回答当作回复接受；默认只接受第一次响应，且须在任何工具执行之前。`Config.FinalTextAllowed` 改由调用序号和已有结果来决定（例如只做过读取之后的回答）；它放行的回答与第一次的回答一样校验和接受。

`Config.Progress` 决定哪些成功的调用算进展。有进展的批次把连续错误数清零；没有进展的批次让它加一，和失败的批次一样。不设置时每个成功的调用都算。

**调用所在的轮次。** `ToolCall` 不带轮次号，由 Host 推导：一个调用的轮次就是携带它的那次响应的 `ModelCall.Attempt`。在 `Config.OnModelCall` 中记下最新的 attempt（或在可持久模型里数 `Generate` 次数），存进本次唤醒的状态，在 `Execute` 里读取。循环只在返回该批次的 attempt 之后、下一次 attempt 之前执行这一批，所以最新的 attempt 总是正确的。每次 `Run` 的 attempt 从 1 开始计数，重放模型日志的重新领取会得到相同的编号。原生调用 ID 只在一次响应内唯一，所以键里要带轮次。

**每次唤醒的状态。** 一次响应依次经过：`OnModelCall`；循环自己的检查（调用身份与参数、本轮是否提供、终态与独占规则、schema、`PublicArgs` 文本）；`ValidateBatch`；然后要么是 `OnBatchRejected` 加一次修复，要么是 `OnBatchAccepted`，它在本批第一个 `Execute` 之前收到该批，只收一次。策略挂点（`TurnTools`、`ValidateBatch`、`FinalTextAllowed`、`Progress`）依赖模型做过什么的 Host，就在这里记录被接受的批次，绝不记录被拒的；被接受的 final text 不是批次。这些状态按 `Run` 保存：重放模型日志的重新领取从空状态开始，并对它接受的每个批次再次调用 `OnBatchAccepted`。

## 6. 让模型可持久

包装 Eino 模型，使每次请求/响应都被记录（`window_model_turns`），重新领取时对相同请求重放记录的响应，而不是再问一次模型。提供方参数拒绝与预留耗尽通过 `Config.Permanent` 报告。模型收到的是循环自己的对话记录消息，v0.2 与 v0.3 发出的请求逐字节相同（`loop/testdata/parity.golden.gz`），所以滚动发布期间一个版本开始的唤醒可以由另一个版本重新领取。循环下面的 Eino 版本由 Host 的依赖解析决定，所以每次升级 eino 或 eino-employee-loop，都要在 Host 模块里运行 `go test -run TestParityWithV021 github.com/D1-2004/eino-employee-loop/loop`。

Host 传给 `loop.New` 的模型必须：

- **每次 `Generate` 只发一个 provider 请求**（一次 attempt），内部绝不重试：关掉 SDK 自带的重试。循环把每次 attempt 计入 `MaxPhysicalAttempts`，并自己带修复指令重新请求；
- 从 `WithTools` 取工具。循环在每次 attempt 之前调用它，传入本次的工具（全部，或 `TurnTools` 提供的那些）。选项里从不携带工具。`ToolInfo.Extra[loop.ExtraParameters]` 原样保存每个工具的 JSON Schema。没有工具的 attempt 使用未绑定的模型；
- 当 attempt 传入 `model.WithToolChoice(schema.ToolChoiceForced)` 时强制工具调用（`RequireNativeToolCall`；OpenAI 的 `tool_choice: "required"`）。循环不传其他选项；
- 返回 provider 自己的消息：调用 ID 用 provider 的（非空、在本次响应内唯一），type 为 `function`，参数是 JSON 对象，`finish_reason` 为 `tool_calls`、`stop` 或空（其他值走修复）。`ResponseMeta.Usage` 会被累加。从不调用 `Stream`。

## 7. 送达

在完成窗口的同一事务中把用户可见的回复写入发件箱（`outbox`）。结果未知的发送（超时）保持 `unknown`，用状态查询核对——绝不重发。`Run` 失败时也要保留 `Outcome.Receipts` 和 `ToolOutcomes`：其中可能有已提交的效果。

## 失败分类

用 `errors.Is` 匹配 `Run` 返回的错误和各钩子收到的错误。错误文本沿用 v0.2，但带有细节，不属于 API。

| 哨兵 | 含义 | 后续 |
| --- | --- | --- |
| `ErrIncompleteResponse`、`ErrInvalidToolCall`、`ErrInvalidArguments`、`ErrUnknownTool`、`ErrExclusiveTool`、`ErrTerminalConflict`、`ErrTerminalRequired`、`ErrFinalTextRejected` | 响应在任何调用执行之前被拒绝 | 走修复；最后一次拒绝会并入预算错误 |
| `ErrModelBudget`，并带 `ErrConsecutiveModelErrors`、`ErrModelResponseBudget` 或 `ErrPhysicalModelBudget` 之一 | 预算用尽 | 唤醒结束 |
| `ErrToolRefused`（Host 的） | 在任何效果之前拒绝 | 模型可以纠正 |
| `ErrToolFailed`、`ErrMissingReceipt`、`ErrTerminalConflict`、`ErrNoTerminalDecision` | 效果失败或没有凭证，或结果互相冲突 | 本批结束后唤醒结束 |

Host 自己的钩子（`ValidateBatch`、`ValidateText`、provider 错误）产生的错误原样传递，用 Host 自己的哨兵分类。`ErrMissingReceipt` 总是包在 `ErrToolFailed` 里。

有三个名字来自循环的发源地 Multica，为了让 v0.2 Host 存下的数据继续匹配而保留：错误文本的前缀 `employee loop:`、`FinalTextOrigin`（`host_final_text`）和 `ExtraParameters`（`employee_loop.parameters`）。

## 一致性检查

`loop/hosttest` 让 Host 在自己的测试里，用真实的 Host 和模型适配器（外接假的传输）跑这份契约：

- `Target.Effect`：有回执，提交后 ctx 立刻被取消时也有（假传输调用 `hosttest.Committed`）；同一调用重放时返回同一回执，不产生第二次效果；
- `Target.Refused`：`ErrToolRefused`，不提交任何东西；
- `Target.FinalText`：只提交一次；
- `Target.Wake`：脚本化唤醒得到预期决定，每个成功的效果调用都有提交；重新领取时请求相同、不再提交；
- `Target.ReusedID`：两次响应复用同一个原生 ID 时提交两次效果（§5）。`Wake` 用脚本化模型运行，所以轮次要从 `OnModelCall` 取；
- `Model`：每次 `Generate` 一个请求，绑定、收窄和未绑定时的工具，强制调用，响应形状，provider 自己的 ID，以及假 provider 报告用量时的用量。

`Run` 返回之后的事（保留结果、送达）由 Host 自己的测试负责。`local` 包是参考实现，它的测试跑除 `ReusedID` 之外的全部检查，因为它唯一的效果会结束唤醒。

## 存储

[`schema/host.sql`](../schema/host.sql) 是上述表的参考 PostgreSQL DDL。没有外键（每张表有自己的保留期），索引用 `CONCURRENTLY` 创建。

## 示例：Multica

Multica 把本循环作为可选引擎，与内置内核并列。两者接收相同的冻结配置、可持久模型和 Host，所以切换引擎只换掉决策循环：

| 职责 | Multica |
| --- | --- |
| 受理、窗口、租约 | `employee_scene_job` 及其 worker |
| 唤醒装配 | 内置上下文构建器（`WakeEntries`） |
| 工具、权限、回执 | EmployeeLoop Host 及其工具日志 |
| 可持久模型 | 任务的模型日志 |
| 送达 | 钉钉回复发件箱 |
| 每轮工具策略、修复文案 | 由内置内核规则提供的 `TurnTools` / `RepairText` |

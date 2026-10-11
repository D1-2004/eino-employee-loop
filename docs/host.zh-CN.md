# Host 要实现什么

[English](host.md)

循环负责决定；有副作用的事情都由 Host 来做。本页按一次唤醒经过的顺序列出 Host 的职责。

## 1. 受理事件

用**经过认证的**数据把提供方输入转成 `event.Event`（行为者、场域、租户来自 Host，绝不来自消息正文），然后 `catalog.Route(e)`：

- 每个 `(employee, source, id)` 存一条回执——`Fingerprint()` 相同的重放是重复，不同则是冲突（`event_receipts`）；
- 按类型的合窗规则把事件放进所属会话的打开窗口（`session_windows`）；
- 在同一事务中执行 `Admission.Cancel`；
- host 通道事件：直接应用事实（记录回执、关闭任务），不唤醒模型。

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

`TurnTools` 收窄每次模型调用提供的工具（例如第一轮之后去掉只在首轮可用的工具）；调用本轮未提供的工具会像未知工具一样被拒绝。

## 5. 执行调用

`Host.Execute(ctx, identity, call)`：

- 用 **Host 自己的**身份检查权限与租户围栏；
- 按 `(window, turn, native call id)` 对效果去重，并记录结果（`window_tool_records`）；已提交的效果即使 `ctx` 紧接着被取消也要返回回执；
- 在任何效果之前拒绝时，返回包装了 `loop.ErrToolRefused` 的错误；模型看到错误文本，可以自行纠正；
- 调用结束唤醒时返回 `Terminal`（已发送的回复、带确认的已受理派发）。

`FinalTextHost.AcceptFinalText` 可选：在要求终态工具的唤醒中，把首个普通回答当作回复接受。

## 6. 让模型可持久

包装 Eino 模型，使每次请求/响应都被记录（`window_model_turns`），重新领取时对相同请求重放记录的响应，而不是再问一次模型。提供方参数拒绝与预留耗尽通过 `Config.Permanent` 报告。模型收到的是循环自己的对话记录消息，v0.2 与 v0.3 发出的请求逐字节相同（`loop/testdata/parity.golden.gz`），所以滚动发布期间一个版本开始的唤醒可以由另一个版本重新领取。循环下面的 Eino 版本由 Host 的依赖解析决定，所以每次升级 eino 或 eino-employee-loop，都要在 Host 模块里运行 `go test -run TestParityWithV021 github.com/D1-2004/eino-employee-loop/loop`。

## 7. 送达

在完成窗口的同一事务中把用户可见的回复写入发件箱（`outbox`）。结果未知的发送（超时）保持 `unknown`，用状态查询核对——绝不重发。

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

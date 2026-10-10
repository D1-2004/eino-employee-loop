<!-- 由 `go run ./cmd/catalog-doc -locale zh-CN -o docs/catalog.zh-CN.md` 根据 contract/catalog/*.json 与 i18n 语言包生成，请勿手工修改。 -->

# 事件与工具目录

本模块自带的目录。标准目录是本模块端到端实现的部分：它的网关、Task Manager 和 Loop 产生或处理的事件类型，以及 toolhost 包执行的工具。Host 可以注册更多事件类型（contract.Catalog.Register），也可以提供更多工具（contract.ToolSpec），见 [events.zh-CN.md](events.zh-CN.md) 与 [tools.zh-CN.md](tools.zh-CN.md)。

## 术语

| 维度 | 取值 | 含义 |
| --- | --- | --- |
| 类别 | `business` | 业务事件 |
| 类别 | `execution` | 执行事件 |
| 类别 | `completion` | 完成事件 |
| 类别 | `schedule` | 时间事件 |
| 类别 | `enablement` | 启用事件 |
| 模式 | `triggered` | 被动触发 |
| 模式 | `listening` | 主动监听 |
| 通道 | `foreground` | 前台 |
| 通道 | `background` | 后台 |
| 通道 | `host` | 仅 Host 处理 |
| 会话 | `interactive` | 前台交互会话 |
| 会话 | `observation` | 旁听会话 |
| 会话 | `follow_up` | 任务跟进会话 |
| 会话 | `routine` | 例行任务会话 |
| 会话 | `lifecycle` | 生命周期会话 |

## 事件类型

每个类型恰好属于一个类别和一个模式。通道与会话决定受理后的事件在哪里处理；选项控制合窗、抢占与撤销。 共 20 个事件类型。

### 业务事件 (`business`)

| 类型 | 模式 | 通道 | 会话 | 选项 | 说明 |
| --- | --- | --- | --- | --- | --- |
| `human.response` | 被动触发 | 前台 | `interactive` | 抢占旁听 | 有人回答了员工的问题或卡片。 |
| `member.changed` | 主动监听 | 后台 | `observation` | 可被抢占 | 有人加入或离开了场域。 |
| `message.addressed` | 被动触发 | 前台 | `interactive` | 抢占旁听; 合窗：静默 250 ms，最长等待 1000 ms，最多 16 项 | 有人找员工：@ 提及、私聊，或回复员工的消息。 |
| `message.follow_up` | 被动触发 | 前台 | `interactive` | 抢占旁听; 合窗：静默 250 ms，最长等待 1000 ms，最多 16 项 | 员工的回复送达后，同一个人紧接着继续说话，没有再次 @。 |
| `message.observed` | 主动监听 | 后台 | `observation` | 可被抢占; 合窗：静默 12000 ms，最长等待 30000 ms，最多 20 项 | 群里没有指向员工的消息。 |
| `reaction.added` | 被动触发 | 前台 | `interactive` | 抢占旁听 | 有人对员工的消息做了表情回应，例如表示纠正。 |
| `resource.changed` | 主动监听 | 后台 | `observation` | 可被抢占 | 场域里的资源发生变化（文档、日程、审批）。 |

### 执行事件 (`execution`)

| 类型 | 模式 | 通道 | 会话 | 选项 | 说明 |
| --- | --- | --- | --- | --- | --- |
| `execution.accepted` | 被动触发 | 仅 Host 处理 | - | - | 后台工作已受理并取得回执。 |
| `execution.progress` | 被动触发 | 后台 | `follow_up` | 按引用 `task` 各开一个会话; 合窗：静默 250 ms，最长等待 1000 ms，最多 16 项 | 后台工作上报了有意义的进展。 |
| `execution.started` | 被动触发 | 仅 Host 处理 | - | - | 后台工作开始执行。 |
| `execution.steered` | 被动触发 | 仅 Host 处理 | - | - | 在途的后台工作被纠正。 |

### 完成事件 (`completion`)

| 类型 | 模式 | 通道 | 会话 | 选项 | 说明 |
| --- | --- | --- | --- | --- | --- |
| `execution.result` | 被动触发 | 后台 | `follow_up` | 按引用 `task` 各开一个会话; 合窗：静默 250 ms，最长等待 1000 ms，最多 16 项 | 一次执行的结果已就绪，等待员工汇报。 |
| `execution.terminal` | 被动触发 | 仅 Host 处理 | - | - | 后台工作进入终态（仅记录事实）。 |

### 时间事件 (`schedule`)

| 类型 | 模式 | 通道 | 会话 | 选项 | 说明 |
| --- | --- | --- | --- | --- | --- |
| `observation.due` | 主动监听 | 后台 | `observation` | 可被抢占 | 旁听窗口到了复核时间。 |
| `routine.due` | 主动监听 | 后台 | `routine` | 按引用 `routine` 各开一个会话 | 例行任务到点。 |

### 启用事件 (`enablement`)

| 类型 | 模式 | 通道 | 会话 | 选项 | 说明 |
| --- | --- | --- | --- | --- | --- |
| `employee.disabled` | 被动触发 | 仅 Host 处理 | - | 撤销 `observation`, `lifecycle`; 可不带场域 | 员工被停用。 |
| `employee.enabled` | 被动触发 | 仅 Host 处理 | - | 可不带场域 | 员工被启用。 |
| `policy.changed` | 被动触发 | 仅 Host 处理 | - | 撤销 `observation` | 场域策略或运行配置版本发生变化。 |
| `scene.bound` | 被动触发 | 后台 | `lifecycle` | - | 员工被绑定到一个新场域。 |
| `subscription.changed` | 被动触发 | 仅 Host 处理 | - | 撤销 `observation`; 可不带场域 | 监听订阅被开启或关闭。 |

## 标准工具

toolhost 包在契约端口之上实现的工具。工具只在列出的通道与会话中提供；`name?` 表示可选参数。效果类工具必须返回持久回执。 共 13 个工具。

| 工具 | 种类 | 结束唤醒的处置 | 提供范围 | 端口 | 参数 | 说明 |
| --- | --- | --- | --- | --- | --- | --- |
| `reply` | 终止 | `reply` | `foreground`, `background` / 任意会话 | `outbox`, `conversation` | `source_ref`, `reply` | 用已知信息直接回复，然后结束，不创建工作。 |
| `stay_quiet` | 终止 | `quiet` | `foreground`, `background` / 任意会话 | - | `reason?` | 无需回应时，不发言直接结束。 |
| `ask_human` | 效果 | `waiting` | `foreground`, `background` / `interactive`, `follow_up` | `outbox`, `tasks` | `source_ref`, `question`, `options?` | 向对方问一个简短问题，然后等待回答。 |
| `dispatch_task` | 效果 | `dispatched` | `foreground` / `interactive` | `tasks`, `journal`, `executor` | `source_ref`, `goal`, `prompt`, `reply?` | 创建真实的后台工作。返回异步回执，结果稍后以完成事件送回。 |
| `find_tasks` | 读取 | - | `foreground` / 任意会话 | `tasks` | `source_ref`, `query?` | 查找本场域里 Host 记录过的工作。 |
| `read_task` | 读取 | - | `foreground`, `background` / 任意会话 | `tasks` | `source_ref`, `task_ref` | 读取某项工作在 Host 中记录的状态和报告。 |
| `continue_task` | 效果 | `dispatched` | `foreground` / `interactive` | `tasks`, `journal`, `executor` | `source_ref`, `read_ref`, `prompt`, `reply?` | 请求人明确要求延续时，继续一项已完成的工作。 |
| `steer_task` | 效果 | `dispatched` | `foreground` / `interactive` | `tasks`, `journal`, `executor` | `source_ref`, `correction`, `reply?`, `task_ref?` | 在请求人自己的工作运行中对其进行纠正。 |
| `stop_task` | 效果; 独占批次 | `reply` | `foreground` / `interactive` | `tasks`, `journal`, `executor` | `source_ref`, `read_ref`, `reply` | 请求人明确要求停止时，停止其自己的工作。 |
| `memory_capture` | 效果 | - | `foreground` / `interactive` | `memory` | `source_ref`, `audience`, `type`, `subject`, `quote` | 记录对方明确要求记住的内容，引用其原话。 |
| `memory_lookup` | 读取 | - | `foreground`, `background` / 任意会话 | `memory` | `source_ref`, `scope`, `query` | 快照里没有所需事实时查询记忆。 |
| `memory_forget` | 效果 | - | `foreground` / `interactive` | `memory` | `source_ref`, `record_ref` | 归档对方要求删除的一条记忆。 |
| `read_context` | 读取 | - | `foreground`, `background` / 任意会话 | `conversation` | `source_ref`, `section` | 读取本次唤醒已冻结的更早上下文。 |

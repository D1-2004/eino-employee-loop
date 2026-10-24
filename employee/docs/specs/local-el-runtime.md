# Local EL Runtime 合同

本合同描述当前七模块协作，不是完整线上协议还原声明。默认实现为本地 SQLite、
Markdown 和原生 Eino；真实 DWS 身份、消息送达和云端部署需要独立适配与验收。
历史验收与评审记录见 [evals](../../../evals/README.md)，不自动覆盖新版本。

## 模块与装配

| 模块 | 目录 | 合同 |
| --- | --- | --- |
| M1 Chat | `employee/chat` | 当前来源 → 授权 Context → native 工具；回答或提交业务命令 |
| M2 Task | `employee/task` | Worker、Review、Timing；`state` 是 Task/Goal/Run/Budget/Wait/Occurrence 的唯一业务聚合 |
| M3 Context | `employee/context` | 当前可授权来源、消息、任务和 Owner 视图；冻结版本并列出缺口 |
| M4 Owner | `employee/owner` | 独立 attention、memory、dreaming、tone、reflection、summary 加工 |
| M5 Execution | `employee/execution` | 幂等 Submit/Lookup/Poll/Cancel 与原子 Run 事实，不决定业务完成 |
| M6 Interaction | `employee/interaction` | 表达、Package、消息、Outbox、送达回执与业务事实回流 |
| M7 Entry | `employee/entry` | Profile 校验、依赖装配、模块注册和应用生命周期 |

`app/local` 是默认本地参考应用，调用 `employee/entry.Open` 并注入具体 Provider。`employee/contract` 只有共享值，`employee/ports` 定义能力接口，
`employee/kernel` 提供通用推理与恢复。具体 Provider 位于应用中，框架不导入 `app` 或 `evals`。
业务实现留在对应 M1–M6，M7 不成为新的业务中心。详见[架构](../../../ARCHITECTURE.md)。

## 生命周期

`entry.Open` 校验身份和 immutable profile，选择精确版本模块并打开依赖。
`Start` 只有依赖 ready 后才启动 workers。`Submit` 原子受理输入，`RunOnce`
推进可执行工作，`Resume` 验证原 Question/Wait 与当前回答的绑定。
`Close` 停止接单，等待/中断进行中调用，结算已预留 journal 并释放资源。

Channel 只适配入站事实，不拥有 Task/Goal。Entry 持有一个业务后端和进程领导锁，
同 scope 不因换 profile ID 得到第二个领导权。Task、inbox、Owner jobs 和执行配置
在受理时冻结。原生模型返回先 journal，原生工具再提交。模型特有的 opaque options
由不可变工厂配置负责版本化。

## 状态与副作用

trusted workspace/tenant/employee/scene/requester 是最小作用域；所有读取、命令和
worker 提交须带 scope。文本不是身份来源。Task ID、Run ID、Goal revision、
input receipt 和 native tool call ID 分开。相同 receipt 配不同请求必须 conflict。

Task 支持 ready/running/waiting/completed/failed/paused/stopped。Goal revision
独立；Run 只表示执行状态。Review 依据当前 Run 的退出和效果证据提议 complete、
continue、wait 或 fail；提交时再核验 Task/Goal/Run revision、取消、授权与预算。

State 事务原子提交业务状态、effect receipt 和 outbox 意图；模型/外部 I/O 不持有
事务。CAS 和租约须防陈旧写。Markdown 是配置、资料和产物能力，路径不能越界或
跟随符号链接；内容以 hash CAS 原子写入，不保存凭据。投影根据 State 真相重建，
不能反向猜测已经完成的业务。

unknown submit/delivery 必须向原 provider 用稳定键 lookup；timeout 或瞬时
NotFound 不能作为 exit/absence 证明，也不能换 provider 重做。Task stopped 后仍有
preparing/unknown/active Run，M5 仍须 lookup、取消并回流退出事实。

## 推理、上下文和恢复

`employee/kernel/eino` 复用原生 Runner、CheckPointStore、checkpoint ID、middleware 和
callbacks；`employee/kernel/journal` 记录物理模型调用与预算。`employee/kernel/decision` 保留独立的
单次 wake API。Eino continuation 不替代 inbox 幂等账本或 effect journal。

Context、Providers、Model 和 lease 属于运行绑定，不写入 continuation。恢复时重验
来源、撤回、受众与权限；已取消输入只能执行有界 journal 收尾，不能发起新业务动作。
Owner 投影必须能回溯授权原文，不能因为同一 owner 就向其他场景分享内容。

Profile 只选择可信 Go 工厂的精确版本，不下载或执行 JSON 中的代码。各 Loop 有独立
admission 和预算。旧任务保留冻结配置和版本，旧工厂须留到任务排空。没有 latest
回退、任意热卸载或自动迁移；核心模块目录绑定的变更需要显式迁移。

## 执行与完整业务后端

原子 Executor 只报告某个 Run 的物理执行事实，不再创建一套 Goal/Review。完整业务
Backend 则接管整套 Task 命令；二者是不同接口。选择一个 Backend，就只有一个业务
聚合，不同时运行本地和远端两套 TaskManager。

Work 不读取执行器私有命名空间。默认本地取消用稳定键 tombstone 阻止迟到 Submit；
独立 provider 若返回 NeverSubmitted，必须提供等价的持久 fencing 证明。Review
只提案，由 M2 在当前来源、证据与版本核验后提交。实际业务完成不能由 provider exit
或模型总结代替。

## 交付、维护和 CLI

M6 持有唯一 Outbox。PrepareIntent 冻结目的地；当前 AuthorizeDelivery 在 Send 前
再次检查。provider_accepted 是待查询状态，Lookup 证明 delivered 才是远端送达。
默认 local_applied/local-file 只代表本地文件回读。不支持的富资源和卡片明确拒绝。

Greeting/Routine 由明确来源受理，不在空闲时自动生成问候。周期任务在原 Task 上
记录 occurrence，经 Task Attention/Review 推进。Owner 文档是引用原文的派生视图，
不能替代 Task 状态或原始用户授权。

`app/local` 支持 replay/submit/answer/signal/schedule/drain/status/owner-status/retry/serve。
DWS native envelope 导入需要显式 owner/scene 映射，这不等于真实平台认证。Retry
保留原授权和已消耗物理预算；未确认投递不阻塞其他 scene 已就绪项。

操作见[本地指南](../local-entry.md)，替换见[模块装配](../module-composition.md)。
源实现与上游参考仍遵循固定 Eino v0.9.21 的 Runner、handler、turn loop 与 checkpoint
合同，不另造 JSON 工作流解释器。

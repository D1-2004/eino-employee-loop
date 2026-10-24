# Local EL Runtime 合同

状态：本地 standalone 已实现。工程、回放和真实模型验收分别见当前 Plan 与模型报告；
真实 DWS IM、可选外部完整业务后端和云端部署不属于本地文件回执的通过范围。

## Entry 与模块

`entry.Open` 校验身份和 immutable profile，迁移 SQLite 并打开 Markdown workspace；
`Start` 只有依赖 ready 后才启动全部相关 worker；`Submit` 原子受理事件；`RunOnce`
推进可执行工作；`Resume` 只接受已受理 Question/Wait 的当前回答；`Close` 停止接单、
等待/中断进行中调用、结算已预留 journal 并释放资源。Console 与 DWS 只做 Channel
适配，不拥有 Task/Goal 状态。流程编排由 Eino Graph/Runner 承担，业务动作由 Host。

| 模块 | 合同 |
| --- | --- |
| ChatLoop | 当前来源 → Context → native 工具；完成普通回答或提交业务命令 |
| TaskManager | Task/Goal/Run/Budget/Review/Watch/Wait/Occurrence 的唯一业务聚合 |
| Context Builder | 当前可授权来源、消息窗口、任务与 owner 视图，冻结版本并列缺口 |
| Owner Context Loop | 独立的记忆、关注、Tone、Dreaming、反馈与摘要加工 |
| Execution Bridge | 幂等 submit/lookup/events/exit，不将执行完成当作业务完成 |
| Interaction & Delivery | 冻结表达/问题/文件/outbox，确认回执，回流业务事实 |

## 状态与副作用

同一 trusted workspace/tenant/employee/scene/requester 是最小作用域，所有读取、
命令和 worker 提交须带 scope；文本不是身份来源。Task ID、Run ID、Goal revision、
input receipt、tool native call ID 分开。重复 receipt + 不同请求为 conflict。

Task 支持 ready/running/waiting/completed/failed/paused/stopped；Goal revision 独立，
Run 状态只代表执行。Review 必须依据当前 Run 的退出与效果证据提议 complete、
continue、wait 或 fail；事务提交时再核验 Task/Goal/Run revision、取消与预算。
未知 submit 或 delivery 不重试原 effect；先 lookup 原稳定幂等键，缺证明保持 unknown。

SQLite 事务提交业务状态、effect receipt 和 outbox 意图；外部 I/O 不持有事务。
存储 Provider 的 CAS/租约须防陈旧写。原生 checkpoint 是 Eino continuation，
不替代 effect journal。已取消输入只允许有期限的 journal 收尾，不执行新的业务动作。

Markdown 用作版本配置、owner 资料和结果文件；不接受越界/符号链接路径，不保存
凭据；写入原子替换。模型提出的修改须通过当前来源、owner、引用与版本校验后提交。
若 md 是 SQLite 投影，重建由 SQLite 真相驱动，不能反向猜测已完成。

## 原生 Eino 与扩展

既有 loop.Run 的兼容行为保留。新 Entry 原生路径使用 Runner、CheckPointStore、
明确 checkpoint ID、标准 middleware/callback 与事件。Context/Providers/Model/lease
属于运行绑定，不进入可序列化 continuation。Profile 只选择已注册精确版本的代码
工厂，不执行任意 JSON 中的命令/URL；各小 Loop 独立 admission 与预算。

参考当前固定 Eino v0.9.21 的 adk/runner.go、adk/handler.go、adk/turn_loop.go、
compose/checkpoint.go 和 components/tool/interrupt.go。沿用原 Host 的幂等/账本模式，
不因为框架有 checkpoint 就弱化来源、取消、权限、重放和 unknown 语义。

## 当前包边界（第一组实现）

- `agent`：新版本原生 Runner facade；Gate/Factory/Scoped Store/标准 observer，原生
  interruption、定向 resume、取消与 checkpoint。与历史兼容 `loop` 明确分开。
- `storage/sqlite`：事务性 scope KV/事件账本、CAS、workflow session adapter、原生
  Eino checkpoint adapter。OS session lease 进程死亡自动释放，不能按时间偷锁。
- `storage/markdown`：私有工作区、content hash CAS、原子替换、os.Root 路径边界。
- `work`：Task/Goal/Run/Wait 聚合基础。提交先原子记 preparing 意图；停止时保留
  unknown/资源 lookup 义务；终态 execution fact 与业务 Review 分开。当前不等同于
  完整 M2 Worker、Watch、计时调度、模型预算或 M5/M6 已接通。
- `internal/runcontext`：Graph 节点值向 Provider 传播，原取消/deadline/cause 保留；
  已预留 Journal 使用独立有期限的收尾 context，不执行新的模型或业务动作。

后续 Entry 的前台会话需要把 durable inbox 的受理事实与 Eino TurnLoop 的输入消费/
恢复分开：前者是业务真相，后者是编排 continuation。避免另造一个内存 foreground
队列，也不能把 checkpoint 当成重复消息的唯一幂等账本。

Task 停止后仍有 preparing/unknown/active Run 时，Execution Bridge 仍须处理其
lookup/取消/退出回流；不能因为 Task.State=stopped 就跳过资源收尾。待纠正后继受
预算阻挡时仍保存已证明的退出，并保留原纠正意图，不能回滚为“旧执行还活着”。

## 可选业务 Runtime 接入边界

Entry 在通用认知内核与业务状态所有者之间保留 BusinessRuntime port。standalone
由本地 SQLite 管理业务状态；已有完整业务后端模式则将 Task/Goal/Review 命令全部
交给选定后端。它们是可选实现，不是并列执行的两个 TaskManager。旧任务的后端
绑定持久冻结；unknown 先向原后端 lookup，不创建替代 Task。

完整业务任务 API 与原子 Executor API 必须分开：Execution Bridge 不得通过再建
一个有独立 Goal/Review 的完整业务任务来模拟原子 Run。具体内部 DTO/SDK 适配放在
内部消费端，公共模块只暴露通用端口。能力层的 Provider 替换也不代表业务数据库/
队列已替换；任何新增 backend 都要独立通过完整本地存储与恢复验收。

## 已接通的本地装配

`entry` 持有一个业务后端和进程领导锁，分别启动 Chat、Task/Review、Delivery、
Scheduler/Attention、Owner workers；同 scope 不因换 profile ID 获得第二个领导权。
Task、inbox、Owner jobs 与 execution 配置在受理时冻结。原生模型返回先 journal，
原生工具才可提交。底层模型特有的 opaque options 由不可变工厂配置负责版本化。

`cmd/el` 支持 replay/submit/answer/signal/schedule/drain/status/owner-status/retry/serve；
DWS native envelope 的导入需要操作者显式 owner/scene 映射，不能当成真实身份认证。
Greeting/Routine 只由明确来源受理，周期任务经原 Task 的 occurrence 与 Review，
不在空闲时无依据生成问候。表达和 Package 使用真实读回的 artifacts；实际本地文件
receipt 回流原 Task，并可唤醒其准确 delivery Wait。

恢复 hold 和失败项提供明确 handle；Retry 不重置物理预算。unknown 原动作只能向
原 port lookup，不能换 provider 再做一遍。未确认投递不阻塞其他 scene 的已就绪项。

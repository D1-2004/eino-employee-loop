# 本地完整 EL Runtime 交付 Plan

## 目标与边界

用户要求：特点/特性全部对齐，所有小 Loop 对齐；本地 DWS 能模拟运行；存储使用
SQLite、Markdown；代码结构清楚，有明确 Entry；实现附图 EL 业务模块；本地可用。
本目标不等同于此前的行为兼容适配或只读 review，不以 chat-only/工具桩完成。

附图六个模块：M1 ChatLoop；M2 TaskManager（Goal/Version/Run/Budget、Review、
Watch/Wait/Attention、定时发生次）；M3 Context Builder；M4 Owner Context Loop
（关注/Memory/Dreaming）；M5 Execution Bridge（提交/查询/事件、unknown 与退出证明）；
M6 Interaction & Delivery（表达、投递、动作、回执、问题/答案回流）。另有配置/版本/
装配、迁移、ready/start、全部 worker 生命周期、恢复与隔离边界。

图中的云端 DE/MA 用本地 Executor Provider 实现同样的提交/lookup/events/退出合同；
保留远端 Provider seam。SQLite 是工作与执行真相；Markdown 是可读配置/知识/产物，
不把两份数据都当权威，也不把模型自述当完成。DWS 接入区分真实 CLI 模拟模式、
事件回放和真人会话；不会将回放冒称真实 IM 投递。所有真实外部写操作继续按用户
给定对象与授权执行。

仓库：D1-2004/eino-employee-loop；基线 f48e3c1，工作分支
feat/local-el-runtime-20261024。不部署云端，不把 dt-fde-multica 的 6000+ 历史
提交混入其 GitHub 主干。公开库保持既有 loop API 行为；新 Entry 明确启用完整本地
Runtime，并逐步替代旧 chat-only harness 的默认使用说明。

## 实现顺序

1. 合同/架构：逐项列模块、公开入口、状态/错误、授权与恢复证据；独立对齐各小 Loop。
2. Eino 基础：修复取消结算与节点 context；新增原生 Runner、标准 handlers/events、
   SQLite CheckPointStore、Interrupt/Resume，纯可序列化 continuation。
3. 本地存储：SQLite 事务、迁移、session、journal、任务/Run/Goal、问答、outbox、
   watch/occurrence、owner state；Markdown 配置与知识投影，安全路径与原子替换。
4. 核心业务：Context→Chat→Task→Executor→Review→交付；未知提交只 lookup，不盲重提；
   原 Goal 上继续/纠正/停止/等待与回答；进程重建后从持久状态恢复。
5. 所有小 Loop：Review/Goal、Attention/Watch/Wait、反馈复盘、摘要、Owner Context/
   Dreaming/Tone、Ask/表达、Package、例行与 Greeting；独立作用域、预算、journal。
6. Entry/CLI：Open/Start/Submit/RunOnce/Resume/Close 的明确生命周期；本地 console、
   DWS 模拟/回放及真实 DWS 适配；无需云环境的完整示例。
7. 验收与交付：真实 CLI 运行、SQLite/Markdown 独立回读、进程重建、跨作用域隔离、
   unknown/取消/并发幂等/回流；自有模型与 grounded judge；源码/命令/产物/运行文档。

## 验收矩阵（全部 required，不以局部通过签完成）

| 要求 | 证明方式 | 当前 |
| --- | --- | --- |
| 明确 Entry、ready/start/close 与配置版本 | 实际 CLI 启动、关闭、非法配置拒绝 | pending |
| Eino Runner/原生 checkpoint/定向 resume | 进程重建后中断点恢复；已完成工具不重复 | pending |
| 标准 callbacks/middleware/组合 | span context 传入 Provider，标准插件实际运行 | pending |
| SQLite/Markdown 持久化 | 独立数据库/文件回读；重启继续；原子事务/路径边界 | pending |
| M1 Chat：查/派发/纠正/继续/停止 | 实际输入到相应业务效果，作用于同一 Task | pending |
| M2 Review/Goal/Wait/Watch/Attention/定时 | Run 完成不等于 Goal 完成；回答/事件回原 Review | pending |
| M3 Context：选材/冻结/缺口/三视图 | 当前授权 source 与版本，撤回/跨场域隔离 | pending |
| M4 Owner Context/Memory/Dreaming/Tone/反馈 | 独立加工、实际写入与回读，不人为强依赖 | pending |
| M5 Execution Bridge | submit/lookup/events/exit、unknown 恢复不重复提交 | pending |
| M6 表达/Package/例行/问题/文件/投递 | 原材料完整、真实回执、答案和交付事实回原 Review | pending |
| 本地 DWS 模拟可运行 | 实际 dws CLI 或明确回放入口跑完整业务并回读 | pending |
| 自有模型本地全链路 | 使用本机模型，核对真实效果并做定性 judge | pending |
| 文档/示例/发布 | 一条可复制命令，依赖/存储/边界清晰，固定版本 | pending |

## 当前状态

实现：进行中。上一轮是 progress（review/探针/主干提交）；本目标当前从现场代码
确认了 local.Employee 只有内存历史和 reply/stay_quiet，background 不执行。
这不满足完整本地 EL。已读取附图和现有代码，接下来先完成 Eino 基础与持久化，
再实现业务状态聚合和明确 Entry。验收 Automatic（本地）；未通过项目仍保持 pending。
本机模型凭据仅进进程环境；不入库/不进 md/不提交。外部真实 DWS 对象与授权缺失时
不发真人消息，先完成可独立验证的本地运行与 CLI 合同，不降低剩余验收口径。

### 第一组：原生 Eino、SQLite/Markdown、Task 聚合基础（progress）

已实现并实际检查：

- 修复 review R1：已预留 attempt 的 Journal 在取消后使用最长 3 秒独立 context
  收尾；仍返回原取消，不接受决策、不重试。原探针 persisted_attempts 从 0 变为 1。
- 修复 review R2：Provider 收到真实 Eino 节点 span/context 值，原取消、deadline 与
  cause 保留。原探针 prepare/model 的节点标记均从 false 变为 true。
- 新 `agent.Runtime` 使用 Eino Runner、scoped CheckPointStore、标准 callbacks/
  handlers、AgentTool 事件、StatefulInterrupt、明确 target 的 ResumeWithParams。
  旧 `loop` 默认行为不变。
- 原生工具中断：SQLite 关闭重开及整个子进程退出后，在另一个进程恢复指定工具；
  已完成的前序工具刻意没有 fake 幂等，数据库仍只记 1 次执行。原生取消保存真实
  checkpoint，跨 scene 的 resume 被隔离。标准 middleware 和 callback 的 context
  实际传入模型，AgentTool 子事件实际出现在事件流。
- SQLite：WAL、foreign keys、busy timeout、immediate 事务；scope/version checkpoint；
  文档 CAS 与幂等事件、workflow lease adapter。真实并发写只有一次 CAS 成功，
  transaction rollback 无部分提交；进程 SIGKILL 后 session lease 可重新获得。
- Markdown：真实文件写入/回读/重开，content-hash CAS；绝对路径、../、外部 symlink
  均拒绝，没有写出工作区。
- `work.Manager` 基础：dispatch/continue/steer/pause/stop、preparing/unknown/receipt、
  退出事实、Review、Goal revision、Wait/Answer 和 run cap。来源原话保留。测试证明
  unknown 不创建新 Run；纠正等旧执行实际退出；真正缺输入不能被 continue 绕过；
  回答回原 Review；Run succeeded 不自动完成 Goal；preparing 中 stop 不捏造退出；
  后继预算不足时仍保存退出证明与纠正意图。全部在实际 SQLite 事务中检查。

检查：`go test -race ./... -count=1`、`go vet ./...` 通过。该组模型均为明确的
脚本/可控 fake，用于确定性状态与恢复反例，不宣称自有模型或真实 DWS 业务验收。
完整 race 日志 `/private/tmp/eino-local-runtime-foundation-20261024.log`。

DWS 现场：本机 CLI v1.0.63-beta.1；确认 event +listen-im 与 chat +messages-reply
的真实 help/flags。实际执行 `dws --mock event +listen-im --kind group --chat-id
 eel-local-simulation --dry-run --duration 1s --timeout 5 --format json`，返回订阅计划并
明确 no bus connection。只证明真实 CLI 计划能力，不证明事件接收或消息投递；未
建立订阅、未向真人发消息。真实 CLI mock/bus 路径与本地回放/完整业务仍需实现验收。

未完成：明确 Entry/CLI、Context 三视图、各业务 Worker、Execution Bridge、Interaction/
Delivery、owner 小 Loop、DWS 本地完整模拟及自有模型全链路。验收表继续 pending。
下一组：先构建 Context + 明确 Entry，接通 Chat → Task → 本地 Executor → Review →
SQLite outbox/Markdown 交付；再补齐所有独立小 Loop，不能在这个切片停作完成。

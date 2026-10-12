# Eino Employee Loop

[English](README.md)

基于 [CloudWeGo Eino](https://github.com/cloudwego/eino) 的、由事件 + 场域驱动的数字员工决策循环。每次唤醒只回答一个问题——*这个场域刚发生了这些事，员工该调用哪些工具，做完了没有？*——其余一切交给嵌入它的 Host。

```
            Host（你的系统）                               本模块
 ┌──────────────────────────────────────────┐      ┌────────────────────────────┐
 │ 渠道 ─► event.Event ─► catalog.Route ─────┼─────►│ event：类别·性质·通道      │
 │ 队列 · 窗口 · 上下文装配                  │      │        会话 · 取消         │
 │ 工具 · 权限 · 回执                        │◄────►│ loop： 模型 ↔ 工具、       │
 │ 存储 · 重放 · 送达                        │      │        预算、终态决定      │
 └──────────────────────────────────────────┘      └────────────────────────────┘
```

## 包

| 包 | 作用 |
| --- | --- |
| `event` | 给输入分类（类别 × 性质），路由到所在场域的通道与会话，处理抢占与撤销。标准目录在 `event/events.json`。 |
| `loop` | 在 Eino ADK `ChatModelAgent`（底层是 `ToolCallingChatModel`）上跑一次唤醒：原生工具批次、任何效果之前先校验、效果必须有回执、终态决定、预算、修复轮。 |
| `loop/looptest` | 给 Host 测试用的脚本化 Eino 模型。 |
| `loop/hosttest` | Host 在自己的测试里对其 Host 和模型适配器跑的一致性检查。 |
| `local`、`cmd/eel` | 在本机跑一个员工，接入真实钉钉（经 dws CLI 的账号，或 Stream 模式机器人），并上报 Langfuse。 |
| `schema` | `event.Event` 的 JSON Schema，以及持久化 Host 的参考 PostgreSQL 存储。 |

## 边界

Host 调用本循环的方式与调用任何决策引擎相同：

```go
out, err := loop.New(loop.Config{
	Tools:               tools,          // Host 的能力
	Budget:              loop.DefaultBudget(),
	RequireTerminalTool: true,           // 普通文字是私有的，用工具结束
}, model, host).Run(ctx, loop.Wake{Identity: id, Messages: messages})
```

- **输入：** `Config`（工具、预算、协议开关与挂点）、一个 Eino 模型、一个 `Host`（`Execute(ctx, Identity, ToolCall) (ToolResult, error)`），以及本次唤醒冻结的消息。
- **输出：** `Outcome`——决定（`reply`、`quiet`、`dispatched`、`waiting`）、回执、按调用顺序的每个工具结果、预算用量、token 用量和被接受的对话记录。

类型与常见的员工内核一一对应（`Tool`、`ToolCall`、`ToolResult`、`Decision`、`Receipt`、`ToolOutcome`、`Outcome`、`Budget`），已有 Host 换引擎只需映射字段，不需要换概念。Host 的策略通过挂点留在模块之外：`ValidateBatch`、`ValidateText`、`ValidateArguments`（用 Host 自己的参数规则代替内置的 schema 检查）、`ValidateDecision`（Host 接受哪些终态决定）、`TurnTools`（每次模型调用提供哪些工具）、`FinalTextAllowed`（哪些普通回答可以结束唤醒）、`Progress`（哪些成功的调用清零连续错误）、`RepairText`、`Permanent`，以及模型调用、工具调用、批次被接受与被拒、失败的观察者。

循环强制的规则：

- 整批校验通过才执行任何调用（未知或本轮未提供的工具、独占工具、静态终态处置冲突、参数 schema 或 Host 的 `ValidateArguments`、Host 规则）——被拒的批次走修复轮，绝不执行一半；
- 没有 Host 回执的效果永远不算完成；
- Host 拒绝（`ErrToolRefused`）作为工具结果交回模型；
- 无效输出永远不进入对话记录；
- 预算限制有效响应数、连续错误数和模型请求次数。

从 v0.3 起，循环跑在 Eino ADK 的 `adk.ChatModelAgent` 上，不再是手写循环。发给模型的请求与 v0.2 逐字节一致，行为也相同，只有 [`loop` 包文档](loop/loop.go)列出的少数差异（例如 Host、模型或任何 `Config` 回调里的 panic 会变成 `Run` 返回的错误）。`loop/testdata/parity.golden.gz`（从 v0.2.1 录制的 3000 个种子场景）把两者钉住，记录过的唤醒可由任一版本续跑。

## 事件与场域

每个输入都是一个 `event.Event`。它的类型决定**发生了什么**（类别）和**员工为什么收到它**（性质）：

| | triggered（被动触发） | listening（主动监听） |
| --- | --- | --- |
| business（业务/场域） | `message.addressed`、`message.follow_up`、`human.response`、`reaction.added` | `message.observed`、`member.changed`、`resource.changed` |
| execution（执行） | `execution.accepted`、`execution.started`、`execution.progress`、`execution.steered` | |
| completion（完成） | `execution.result`、`execution.terminal` | |
| schedule（定时） | | `routine.due`、`observation.due` |
| enablement（启用） | `employee.enabled`、`employee.disabled`、`subscription.changed`、`policy.changed`、`scene.bound` | |

路由让每个场域有**一个前台交互会话**（来自人的被动触发业务事件，串行处理，人的输入优先），以及若干**排队的后台会话**——`observation`（主动监听，可被抢占）、`follow_up`（每个任务一个）、`routine`（每个例行任务一个）、`lifecycle`——外加一条不唤醒模型、由 Host 直接应用事实的 **host 通道**。有人找员工会抢占旁听窗口；策略或订阅变化会撤销它们。详见 [docs/events.zh-CN.md](docs/events.zh-CN.md)。

## 本地运行

```bash
export LLM_BASE_URL=... LLM_API_KEY=... LLM_MODEL=...      # 任意 OpenAI 兼容端点
export LANGFUSE_BASE_URL=... LANGFUSE_PUBLIC_KEY=... LANGFUSE_SECRET_KEY=...   # 可选
go run ./cmd/eel -channel dws -profile <corpId:userId> -locale zh-CN       # 一个钉钉账号
go run ./cmd/eel -channel robot -robot-client-id <id> -robot-client-secret <secret>   # Stream 机器人
go run ./cmd/eel -channel console -v -model-script local/testdata/console.json   # 本终端，不接钉钉；带 -model-script 不需要 LLM
```

各渠道、本地调试、Langfuse trace，以及用评测集对本地员工跑评测，见 [docs/local.zh-CN.md](docs/local.zh-CN.md)。

## 文档

- [docs/events.zh-CN.md](docs/events.zh-CN.md)——分类、路由不变量、会话、抢占、扩展类型。
- [docs/host.zh-CN.md](docs/host.zh-CN.md)——Host 要实现什么：工具、回执、拒绝、唤醒装配、存储、重放、送达。
- [docs/local.zh-CN.md](docs/local.zh-CN.md)——本地验证。
- [docs/integration.zh-CN.md](docs/integration.zh-CN.md)——把本循环接入你的平台：最小 Host、状态、一致性检查、驱动、切换引擎、评测。

文案支持多语言：循环的修复指令（`loop/locales`）、本地员工的提示词（`local/locales`）和事件说明（`event/events.json`）都提供 `en` 与 `zh-CN`，并有测试保证键集合一致。新增语言只需新增文件。

## 许可

MIT

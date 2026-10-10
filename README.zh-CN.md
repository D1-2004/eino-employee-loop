# Eino Employee Loop

[English](README.md)

基于 [CloudWeGo Eino](https://github.com/cloudwego/eino) 的 AI 员工事件循环。员工在群聊、私聊、企业空间等场域中工作：每个输入都作为事件受理，路由到所属场域的某个会话，在一次模型唤醒中做出决策，并且只通过 Host 执行的工具行动。后台工作交给独立的 Task Manager，由它以事件回报。

> **仓库：** [D1-2004/eino-employee-loop](https://github.com/D1-2004/eino-employee-loop)

- [事件与场域模型](#event-and-scene-model)
- [三个服务](#three-services)
- [包地图](#package-map)
- [快速开始](#quick-start)
- [嵌入内核](#embedding-the-kernel)
- [测试](#testing)
- [国际化](#internationalization)
- [文档](#documentation)
- [许可证](#license)

<a id="event-and-scene-model"></a>
## 事件与场域模型

```
Event {class, mode, scene, actor, refs}
  │  class: business | execution | completion | schedule | enablement
  │  mode:  triggered (被动触发) | listening (主动监听)
  ▼
Catalog.Route ──► 场域 (员工 x 租户 x 场所)
                   ├─ foreground  interactive          每个场域一个；有人找员工；最先处理
                   ├─ background  observation          每个场域一个；无人指向员工的消息；可被抢占
                   │              follow_up/<task>     在途工作的进展与结果
                   │              routine/<routine>    例行任务到点
                   │              lifecycle            绑定到场域、配置变化
                   └─ host 通道   回执与事实，只记录不唤醒

会话 ──► 窗口 (合并的事件) ──领取、租约──► 唤醒 (kernel) ──围栏完成──► 发件箱 ──► 网关
```

- **类别**说明发生了什么：`business`、`execution`、`completion`、`schedule`、`enablement`。**模式**说明员工为什么收到它：`triggered`（被动触发：指向员工，或关于员工自己的工作）或 `listening`（主动监听：员工订阅、旁听或计时）。
- 每个场域恰好有一个**前台交互会话**，以及任意多个**排队的后台会话**。只有来自人的被动触发业务事件进入前台；有人找员工时，会抢占员工对该场域的旁听。
- **窗口**是对一批合并事件的一次唤醒。领取带租约，之后的每次写入都受围栏保护，模型轮次和工具效果都记入日志，回复随窗口一起提交到发件箱，结果未知的投递绝不重发，只通过状态查询核对。

详见：[架构](docs/architecture.zh-CN.md)、[事件](docs/events.zh-CN.md)、生成的[目录](docs/catalog.zh-CN.md)。

<a id="three-services"></a>
## 三个服务

```
Event Gateway ──Event──► Employee Loop ──DispatchRequest──► Task Manager
      ▲                       │  ▲                               │
      └──────Delivery─────────┘  └──────────Event (execution.*)──┘
```

| 服务 | 拥有 | 边界 |
| --- | --- | --- |
| **Event Gateway** | 提供方连接、场域解析（生成场域 id，绝不使用原始会话 id）、发送 | 发布 `contract.Event`；提供 `contract.Deliverer` |
| **Employee Loop** | 受理、会话、唤醒、日志、发件箱、记忆、会话日志 | 提供 `contract.EventSink`；调用 `contract.TaskManager` 与 `Deliverer` |
| **Task Manager** | 任务、执行、执行器 | 提供 `contract.TaskManager`；**只**通过发布 `execution.*` 事件回报 |

这些服务可以在一个进程中运行，也可以分开运行，无需改代码：每个跨边界的值都是 `contract` 中的普通 JSON 类型，由 `transport/httpapi` 通过 HTTP 传输，JSON Schema 见 [schema/](schema/)。`internal/archtest` 以按服务的白名单强制引用边界（传输层只引用 `contract`；Loop 包只引用 Loop、`contract` 和 `i18n`）。每个事件都写明 `employee`，因此一个 Task Manager 可以服务多个 Loop（`contract.SinkResolver`）。线上协议与部署拓扑见[服务](docs/services.zh-CN.md)。

<a id="package-map"></a>
## 包地图

| 包 | 职责 |
| --- | --- |
| `contract` | 零依赖契约：事件、标准目录、工具、Loop 的存储端口、服务边界 |
| `i18n` | 语言包 bundle；内嵌 Loop 的语言包，其他包内嵌并合并自己的语言包 |
| `kernel` | 基于 Eino 组件的一次唤醒；`WakeJournal` 重放；`kerneltest` 编排模型与日志 |
| `scene` | `Hub`（受理、会话、围栏完成、发件箱）与 `Worker` |
| `toolhost` | 基于契约端口的标准工具 |
| `store/memstore` | 内存事件日志、会话队列、日志、发件箱、记忆、会话日志（唯一随附的端口实现） |
| `employeeloop` | Employee Loop 装配（基于契约端口的 scene + toolhost；未提供的端口用 memstore） |
| `taskmgr` | Task Manager 一侧：执行器端口、事件序列；`memtask`（内存实现）、`executors` |
| `gateway/dws`、`gateway/stdin` | 通过 `dws` CLI 接入钉钉；本地 stdin 场域 |
| `providers/llm` | 从环境变量构造 Eino 聊天模型（DeepSeek、DashScope、OpenAI 兼容） |
| `transport/httpapi` | 三个边界的 HTTP 处理器与客户端 |
| `schema` | JSON Schema 与按服务划分的参考 DDL（`loop.*.sql`、`taskmgr.*.sql`；PostgreSQL、SQLite） |
| `cmd/employee-loop`、`cmd/task-manager`、`cmd/dws-gateway` | 三个服务 |
| `cmd/catalog-doc` | 根据目录和语言包生成 [docs/catalog.zh-CN.md](docs/catalog.zh-CN.md) |
| `examples/hostext` | 平台扩展配置示例（事件类型、工具及其自己的 i18n 语言包）；Loop 不使用它 |
| `e2e` | 基于 `httptest` 的离线跨服务测试 |
| `prototype` | 早期的单进程 TurnLoop 运行时，保留作参考 |

<a id="quick-start"></a>
## 快速开始

**作为库使用**（在你自己的 Host 中嵌入内核、场域 Hub 或合同；见 [docs/integration.zh-CN.md](docs/integration.zh-CN.md)）：

```bash
go get github.com/D1-2004/eino-employee-loop@v0.1.0
```

需要 Go 1.26。聊天模型从环境变量配置（`LLM_API_KEY`，可选 `LLM_PROVIDER` = `deepseek` | `dashscope` | `openai`、`LLM_MODEL`、`LLM_BASE_URL`）；见 `.env.example`。

**单进程**（stdin 场域，进程内 Task Manager 加 echo 执行器）：

```bash
export LLM_API_KEY=...
go run ./cmd/employee-loop -stdin
go run ./cmd/employee-loop -stdin -locale zh-CN   # 中文提示词与输出
```

**三个进程**（设置 `EEL_TOKEN` 可在每个边界上要求共享 bearer 令牌）：

```bash
go run ./cmd/employee-loop -serve :8081 -tasks http://localhost:8082 -deliver http://localhost:8083
go run ./cmd/task-manager  -serve :8082 -loop http://localhost:8081
go run ./cmd/dws-gateway   -serve :8083 -loop http://localhost:8081 -cid '<conversation>' -self-id '<employee id>'
```

钉钉网关需要 `dws` CLI 和钉钉 profile；任何其他网关只需要 `POST /v1/events` 并提供 `POST /v1/deliveries`。

<a id="embedding-the-kernel"></a>
## 嵌入内核

平台保留自己的工具、权限、存储和上下文，让本模块运行决策循环。最小的嵌入是一次唤醒：

```go
res, err := kernel.Run(ctx, kernel.Options{
    Model: chatModel,         // 任意 Eino ToolCallingChatModel
    Host:  platformHost,      // contract.Host：执行工具、检查权限、返回回执
    Tools: platformToolSpecs, // []contract.ToolSpec
    RequireTerminal: true,
}, kernel.Wake{Identity: identity, Instructions: persona, Blocks: frozenContext})
// res.Decision：reply | quiet | dispatched | waiting；res.Receipts 证明效果
```

也可以用平台存储运行 `scene.Hub`，并让 `scene.KernelRunner` 的 `Hosts`、`Tools` 和 `Context` 来自平台。可运行示例：`ExampleRun`（`kernel/example_test.go`）和 `ExampleKernelRunner`（`scene/example_test.go`）。[Multica](https://github.com/multica-ai/multica) 以这种方式嵌入内核，并提供按 Agent 的引擎开关；其作业种类和组件的映射见[集成](docs/integration.zh-CN.md)。

<a id="testing"></a>
## 测试

```bash
go test ./...                     # 离线：编排模型、假实现、httptest
go vet ./...
go run ./cmd/catalog-doc -locale zh-CN -o docs/catalog.zh-CN.md   # 修改目录或语言包之后
EEL_TEST_POSTGRES_URL=postgres://... go test ./schema/             # 同时执行 schema/*.postgres.sql
```

没有测试会调用模型提供方或访问网络。`kernel/kerneltest` 编排模型响应；`e2e` 通过 `httptest` 分开运行三个服务；`schema` 用 JSON Schema 校验真实的契约值，并执行 SQLite DDL；提交的目录文档过期时 `internal/catalogdoc` 会失败。

<a id="internationalization"></a>
## 国际化

所有用户可见和面向模型的文字都在 i18n 语言包中：Loop 的在 `i18n/locales`，其他包的放在各自代码旁（`<dir>/locales`），用 `Bundle.LoadFS` 合并。每个包随附的 `en` 和 `zh-CN` 键集合完全相同。Host 可以在运行时用 `Bundle.Merge` / `Bundle.Load` 覆盖或新增语言。见[国际化](docs/i18n.zh-CN.md)。

<a id="documentation"></a>
## 文档

| 文档 | 内容 |
| --- | --- |
| [架构](docs/architecture.zh-CN.md) | 场域、会话、通道、从受理到送达、抢占、围栏、日志、发件箱 |
| [事件](docs/events.zh-CN.md) | 分类、路由不变量、引用、合窗、自定义类型 |
| [目录](docs/catalog.zh-CN.md) | 生成的标准事件类型与工具表 |
| [工具](docs/tools.zh-CN.md) | 工具契约、终止决策、回执、拒绝、预算 |
| [存储](docs/storage.zh-CN.md) | 端口、表、状态机、幂等键 |
| [服务](docs/services.zh-CN.md) | 接口、消息体、错误、认证、拓扑 |
| [集成](docs/integration.zh-CN.md) | 嵌入平台；Multica 映射 |
| [国际化](docs/i18n.zh-CN.md) | 语言包、解析、覆盖、新增语言 |

每篇文档都有英文版（去掉 `.zh-CN`）。

<a id="license"></a>
## 许可证

[MIT](LICENSE)

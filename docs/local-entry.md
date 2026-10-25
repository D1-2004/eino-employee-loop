# 本地持久化 Employee Loop Entry

`app/local` 是直接可运行的参考应用，向 M7 `employee/entry.Open` 注入自己的具体 Provider。它不是供其他应用导入的库。七模块职责见[架构说明](../ARCHITECTURE.md)。身份、场景、业务后端、模型名称、预算和配置修订由 profile 显式声明。SQLite 保存业务状态、输入回执、模型账本和 Eino checkpoint；Markdown 保存任务文件和本地交付回执。独立钉钉接入例子位于 `app/dingtalk`，其内存会话不能代替这里的持久化任务运行时。

这些命令操作本地回放输入和本地文件 outbox。没有创建 DWS 订阅，没有向钉钉发消息；`local-file/...` 回执只证明本地文件已写入并回读。真实 IM 通道需要独立实现 `interaction.Port` 的提交、查询和回执核验。

## 从同一 Entry 跑完整文件交付

在仓库根目录执行；每次实验使用自己的状态目录，避免混入既有任务：

```sh
go build -o /tmp/el ./app/local
/tmp/el -profile evals/cases/local/profile.json -dir /tmp/el-demo -script evals/cases/local/script.json replay -in evals/cases/local/input.ndjson
/tmp/el -profile evals/cases/local/profile.json -dir /tmp/el-demo status
```

第一条业务命令依次接收入站消息、ChatLoop 派发任务、执行器写入和回读 `report.md`、Review 判定 Goal、交付 worker 回读本地 outbox。JSON 输出中应出现 `tasks[].State = completed`、Run 的真实 artifact 路径和 SHA256、`deliveries[].State = applied` 及 `local-file/...` 回执。文件位于状态目录的 `workspace/tasks/.../report.md`，交付文本位于 `workspace/deliveries/*.md`。以输出实际路径为准。

再次执行同一个 `replay` 命令应返回 `admission.Replay = true`，保留原 Task/Run 和交付记录。输入 ID 相同但正文不同会被拒绝；不能更换正文来模拟新消息。重放发生在真实 SQLite 和 Markdown 上，脚本只替换模型决策，不替换工具执行或文件回读。

`-limit` 限定 drain 步数，`-timeout` 限定整个命令时长。等待、未知结果、预算耗尽和失败不会被 CLI 解释为完成。命令返回错误时已持久化的输入仍可通过 `status` 检查，再用 `drain` 推进。`status` 不需要模型凭据，也不会触发模型调用。

## 用户输入、状态和原中断恢复

```sh
/tmp/el -profile evals/cases/local/profile.json -dir /tmp/el-question -script evals/cases/local/ask-script.json submit -id question-1 -scene local-scene -text '请创建一份报告'
/tmp/el -profile evals/cases/local/profile.json -dir /tmp/el-question status
```

在 `pending` 中找到真实 job ID 和 native target ID，再回传同场景、同 owner 的原始回答：

```sh
/tmp/el -profile evals/cases/local/profile.json -dir /tmp/el-question -script evals/cases/local/ask-script.json answer -job '<pending 的 job ID>' -target '<pending 的 target ID>' -id answer-1 -scene local-scene -text '本地验证报告'
```

`answer` 只恢复 `pending` 暴露的 ChatLoop 中断。TaskManager 的业务 Wait 则通过新的 `submit` 消息进入 ChatLoop，由 `answer_question` 工具携带原 Task、Wait 和 Goal revision 返回原 Review；不要把 Task Wait ID 当成 ChatLoop job ID。

`serve` 调用 `Entry.Start`，处理已有持久化队列并等待信号或 `-timeout`。它订阅 `Entry.Errors()`；单条消息、模型或工具调用失败会输出 `worker_error` NDJSON，保留持久化失败记录并继续处理其余工作，不把一条坏消息变成整个服务退出。每秒最多输出一条 worker 诊断；窗口内重复或并发错误合并为 `occurrences` 计数，并报告最后一条错误，退出前刷新剩余诊断。错误不会被当作成功或自动伪造一条 IM 回复。启动失败、输出写入失败、SQLite 损坏/只读/磁盘满等资源错误仍返回失败；context 取消或超时仍显式返回取消原因。它没有监听 stdin 或网络；常用单次流程优先使用 `replay`、`submit` 和 `drain`。同一状态目录由一个 Entry 持有文件锁，不能同时运行另一条 CLI 命令。

## 显式开启维护、表达和包装 Loop

基础 profile 只运行核心任务路径。`allloops-profile.json` 在同一 Entry 中显式启用 Owner 的 attention、memory、dreaming、tone、reflection、summary，以及交付侧的 expression、package。启用名单冻结在 profile 修订中，不会因为代码里存在相应组件就自动开启。

```sh
/tmp/el -profile evals/cases/local/allloops-profile.json -dir /tmp/el-allloops -script evals/cases/local/allloops-script.json replay -in evals/cases/local/allloops-input.ndjson
/tmp/el -profile evals/cases/local/allloops-profile.json -dir /tmp/el-allloops owner-status
```

核验核心 Task 完成及文件后，再看 `owner_documents.documents` 是否包含六个角色的真实文档、原文 evidence、文件路径和 SHA256。交付正文应使用 expression 的文本，附带实际 report 文件和 `packages/.../*.md` 的路径与 SHA256；对应包必须能在状态目录的 workspace 下读到。Owner 的记录是带引用的派生视图，不能替代 Task 状态或原始用户授权。`dreaming` 中的想法也不会直接创建业务任务。

这些示例覆盖当前 profile 可显式配置的维护和交付 Loop。Greeting 和 Routine 使用另外的显式业务入口时，以其具体合同与验收为准；这里不把未调用的功能记作已经运行。

## 原任务上的事件与定时发生项

`schedule` 在已有 Task 与准确 Goal revision 上注册发生条件，不创建替代 Task。源引用必须是已经接收的原始 source，actor 和 text 必须与其完全一致。先把 `evals/cases/local/schedule.json` 中的 `taskId` 换为实际状态输出的 Task ID；本例的 source 与 `allloops-input.ndjson` 一致。

```sh
/tmp/el -profile evals/cases/local/allloops-profile.json -dir /tmp/el-allloops -script evals/cases/local/allloops-script.json schedule -scene local-scene -schedule /absolute/path/schedule.json
/tmp/el -profile evals/cases/local/allloops-profile.json -dir /tmp/el-allloops -script evals/cases/local/allloops-script.json signal -condition report.updated -event evals/cases/local/signal.json
```

`signal` 接收一个原始消息文件，将它绑定到 profile 的 owner/scene，再记录条件事实并 drain。消息文件支持与回放相同的身份映射参数。它不是钉钉发消息命令，也不授予原任务以外的执行权限。

`local-el-schedule/1` 文件显式包含 `id/taskId/goalRevision/source/every/limit`，并在 `condition` 与 RFC3339 `due` 之间选一个。`every` 使用 Go duration 字符串，例如 `0s` 表示单次，`1h` 表示每小时；重复定时必须设置 due 且有明确 limit。定时器以实际时钟推进；一次 `drain` 不会等未来时间，之后可重新运行 drain 或在有界 `serve` 中等待。脚本中的 task_attention 选择 skip，不会因为一个演示发生项开启新的 Run；真实模型的决定仍需核验原 Task 的执行和交付事实。

## 使用自己的模型

复制 profile，设置独立 `id`、`revision` 和明确的 `modelName`，再设置环境变量：

```sh
export LLM_BASE_URL='https://your-compatible-endpoint/v1'
export LLM_MODEL='your-model-name'
# 通过本机凭据管理方式设置 LLM_API_KEY，不写进 profile 或仓库。
/tmp/el -profile /absolute/path/profile.json -dir /tmp/el-real-model replay -in evals/cases/local/input.ndjson
```

`LLM_MODEL` 必须等于冻结 profile 的 `modelName`。模型需要兼容 OpenAI native function calling，完整返回 `finish_reason=tool_calls` 和工具调用 ID。更换模型、预算或指令时增加 profile `revision`；相同修订不能被悄悄覆盖。脚本验证不证明真实模型能完成任务，真实模型运行的 Task、文件和交付证据也需要另行核验。

已通过本机既有方式配置 LLM 环境时，可从完整 profile 生成独立验收配置。下面只读取模型名称，不读取或打印 API key，也不复制凭据到配置：

```sh
python3 - <<'PY'
import json, os
p = json.load(open('evals/cases/local/allloops-profile.json'))
p.update(id='local-allloops-realmodel', revision='realmodel-v1', modelName=os.environ['LLM_MODEL'])
with open('/tmp/el-allloops-realmodel-profile.json', 'w') as f:
    json.dump(p, f, ensure_ascii=False, indent=2)
PY
/tmp/el -profile /tmp/el-allloops-realmodel-profile.json -dir /tmp/el-allloops-realmodel -timeout 10m replay -in evals/cases/local/allloops-input.ndjson
/tmp/el -profile /tmp/el-allloops-realmodel-profile.json -dir /tmp/el-allloops-realmodel status
/tmp/el -profile /tmp/el-allloops-realmodel-profile.json -dir /tmp/el-allloops-realmodel owner-status
```

这组命令没有 `-script`，会产生真实模型调用。验收需同时读回任务 artifact、交付包、本地回执和六份 Owner 文档的原文引用；模型自行说“完成”不能代替这些事实。某个 Loop 失败时保留真实失败状态与日志，不能把脚本结果替作该真实模型的通过证据。

## DWS NDJSON 回放

便携格式每行是一个 `local-dws-replay/1` 对象，字段为 `version/id/scene/actor/text`，示例见 `evals/cases/local/input.ndjson`。`actor` 必须是 profile owner，`scene` 必须显式列在 profile 的 scenes 中。

也可导入事先获取的 `dws event +listen-im -f ndjson` 文本消息记录或 `{type,data}` envelope。解析保留原始记录，不相信模型重写身份；消息 ID、发送者和 conversation ID 缺失时拒绝接入。原始内容中的空格和换行保持不变。

```sh
/tmp/el -profile /absolute/path/profile.json -dir /tmp/el-captured -script evals/cases/local/script.json replay -in /absolute/path/captured.ndjson -native-owner '<捕获记录 sender ID>' -scene-map '{"<捕获记录 conversation ID>":"local-scene"}'
```

`native-owner` 与 `scene-map` 是操作者显式指定的本地模拟映射，不能作为钉钉身份认证的证明。通知和非文本记录会跳过，已接受的重复消息交给 Entry 核验，后续坏行会停止导入并报告行号；之前已持久化的消息不会回滚。

## 可控模型脚本

`local-el-script/1` 脚本按 role、可选 receipt、最近的 `afterTool` 和消息 JSON `contains` 匹配规则，使用第一个匹配结果。`afterTool: ""` 表示还没有工具结果，省略则不限制。规则必须恰好包含一种结果：`tool_calls`、`text` 或 `error`。`arguments` 中完全等于 `${source}` 的字符串会替换为当前模型请求 receipt；ChatLoop 中它就是当前原消息 ID。`${current.id}` 和 `${current.text}` 只从实际冻结输入的 Current 读取，用于 owner citations 的 source_id 和 quote。其他参数不会自动扩权或插入任务 ID。

脚本没有进程内响应序号。每次重建模型后，相同请求仍匹配同一规则，避免 checkpoint 和模型缓存命中导致响应游标错位。规则可以模拟失败和恢复边界，但它不是模型质量评测，也不能替代真实模型验收。

## 显式恢复

`retry -job <Task ID 或 admission ID>` 释放当前 Review hold，或重新排队失败的
Chat 输入。原始来源、配置和已经消耗的模型物理预算保持不变；重试不代表新的授权。
预算耗尽仍会报告真实失败，需要按原任务的继续/纠正合同处理，不能通过重试清账。
未知投递先 lookup 原稳定键，缺少证明会保留 unknown；这不阻挡其他已就绪交付。

Greeting/Routine 通过 ChatLoop 的 `compose_message` 显式受理，`kind` 为 greeting
或 routine。各自使用原生 Agent 和独立预算，结果进入同一 outbox；空闲定时器不自动
发送问候。真正周期工作用 `schedule_task` / `schedule` 绑定同一个 Task，发生次经
Task Attention 继续原 Task 并核验退出，不创建替代任务。

## 服务能力与默认装配

Go 嵌入入口、各组服务覆盖和线上适配边界见[模块组合](module-composition.md)。
默认本地 Config、Messages、Resources、Tools、Files 均返回真实本地事实；模型只读
工具通过冻结的 hostTools 显式启用。allloops profile v2 和对应脚本实际调用
host_read_source，随后写文件、Review 和交付；原有持久 profile 不会被静默开启新工具。

结果的 local_applied/local-file 表示本地文件回读。远端通道的 provider_accepted
保留待查询状态，只有 Lookup 的 delivered 才记入任务交付；PrepareIntent 冻结目的地，
当前 AuthorizeDelivery 再检查目标权限。默认 Local 不支持的附件/卡片明确拒绝。

# Local EL 真实模型验收（2026-10-24）

结果：冻结候选的本地文件交付、六个 Owner Loop、表达/包装、Greeting/Routine 以及 ChatLoop 原生问答恢复通过自动验收。独立真实模型 grounded judge 给出 pass。此结论仅对应下列已执行场景，不代表所有故障、定时或真实 IM 场景通过。

## 版本、环境与证据

使用本机自有模型 `deepseek-v4.1-flash`；业务 CLI 全程未带 `-script`，无脚本模型替代。SQLite 和 Markdown 均为真实本地存储。没有启动 DWS 订阅，没有向钉钉或任何真人发消息。凭据仅通过 Python shlex 解析既有配置中的 LLM_* 注入子进程，未打印 API key 或全量环境，未写入 profile、证据或本报告。

- 工作树基线 HEAD：`f4f032538fb3dbeac0c57558ca377cf5ed6be7b5`。候选包含未提交实现，不能只用该 HEAD 代表本轮被测内容。
- 冻结源码副本：`/private/tmp/el-realmodel-fixed-20261024-nm0lns7v/source`。
- 核心源码 SHA256：`fed8293e963337c265b165f970bcb77a6d52328d6c2c8d06f83ba212d75fd86b`；冻结副本构建前后相同，共 62 个文件。
- 散列规则：按相对路径排序，对所有非测试 `.go` 文件及 `go.mod/go.sum` 顺序连接“路径、NUL、文件字节、NUL”；不含 docs 与 `*_test.go`。
- 二进制 SHA256：`55524bc5598e8bf7b5c36255c28e1e8d2f4e90b4794f1c502203d6c7b43f2104`。
- 主场景 Profile SHA256：`2f2e6c80e2e92bd42e75f8ac8323936a0736e0ca99c48918f2296691a90c16a0`；输入 SHA256：`0f97070f413c46f66469313f176c17dc8c1587c5e7c91805702eaefcf8280055`。
- 主场景证据目录：`/private/tmp/el-realmodel-fixed-20261024-nm0lns7v`。`manifest.json`、`buildinfo.txt`、各 CLI stdout/stderr、完整 `evidence.json`、`citation-checks.json`、`native-repair-checks.json`、judge 请求证据及响应均保留。
- 完整 evidence SHA256：`6bb0ffbf50b323f15b2dd63ef79358029d87b7eacf294a1cd47b7947b26bc75c`。

冻结后新增的三个文件差异为 `cmd/el/main.go`、`entry/entry.go`、`entry/recovery.go`：新增 Failures/Owner retry、CLI status/retry，以及 Open 对 Scenes/OwnerLoops/DeliveryLoops/Plugins/Callbacks 切片的复制，防调用方别名热改配置。它们没有改变本轮使用的普通提交、原生问答、Owner 正常加工或 Greeting/Routine 路径，因此复用该冻结候选的有效证据；后续新增失败恢复和配置别名检查属于主任务另行执行的 SQLite/单测证据，不伪称本轮模型验收已经调用这些新增入口。

最终当前源码另有精确 binary 的脚本验收，与冻结真模型证据分开记录：核心 SHA256 `6e721b680b32b13d98b856f0edda596b5ad2400ee5c2c8d0fef3e786d1aab675`，binary SHA256 `ee858b17a568f98e81e15da539d84e57d279cdb019ef7fdda0f2997e0db86e81`。`/private/tmp/el-final-script-acceptance.json` 及目录 `/private/tmp/el-final-acceptance-20261024-j_lmi99f` 证明全配置与重复 receipt replay 后仍只有 1 Task、1 Run、1 delivery、6 份 Owner 文档，实际文件 SHA 一致，failures=[]。该轮只用脚本控制模型决策，存储、工具与文件回读仍真实；不把它记作第二次真实模型验收。

## 本地任务、方法与实际产物

原始任务要求创建 `report.md`，正文为标题“# 本地验证”、一个空行和正文“状态用 SQLite，文件用 Markdown。”，允许正常文件末尾换行。要求经 dispatch_task 受理、write_file 实际落盘、read_file 回读并核验 SHA256、由 Review 判断 Goal 完成，再返回本地交付记录；明确偏好简洁中文，全部操作限本地。

实际 Task `task-b57bcd3638a109d4db53bfd4` 只有一个 Run。原生工具顺序为 write_file → read_file → finish_run，Run succeeded/ExitProven=true；独立 review 角色随后调用 decide_review(kind=complete)，Goal 才变为 completed。不能把 Run 的成功或 CLI exit=0 单独当成 Goal 完成。

实际文件回读：

```text
# 本地验证

状态用 SQLite，文件用 Markdown。
```

- 文件 `tasks/73cefea15d98bf10ed75dda8dd81a275a6b4a2f1f1cc9b84a4d2724221877b24/task-b57bcd3638a109d4db53bfd4/report.md`，56 字节，SHA256 `c8f16dc679f6ea07cbfc3f504023d1d185d6de4d7b4d2f8c06c77c8159c04123`。
- 该实际内容没有末尾换行，符合本轮明确允许正常换行、未要求额外换行字节的原始输入。没有把脚本的字节长度引入真实验收合同。
- read_file 实际使用 write_file 回执中的完整 workspace 相对路径并成功回读，直接验证本轮路径修复。
- package Loop 写出真实 `packages/.../*.md`，SHA256 `ab11b4181de239bb9877151008250f5cc49909d049985da21b314ca3ca6dfa6c`；expression Loop 生成对外表达。Host 附加实际文件/包装路径及 SHA。
- result、greeting、routine 三条交付均为 applied，ProviderMessageID 均是 `local-file/deliveries/...md`，其 BodySHA256 与实际文件字节一致。这些回执只证明本地文件交付，不证明真实 IM。

独立 CLI `status`、`owner-status` 重新打开状态目录回读。任务文件、包装、本地交付与六份当前 Owner 文档共 11 份文件逐份验证 SHA256 一致；最终 36 条 Owner evidence 逐一查询真实 SQLite sources，scope、revision、源字节 SHA、原文子串、actor 和未撤回状态全部一致。

## 所有显式启用的角色与真实响应

同一 profile 开启 attention、memory、dreaming、tone、reflection、summary 六个 Owner Loop，以及 expression/package。另两条原始输入明确要求 compose_message(kind=greeting/routine)，分别由独立 greeting/routine 角色调用 publish_result；未创建额外业务 Task，Routine 没有注册定时任务。

SQLite 原生模型账本共 32 条响应，全部实际返回完整 `finish_reason=tool_calls`：

| 原生角色 | 实际模型响应数 |
| --- | ---: |
| chat | 3 |
| executor | 3 |
| review | 1 |
| package | 1 |
| expression | 1 |
| owner_attention | 3 |
| owner_memory | 3 |
| owner_dreaming | 3 |
| owner_tone | 4 |
| owner_reflection | 4 |
| owner_summary | 4 |
| greeting | 1 |
| routine | 1 |

这里的 raw response complete 仅表示完整 API 响应已落账，不表示每个候选工具效果都被接受。18 个 Owner 加工 job 对应 18 个实际 owner.updated 事件；额外三次原生响应是修复过程：

- reflection 将诊断 gap `context not written` 误引用为 real-task-1 原文。
- summary 在原文中多写了一次“本地”。
- tone 在引用中插入了原文不存在的 `also`。

三个非法引文均被 Host 原文校验挡住，Eino native repair 后才写入合法视图，没有放宽工具合同，也没有把诊断或模型改写提升为来源事实。同一 reflection job 两条模型响应，只产生一个 owner.updated 事件。

## 最小问答：原 ChatLoop 中断恢复

此例使用同一冻结 binary，新独立目录 `/private/tmp/el-realmodel-question-20261024-9vsm68ly`，新 Profile 修订 `realmodel-question-v1`。OwnerLoops 和 DeliveryLoops 关闭，仅验证问答与后续任务。Profile SHA256：`6891e66ee0c54294f63ff7ef8103ab68657c459b97297feccbb6a12f9fe039cf`。

原始输入：“必须先用 ask_input 问我季度，没回答前不要写文件或假定答案；拿到季度后写 quarter.md，并完成。只操作本地 Markdown，不向任何真人或钉钉发送消息。”

实际发生：

1. 真实模型先调用 ask_input；回答前不存在任何任务产物，没有 quarter.md，也没有提前创建 Task。提问自身的本地 outbox 仍正常持久化。
2. CLI 独立进程读取真实 pending root：job `8eb440aa3217f0260edca8406d4b390beef6d02c1e440cf05dfec37642c21633`，target `8e394a21-3eaa-46bf-9888-8713121ade4c`。
3. CLI `answer -job <原job> -target <原target> -id real-answer-q3 -text Q3` 定向恢复。SQLite 中同一 job 从 interrupted 变为 done，没有新建聊天 job 代替恢复；原始回答 `Q3` 被保留到 Task.Sources。
4. 随后只创建一个 Task、一个 Run，完成 write_file → read_file → finish_run → Review；实际 quarter.md 为 `# Quarter\n\nQ3`，SHA256 `66313f7a2a8d6c83f9209a06e3acec2ee51cbaadf4ef3b6e6e97c43dfe2417c6`，与 Run.Artifacts 完全一致。

该例共 6 条真实模型响应，工具依次是 ask_input、dispatch_task、write_file、read_file、finish_run、decide_review。由于首问发生在派发前，回答前尚无 Task/Run；此处证明的是原 ChatLoop root 恢复后产生唯一 Task/Run，不把它表述为“执行中 Run 暂停与恢复”的真实模型通过证据。完整前后 jobs、native 调用、文件及判定在该目录的 manifest 与 JSON 中。

## Grounded judge 与证据复核

最终独立 judge 为真实 `deepseek-v4.1-flash` 调用，输入包含原始任务、实际报告正文、三个交付摘要、六个 Owner 首段与每类 1–2 条原始引文、独立文件/引用一致性检查，以及非法引文已被拒的事实。完整原始证据继续保留；没有将整个重复账本灌入最终评分输入。

- 最终输入 1625 tokens，输出 577 tokens；finish_reason=stop，有效 JSON，overall=`pass`。
- 请求设置：thinking disabled、max_tokens=2500。网关实测接受该参数并返回可见文本；参数参考 [DeepSeek Thinking Mode 官方文档](https://api-docs.deepseek.com/guides/thinking_mode/)。
- 证据 SHA256：`13842b61c8c21f875c607db9ddc9a16cb9f3d44f3f1c788cf8e96feb2584a5f8`。最终响应：`compact-judge.json`；元数据：`compact-judge-summary.json`。
- judge 对原始方法/正文、真实文件/哈希、Owner 引文范围、dreaming 推测标记、Tone 不臆造、Run 与 Goal 区分、本地收据含义均判 pass。另按原始文件、native 工具顺序、36 条 SQLite 原文引用进行了证据复核，结论有独立事实支撑；没有只接受 completed 标签。

保留的 judge 失败边界：最初 3000-token 和后续 12000-token thinking 请求都将预算耗在 reasoning，finish_reason=length、可见 content 为空，均不算有效 judge。一次 thinking disabled 参数探测得到有效 JSON，但仍是较大上下文；最终按上面的 1625-token 精简证据再次独立评分并收尾，没有掩盖先前失败或把空响应补写成结论。

两份有效 judge 分别保留：第一次有效调用在 `grounded-judge.json` / `grounded-judge-summary.json`，prompt=45609、visible completion=1217、stop、pass；最终精简调用在 `compact-judge.json` / `compact-judge-summary.json`，prompt=1625、visible completion=577、stop、pass。前者是已发送的参数兼容探测结果，后者才是最终小证据评分；两者都是真实模型返回，未合并或改写成同一次调用。

## 首轮真实失败、风险与覆盖边界

首次旧二进制探测中，write_file 返回 `tasks/<owner>/<task>/report.md`，read_file 再次前缀拼接该路径，前两个 Run 报真实文件不存在。Review 没有判为完成，随后进入 waiting；命令 exit=0 仅代表 drain 暂时没有可推进工作。旧构建期间源码变化，明确记录 source_stable=false，仅作定位证据。旧证据目录：`/private/tmp/el-realmodel-20261024-stw1b0a_`。修复后的冻结新目录、新模型请求、成功回读证据与旧失败分开保存，没有复用失败响应作为通过结果。

source-only attention/summary 视图仍可能写“尚无完成证据”，即使 Task 已完成：它们引用的是原始请求快照，不是执行事实。消费者必须继续以 Task/Goal/实际回执为状态权威；不能拿派生注意力文本反向宣称任务未完成或重复派发。此新鲜度差异作为边界保留，没有通过模型措辞把它掩盖。

本报告不主张已验证真实定时/Watch 触发、执行中 Run 的真实模型问答恢复、进程崩溃/未知提交/外部故障注入、跨身份攻击矩阵、线上平台适配或真实 DWS 投递。相关确定性/SQLite/进程测试应单独引用；本地文件回执不能替代线上证据。重跑真实模型时必须使用新 profile/修订与独立状态目录；在同一目录重放会复用既有账本，不算新的真实模型试验。

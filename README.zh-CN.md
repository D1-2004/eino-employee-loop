# Eino Employee Loop

[English](README.md)

基于 [CloudWeGo Eino](https://github.com/cloudwego/eino) 的 Employee Loop。
目录直接对应七个业务模块。`app/local` 提供默认本地装配；接入其他宿主时替换能力端口
或指定模块，保持同一个 Task 状态机。

```text
modules/
├── chat/          M1  前台对话、输入准入、业务命令
├── task/          M2  Worker、Review、计时与 Task/Goal/Run 状态
├── context/       M3  来源引用、授权上下文、冻结视图
├── owner/         M4  Attention、Memory、Dreaming、Tone、Reflection、Summary
├── execution/     M5  原子 Run 桥接与本地原生执行
├── interaction/   M6  表达、消息、Outbox、投递与回执
└── entry/         M7  生命周期、模块装配与注册

app/local/             默认本地装配：SQLite + Markdown + Eino
ports/                 外部服务、持久化接口
adapters/              存储、服务、通道的具体实现
kernel/                通用 Eino 运行及决策能力
contract/              身份、配置、输入、失败等共享值
cmd/el/                产品 CLI
examples/              本地回放、可选钉钉接入例子
```

在仓库根目录运行完整本地例子；使用新的状态目录：

```sh
go run ./cmd/el -profile examples/local-el/allloops-profile.json -dir /tmp/el-demo -script examples/local-el/allloops-script.json replay -in examples/local-el/allloops-input.ndjson
```

脚本只替换模型决策，存储和文件效果实际执行。`local-file` 回执证明本地文件写入，
不能当成钉钉消息送达或完整线上 Buddy 行为还原。

- [架构与 import 迁移](ARCHITECTURE.md)：模块职责和依赖方向。
- [默认装配与替换](docs/module-composition.md)：`app/local`、端口和生命周期。
- [本地 CLI 指南](docs/local-entry.md)：回放、任务、恢复和模型设置。
- [行为合同](docs/specs/README.md)：授权、执行、存储、插件和投递。
- [通用内核 API](docs/kernel/README.md)：单次决策、事件、工作流和结构化生成。

MIT 许可。本轮目录调整会改变 Go import，旧根路径不保留兼容包装；迁移表见
[ARCHITECTURE.md](ARCHITECTURE.md#import-migration)。

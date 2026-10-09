## Why

本 fork 作为个人 coding agent（pi / oh-my-pi / Codex / Claude Code 等）的统一网关。我们希望完整记录经过网关的每一次 LLM 交互，用来研究自己的 coding 过程，并在本地重建模型实际看到的输入前缀（mimic KV-cache）。

现有能力都不满足这个需求：
- `usage_logs` 只记录计量信息；
- ops 错误日志只在出错时截取片段；
- securityaudit 的原则是“完整提示词不落库”，和本需求相反。

LLM API 是无状态的，每次请求都携带完整历史。所以除了录下原始字节，还需要可靠地判断“哪一条是哪一条的后续”，并且能在 Web UI 上回放。

## What Changes

- 新增独立模块 `backend/internal/trajectory/`。在客户端侧（A 请求 / D 响应）和上游侧（B 请求 / C 响应）四个点完整录制原始字节，覆盖 failover 的每个 attempt 和 WebSocket 的每一轮。
- 上游侧采用**装饰 `service.HTTPUpstream`** 的方式录制。客户端侧通过网关路由组上的 gin middleware 录制。Responses WebSocket 单独加一个 hook。
- 录制数据以**磁盘目录结构**为事实来源：`raw/` 只追加，`objects/` 和 `sessions/` 为派生层，`spool/` 为写入缓冲。PostgreSQL 只保存可重建的索引。
- 新增四种协议到统一中间表示（IR）的转换：Anthropic Messages、OpenAI Chat Completions、OpenAI Responses、Gemini。
- 新增基于“规范化消息前缀哈希链”的 trajectory DAG，用于去重、串联、分支识别，以及 subagent / compaction / 辅助调用的分类。
- 新增三级开关：全局、group、API key（覆盖 group），全部默认关闭。
- 新增管理 API `/api/v1/admin/recording/*`，以及前端 “Recording” tab：概览、开关、Sessions、回放、下载。
- 新增离线工具 `tools/trajectory/`：按开源模型的 chat template 渲染 token 序列，用于 KV 研究。

## Non-goals

- 不承诺拿到闭源模型的原始 CoT（Claude 摘要 thinking、OpenAI encrypted reasoning 只能原样保存）。
- 不在 Go 服务内做 tokenizer / chat template 渲染。
- 不修改上游 ent schema、`internal/config/config.go`、已有表和已有行为。
- 不支持 S3 存储（规模不大，以研究便利为先）。
- 第一阶段不开放普通用户侧的 “My recordings” 入口（后端按 user_id 设计，后续复用即可）。

## Capabilities

### New Capabilities

- `trajectory-recording`：录制点、开关、磁盘格式、可靠写入、索引。
- `trajectory-linking`：统一 IR、规范化、前缀哈希 DAG、session 归组与分类。
- `recording-console`：管理 API 与前端 Recording tab（开关、浏览、回放、下载）。

## Impact

- **上游文件改动**仅限“接线”，完整清单见根目录 `AGENTS.md` §4 Hook 清单：wire 装饰、网关路由 middleware、WS forwarder hook、admin 路由、前端路由、侧栏、i18n。
- **数据库**：新增 `trajectory_*` 表，迁移文件前缀为 `9xx_trajectory_`。
- **磁盘**：`$SUB2API_TRAJECTORY_ROOT`（默认 `${DATA_DIR:-./data}/trajectories`）。Claude Code 长 session 的未压缩原始数据可能达到数百 MB，需要监控磁盘占用。
- **性能**：录制关闭时，热路径上只多一次内存中的开关判断。开启时，tee 只做内存拷贝和带缓冲的文件追加，不增加 TTFT。
- **兼容性**：默认关闭，对外 API 行为不变。

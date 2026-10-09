## 0. 准备

- [x] 0.1 配置 `upstream` remote，并新建工作分支 `feat/trajectory-recording`
- [ ] 0.2 （待用户验证）本地跑通后端 + 前端，配好 pi / oh-my-pi / Codex / Claude Code 指向本地网关
- [ ] 0.3 （待用户验证）抓包确认 pi / oh-my-pi 使用的协议与 session hint，结果写回 AGENTS.md §7

## 1. 录全（Phase 1：raw 层）

- [x] 1.1 `internal/trajectory/` 骨架：`ProviderSet`、`Exchange` / `Attempt` 句柄、`FromContext` / `WithExchange`
- [x] 1.2 开关：迁移 `900_trajectory_init.sql`（`trajectory_scopes`）+ repository + 进程内快照 + 判定函数（单测）
- [x] 1.3 磁盘 writer：spool → rename → index.ndjson，启动时恢复 spool；`FORMAT.md` 写入（单测：崩溃恢复、并发、截断）
- [x] 1.4 `UpstreamDecorator`：包装 Do / DoWithTLS，录 B / C 和 timing，统计 orphan（单测：body 放回、GetBody、流式 tee、Close 提前）
- [x] 1.5 gin middleware：录 A / D，计算开关；在 `routes/gateway.go` 各路由组 `apiKeyAuth` 之后挂载（更新 Hook 清单）
- [ ] 1.6 wire 接线 + `go generate`（更新 Hook 清单）
- [x] 1.7 header 脱敏（单测）
- [x] 1.8 meta.json：从 C 尽力解析 usage / response_id / model（四种协议）
- [ ] 1.9 覆盖测试：对每个网关路由组发起请求（mock 上游），断言产生 exchange 且 orphan=0
- [ ] 1.10 （待用户验证）真实流量验证：Responses（SSE）、Chat Completions（含开源模型 reasoning_content）、Anthropic、Gemini 各至少一个完整 agent session
- [ ] 1.11 benchmark：开关前后 TTFT / 吞吐对比

## 2. WebSocket

- [ ] 2.1 在 `openai_ws_forwarder.go` 加 hook 接口（更新 Hook 清单）
- [ ] 2.2 WS 每轮落定为一个 exchange，并用 `ws.connection_id` 串起来
- [ ] 2.3 用 Codex 的 WS 模式做真实验证

## 3. 索引与 IR

- [ ] 3.1 迁移：`trajectory_exchanges`、`trajectory_sessions`；异步索引写入 + `reindex` 命令
- [ ] 3.2 IR 类型定义 + 四个协议适配器（请求解析 + 流式拼接），用 golden fixture 测试
- [ ] 3.3 Responses `previous_response_id` 链展开

## 4. 串联（trajectory DAG）

- [ ] 4.1 规范化 + 前缀哈希链（单测：cache_control 移动、key 重排、thinking 剥离）
- [ ] 4.2 DAG 构建：后续 / 分支 / 宽松匹配等级
- [ ] 4.3 session 归组（hint 优先，DAG 连通分量兜底）
- [ ] 4.3a compaction 串联：摘要内容匹配（Claude Code / pi）+ Responses compact item 精确匹配；rewrite 检测（tool_call id 锚点对齐）；Anthropic `context_management` 的 applied_edits 记录
- [ ] 4.4 分类：main / subagent / compaction / auxiliary / count_tokens / warmup
- [ ] 4.5 生成派生层 `objects/` + `sessions/`（可删除后重建，带规则版本号）
- [ ] 4.6 用真实 session 校验：本地前缀长度和上游 cache_read 的一致性报告

## 5. 管理 API

- [ ] 5.1 status / scopes
- [ ] 5.2 sessions / exchanges 查询，原始 part 分段读取
- [ ] 5.3 下载：session tar.zst、按日期范围导出、IR jsonl
- [ ] 5.4 在 `routes/admin.go` 挂载（更新 Hook 清单）

## 6. 前端 Recording tab

- [ ] 6.1 `features/recording/` 骨架 + 路由 + 侧栏 + i18n（中英，更新 Hook 清单）
- [ ] 6.2 概览页
- [ ] 6.3 开关页（group / API key 三态）
- [ ] 6.4 Sessions 列表
- [ ] 6.5 回放页：DAG 树 + 时间线 + thinking / tool 渲染 + 前缀复用条
- [ ] 6.6 diff 视图 + 原始 A/B/C/D 查看器
- [ ] 6.7 下载入口
- [ ] 6.8 vitest + typecheck + lint

## 7. 离线工具与收尾

- [ ] 7.1 `tools/trajectory/render_tokens.py`（HF chat template）
- [ ] 7.2 duckdb 查询示例
- [ ] 7.3 第一次 rebase 上游演练，按 Hook 清单核对，并记录流程到 AGENTS.md

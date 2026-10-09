# Design: Trajectory Recording

> 背景与仓库调研结论见根目录 `AGENTS.md`。本文只写设计。

## 1. 录制点与数据流

```
client ─(A)─> [mw: trajectory.Middleware] ─> handler ─> service ─(B)─> [trajectory.UpstreamDecorator] ─> HTTPUpstream ─> 上游
client <─(D)─ [tee c.Writer]                                    <─(C)─ [tee resp.Body]
```

| 点 | 内容 | 采集位置 | 用途 |
|---|---|---|---|
| A | 客户端原始请求 body + headers | middleware（位于 apiKeyAuth 之后）读取 body 后再放回 | 客户端视角，session hint |
| B | 改写后真正发往上游的 body + URL + headers | HTTPUpstream decorator 读取 `req.Body`，并通过 `GetBody` 或重新包装 body 放回 | **KV 重建与 DAG 的依据** |
| C | 上游原始响应字节流 + 状态码 + headers + 每块时间戳 | decorator 把 `resp.Body` 包成 tee ReadCloser | 模型输出与 usage |
| D | 回给客户端的字节流 | middleware 包装 `gin.ResponseWriter` 并 tee | 客户端视角 |

### Exchange 句柄

- middleware 在 `apiKeyAuth` 之后运行，从 request context 读取 `ctxkey.UserID`、`ctxkey.Group`，从 gin context 读取 API key。然后计算开关：
  - 不录：直接 `c.Next()`，热路径成本只有一次内存判断。
  - 录：创建 `*trajectory.Exchange`，写入 `c.Request` 的 context（使用包私有 key），并开始录 A 和 D。
- decorator 的 `Do` / `DoWithTLS` 调用 `trajectory.FromContext(req.Context())` 取出句柄：
  - 有句柄：调用 `ex.BeginAttempt(...)`，录 B 和 C。`AccountID` / `RetryCount` 等信息从 context 和参数里取。
  - 无句柄但开关为开：记一次 **orphan** 计数并打日志（不含 body）。这是发现覆盖漏洞的主要手段（例如 service 用了 `context.Background()`）。
- Exchange 的结束：middleware 在 `c.Next()` 返回后调用 `ex.Finish()`。此时 C 可能还没读完（例如上游被中途放弃），attempt 的 ReadCloser 在 `Close()` 时自行结束。Exchange 用引用计数，等全部 attempt 结束后再落定。
- 客户端断开、上游中断、panic：Exchange 被标记为 `incomplete`，并保留已经收到的字节。

### WebSocket（OpenAI Responses WS / Codex）

- 一条 WS 连接上有多轮。**每一轮 `response.create` 和对应的事件流算一个 exchange**，由一个 connection 对象把这些 exchange 串起来（`ws_connection_id`）。
- 在 `openai_ws_forwarder.go` 的转发循环中加一个 hook 接口：`OnClientFrame` / `OnUpstreamFrame` / `OnTurnEnd`。forwarder 中只调用接口，不包含任何录制逻辑。
- `/v1/live` realtime：放在第二阶段（音频，暂不需要）。

### 不录的东西

- 鉴权 header 统一脱敏：`authorization`、`x-api-key`、`x-goog-api-key`、`cookie`、`chatgpt-account-id` 等，保留 scheme 和末 4 位。
- `count_tokens`、warmup 拦截、`/models` 这类请求可单独配置是否录，默认 **录 count_tokens 和 warmup 的元数据，不录 GET**。

## 2. 磁盘格式（事实来源）

根目录：`$SUB2API_TRAJECTORY_ROOT`，默认 `${DATA_DIR:-./data}/trajectories`。

```
<root>/
├── FORMAT.md                          # format_version 与字段说明（由程序写入，随版本更新）
├── raw/                               # 只追加；exchange 目录 rename 进来之后不再修改
│   └── 2026-10-09/                    # UTC 日期
│       ├── index.ndjson               # 当天每个已落定的 exchange 一行摘要（追加写，带互斥）
│       └── u<user_id>/
│           └── <exchange_id>/          # UUIDv7，字典序即时间序
│               ├── meta.json
│               ├── client.req.json     # A（body 原样；非 JSON 时用 .bin）
│               ├── client.resp.sse     # D（流式）/ client.resp.json（非流式）
│               └── attempts/
│                   └── 0/
│                       ├── upstream.req.json
│                       ├── upstream.resp.sse    # 或 .json
│                       └── upstream.timing.ndjson   # {"off":<字节偏移>,"t":<相对开始的毫秒>}
├── spool/                             # 正在写的 exchange：spool/<exchange_id>/（结构同上）
├── objects/                           # 派生：内容寻址块 <sha256 前2位>/<sha256>.json.zst
└── sessions/                          # 派生：u<user_id>/<session_key>/{session.json, main.ir.jsonl, branches/, exchanges/ → 符号链接}
```

### meta.json（format_version 1）

```jsonc
{
  "format_version": 1,
  "exchange_id": "0192...",
  "status": "complete | incomplete | error",
  "started_at": "...", "finished_at": "...",
  "user_id": 12, "api_key_id": 34, "api_key_name": "pi-laptop", "group_id": 5, "group_name": "...",
  "client": { "method": "POST", "path": "/v1/responses", "protocol": "openai_responses",
              "stream": true, "headers": {...脱敏...}, "user_agent": "...", "client_request_id": "...",
              "detected_client": "claude-code | codex | pi | unknown",
              "session_hint": { "source": "anthropic_metadata | codex_session_header | ...", "value": "..." } },
  "attempts": [
    { "index": 0, "account_id": 7, "platform": "openai", "upstream_protocol": "openai_responses",
      "url": "https://.../v1/responses", "status_code": 200, "ttfb_ms": 812, "duration_ms": 15320,
      "bytes_req": 183201, "bytes_resp": 40211, "outcome": "success | failover | error | aborted" }
  ],
  "final_attempt": 0,
  "model": { "requested": "gpt-5.5", "upstream": "gpt-5.5-2026-..." },
  "usage": { "input": 0, "output": 0, "cache_read": 0, "cache_write": 0, "reasoning": 0 },   // 从 C 解析，尽力而为
  "ws": { "connection_id": "...", "turn": 3 },   // 仅 WS
  "response_id": "resp_...",                     // Responses：用于 previous_response_id 串联
  "truncated": false
}
```

### 可靠写入

- **热路径不碰磁盘**：A、B 与初始 meta 交给后台 goroutine 写入；C、D 的 tee 只把字节拷贝进内存队列（`asyncSink`），
  由每个流各自的后台 goroutine 负责 mkdir / open / 带缓冲追加写入（不做 fsync）。磁盘变慢时数据暂存在内存，不会卡住流式输出。
  需要解析 body 的 meta 字段（stream、session hint、requested model）在落定阶段计算。
- 落定时：关闭文件 → 写 `meta.json` → `rename(spool/<id>, raw/<date>/u<uid>/<id>)` → 追加 `index.ndjson` → 异步写 PG 索引（失败会重试，且可通过 reindex 补齐）。
- 启动时扫描 `spool/`：残留目录补写 `meta.json`，标为 `status=incomplete`，然后归档。
- **没有内存队列，也就没有“队列满了丢弃”的路径。** 磁盘写满或写入失败时，录制自动降级为关闭，并在概览页报警；**网关请求本身不受影响**。
- 单个 body 的上限 `SUB2API_TRAJECTORY_MAX_BODY_BYTES`（默认 256MB），超过时截断并设置 `truncated=true`。
- raw 不做压缩（方便 cat / jq / grep）。需要压缩时建议用文件系统透明压缩（btrfs / zfs）；后续可加“N 天后转 zst”的后台任务。

## 3. 开关

- 自有表 `trajectory_scopes(scope_type text CHECK IN ('global','group','api_key'), scope_id bigint, enabled boolean, updated_at)`，主键为 `(scope_type, scope_id)`。
- 判定规则：`global && (api_key 覆盖值存在 ? api_key 覆盖值 : group 开关)`，全部默认 false。
- 开关缓存在进程内（atomic 快照）。admin 修改后本进程立即刷新，多实例通过 Redis pub/sub 失效（单实例部署时可以忽略）。

## 4. PostgreSQL 索引（可重建）

- `trajectory_exchanges`：exchange_id PK，以及 started_at、user_id、api_key_id、group_id、client_protocol、upstream_protocol、model、status、detected_client、session_key、raw_path、usage 字段、response_id、previous_response_id、prefix_hash、head_hash。
- `trajectory_sessions`：session_key PK，user_id、首末时间、exchange 数、主干长度、标题（第一条 user 消息截断）。
- 命令 `sub2api trajectory reindex [--from DATE]`：扫描 `raw/`，重建上述表和派生层。
- 迁移：`9xx_trajectory_*.sql`（例如 `900_trajectory_init.sql`），只建 `trajectory_*` 表。

## 5. 统一 IR

```jsonc
// Turn / Message
{ "role": "system | developer | user | assistant | tool",
  "blocks": [
    { "type": "text", "text": "..." },
    { "type": "thinking", "text": "...", "signature": "...", "visibility": "raw | summary" },
    { "type": "redacted_thinking", "data": "..." },
    { "type": "reasoning_encrypted", "data": "...", "summary": ["..."] },
    { "type": "tool_call", "id": "...", "name": "...", "arguments": { } , "arguments_raw": "..." },
    { "type": "tool_result", "call_id": "...", "is_error": false, "content": [ /* blocks */ ] },
    { "type": "image", "media_type": "image/png", "object": "sha256:..." },
    { "type": "unknown", "raw": { } }        // 不认识的块原样保留，永不丢失
  ] }
// Request IR 还包括：system、tools（object 引用）、sampling 参数、model
```

- 适配器：`anthropic`、`openai_chat`、`openai_responses`、`gemini`。每个适配器实现两个函数：`ParseRequest(body) → RequestIR`、`ParseStream(sse bytes) → []Message + Usage`。
- Chat Completions 的 CoT 提取顺序：`reasoning_content` → `reasoning` → content 开头的 `<think>…</think>`。
- 流式拼接：Chat 的 `tool_calls[i].function.arguments` 增量、Responses 的 `response.output_item.*` 事件、Anthropic 的 `content_block_delta`、Gemini 的分块 `candidates`。
- **测试**：每种协议各用一组真实录制的样本（脱敏后）作为 golden fixture，放在 `backend/internal/trajectory/testdata/`。

## 6. 串联与去重（trajectory DAG）

以 **B（上游请求）的 IR** 为准。

1. **规范化** `canon(msg)`：去掉 `cache_control`；JSON key 排序；tool_call 的 arguments 解析后再序列化；去掉 signature / encrypted 字段的影响，只用于哈希，存储时保留原文；压缩空白差异。图片按内容哈希。
2. **哈希链**：`h_0 = H(model_family ‖ canon(system) ‖ canon(tools))`，`h_i = H(h_{i-1} ‖ canon(msg_i))`。一个请求就是一条路径 `[h_0 … h_n]`，响应在其后追加节点 `h_{n+1}…`。
3. **父子关系**：新请求的路径与已有节点的最长公共前缀。如果命中的是上一轮的响应节点，就是“后续”；如果命中的是中间节点，就是“分支”（重试、rewind、编辑）。
4. **宽松匹配**：如果响应节点和下一轮请求中对应的 assistant 消息不完全相等（客户端剥掉了 thinking、重排了 key），按以下降级顺序比较：
   - 去掉 thinking 后比较；
   - 只比较 text + tool_call (name, id)。
   匹配等级记录在边上（`exact | no_thinking | loose`），UI 中可以看到。
5. **显式指针**：Responses 的 `previous_response_id` 直接通过 `response_id` 索引找到父节点；B 中缺失的上下文，沿链展开后拼出“模型实际看到的完整输入”。
6. **Session 归组**（`session_key`）：优先使用客户端 session hint（Claude Code `metadata.user_id` 中的 session uuid、Codex `session_id` / `conversation_id` header、pi 的 hint 待抓包确认）；没有 hint 时，用 DAG 连通分量 + 时间窗口。
7. **分类**：
   - main：最长且持续增长的链；
   - subagent：不同的 `h_0`，时间上落在某个 main 链 tool_call 的执行窗口内，挂到该 tool_call 下；
   - compaction_request / compaction 新根：见 §6.1，使用内容匹配，不靠时间；
   - auxiliary：小模型的短调用，例如生成标题、话题检测；
   - count_tokens / warmup。
   分类是启发式的，都可以重算；规则版本号写入 `session.json`。
8. **边的类型**：`continue`（前缀延伸）、`branch`（从中间节点分叉）、`compaction`（摘要接续）、`rewrite`（历史被原地改写）、`subagent`。每条边都带 evidence（匹配方式和匹配到的片段位置），保证可以审计。
9. **objects/**：规范化后的大块内容（system、tools、单条消息、图片）按 sha256 存一份，`sessions/*.ir.jsonl` 中只引用它们的 hash，从而实现去重。

### 6.1 Compaction 与历史改写

一次 compaction 会产生两个 exchange，**两个都会被录下来**：

1. **压缩请求 X**：路径 = 主链前缀 + 一条“请总结以上对话”的指令消息（Claude Code `/compact`、自动压缩、pi 等都是这种形式）。分类为 `compaction_request`，挂在主链上，不算主链的后续。X 的响应就是摘要文本 S。
2. **新根 Y**：之后的请求以 system + “本会话从之前的对话继续……” + S 开头，前缀哈希和原链完全不同。

**串联方式：内容匹配，不靠时间猜。** 如果 Y 的首条 user 消息（规范化后）包含 X 响应中的 S（允许前后附加文本），就建立 `X ─compaction→ Y` 边，evidence 记录 S 在 Y 中的偏移。由于摘要很长，几乎不会误配。

**服务端压缩（Codex / Responses）**：`POST /v1/responses/compact`（以及 `/backend-api/codex/responses/compact`）会返回一个带 `encrypted_content` 的 compaction item。后续请求的 input 里原样带回这个 item，所以用该 item 的 `encrypted_content` 哈希**精确匹配**即可，这比 Claude Code 的情况更好处理。注意 subpath 路由要确认被 middleware 覆盖到。

**原地改写（rewrite）**：有些客户端不新开链，而是改写历史中间的消息，例如 Claude Code 把旧的 tool_result 替换为“已清除”占位、剥离旧 thinking、注入或替换 `<system-reminder>`。这会让前缀哈希在中间断开，如果不处理，就会被误判为 branch。处理方法：
- 用 **tool_call id 作锚点**做对齐（id 在改写前后保持不变），对齐后逐条比较，记录哪些位置被改写、改成了什么，建立 `rewrite` 边。
- 判定为 rewrite 的条件是：消息数和锚点序列一致，只有内容不同。否则仍然按 branch 处理。

**服务端上下文编辑（Anthropic `context_management`）**：上游会在服务端清除工具结果，**B 不等于模型实际看到的内容**。需要把请求里的 `context_management` 参数和响应里的 `applied_edits` 记入 meta，重建时再应用。这条要单独标注，因为它会影响 KV 重建的正确性。

**对 KV 研究的意义**：compaction 和 rewrite 都会让上游 cache 从断点处失效。UI 时间线上用“分段线”标出 compaction（可展开查看摘要和被丢弃的轮数），用“改写标记”标出 rewrite 的位置；前缀复用条会在这些位置归零或回落，可以和上游的 cache_read 对照验证。

## 7. 管理 API（`/api/v1/admin/recording`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/status` | 开关、根目录、磁盘占用、spool 积压、orphan / 写失败计数、format_version |
| GET/PUT | `/scopes` | 全局 / group / api_key 开关列表和修改 |
| GET | `/sessions` | 分页，可按 user / group / key / client / model / 时间 / 关键字筛选 |
| GET | `/sessions/:key` | DAG + 主干 IR |
| GET | `/exchanges` | 原始 exchange 分页 |
| GET | `/exchanges/:id` | meta + IR；`?part=client.req|client.resp|attempts/0/upstream.req|...` 返回原始字节 |
| GET | `/sessions/:key/download` | `.tar.zst`，包含 raw 与派生数据 |
| GET | `/export?from=&to=&format=raw|ir_jsonl` | 按日期范围批量导出（流式输出） |
| POST | `/reindex` | 触发重建（后台任务） |

鉴权方式复用现有的管理员 middleware 和管理操作审计。

## 8. 前端 Recording tab（`frontend/src/features/recording/`）

- **概览**：状态卡片和告警。
- **开关**：group 表格和 API key 表格，每行一个 toggle；API key 覆盖为三态（继承 / 开 / 关）。
- **Sessions**：列表和筛选。
- **回放**：
  - 左侧 DAG 树；右侧时间线。
  - system 和 tools 默认折叠；thinking 可折叠；tool_call 与 tool_result 成对显示。
  - 每轮显示前缀复用条，对比本地前缀哈希和上游 cache 命中。
  - 相邻轮次的请求 diff；原始 A/B/C/D 查看器（大文件分段加载）。
- **下载**：单个 session、按日期范围、IR jsonl。

## 9. 离线工具（`tools/trajectory/`）

- `render_tokens.py`：读取 `sessions/*/main.ir.jsonl` 或 raw B，用 HF tokenizer 的 `apply_chat_template` 渲染 token 序列，并输出每轮前缀复用长度。
- `query.sql` 示例：用 duckdb 直接查询 `raw/*/index.ndjson`。

## 10. 风险与对策

| 风险 | 对策 |
|---|---|
| service 用脱离请求的 context 调用上游，导致漏录 | orphan 计数 + 告警；上线前按路由做覆盖测试 |
| 读取 B body 影响原请求 | decorator 读完后用 `bytes.NewReader` 重新放回，并设置 `GetBody` / `ContentLength`；单测覆盖 HTTP/2 和重试 |
| tee 拖慢流式输出 | 只做内存拷贝和 bufio 写入；benchmark 对比开关前后的 TTFT |
| 上游 rebase 冲突 | 遵守 AGENTS.md §4；每次 rebase 后核对 Hook 清单并跑覆盖测试 |
| 规范化规则不断变化 | 派生层可以重建，并带规则版本号；raw 永不修改 |
| 磁盘膨胀 | 概览页显示占用；后续增加压缩归档和保留策略 |

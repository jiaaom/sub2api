## Purpose

规定 trajectory 录制的范围、开关、磁盘格式与可靠性，保证经过网关的 LLM 交互被完整、可重建地记录，并且不影响网关自身行为。

## ADDED Requirements

### Requirement: 四点完整录制
对启用录制的请求，系统 MUST 记录客户端原始请求（A）、每次上游尝试的实际请求（B）、上游原始响应字节（C）和回给客户端的响应字节（D）。B 和 C MUST 按 failover attempt 分别保存。

#### Scenario: failover
- **WHEN** 一次客户端请求先在账号 X 上失败，随后在账号 Y 上成功
- **THEN** exchange MUST 包含两个 attempt，各自带有 B / C 和结果标记，`final_attempt` 指向 Y

#### Scenario: 协议转换
- **WHEN** 客户端以 Chat Completions 请求，上游以 Responses 转发
- **THEN** meta MUST 分别记录 `client.protocol=openai_chat` 和 `upstream_protocol=openai_responses`，A 和 B 都原样保存

### Requirement: 录制不影响网关行为
录制 MUST NOT 改变发往上游与回给客户端的字节，MUST NOT 阻塞流式输出。录制失败（磁盘满、IO 错误）时，系统 MUST 自动停止录制并上报告警，网关请求 MUST 正常完成。

#### Scenario: 磁盘写满
- **WHEN** 录制过程中写入失败
- **THEN** 当前请求 MUST 正常返回，录制状态 MUST 变为降级，并在概览页可见

### Requirement: 不因缓冲区满而丢弃
系统 MUST NOT 存在因内存队列满而丢弃录制数据的路径。未正常落定的 exchange MUST 在下次启动时被恢复，并标记为 `incomplete`。

#### Scenario: 进程崩溃
- **WHEN** 进程在流式响应进行中退出
- **THEN** 重启后该 exchange MUST 出现在 `raw/` 下，`status=incomplete`，并保留已收到的字节

### Requirement: 磁盘是事实来源
`raw/` 下的 exchange 目录在落定后 MUST NOT 被修改。PostgreSQL 索引与 `objects/`、`sessions/` 派生层 MUST 能仅凭 `raw/` 完整重建。

#### Scenario: 重建
- **WHEN** 管理员删除派生层和索引表后执行 reindex
- **THEN** 会话列表与回放结果 MUST 与删除前一致（在规则版本不变的前提下）

### Requirement: 三级开关，默认关闭
是否录制 MUST 由“全局开关 ∧（API key 覆盖值，若存在；否则 group 开关）”决定，所有开关默认关闭。修改开关 MUST 在下一个请求生效。

#### Scenario: key 覆盖
- **WHEN** 全局开启、group 关闭，某个 API key 的覆盖值为开
- **THEN** 该 key 的请求 MUST 被录制，同 group 其他 key 的请求 MUST NOT 被录制

### Requirement: 凭据脱敏
录制的 headers 中的鉴权凭据（authorization、x-api-key、x-goog-api-key、cookie 等）MUST 被脱敏，body MUST 原样保存。

### Requirement: 覆盖漏洞可观测
开关开启时，若上游调用无法关联到 exchange，系统 MUST 计入 orphan 计数，并在状态接口中暴露。

#### Scenario: 脱离请求 context 的上游调用
- **WHEN** 某个 service 用 `context.Background()` 调用 HTTPUpstream
- **THEN** orphan 计数 MUST 增加，并记录路径和平台（不含 body）

package trajectory

// formatDoc 写入 <root>/FORMAT.md，随 FormatVersion 更新。
const formatDoc = "# sub2api trajectory recording — on-disk format v1\n" + `
本目录由 sub2api 的 trajectory 模块写入。设计文档：openspec/changes/add-trajectory-recording/design.md。

## 布局

    raw/<UTC 日期>/index.ndjson                     当天每个已落定 exchange 一行摘要（可直接用 jq / duckdb 查询）
    raw/<UTC 日期>/u<user_id>/<exchange_id>/        一个 exchange（UUIDv7，字典序即时间序）；落定后不再修改
        meta.json                                  元数据（format_version、身份、协议、attempts、usage、状态…）
        client.req.<ext>                           (A) 客户端原始请求 body
        client.resp.<ext>                          (D) 回给客户端的响应字节
        attempts/<n>/upstream.req.<ext>            (B) 实际发往上游的请求 body（KV 重建以此为准）
        attempts/<n>/upstream.resp.<ext>           (C) 上游原始响应字节
        attempts/<n>/upstream.timing.ndjson        C 的分块时间线：{"off":字节偏移,"n":块大小,"t":距 attempt 开始的毫秒}
    spool/<exchange_id>/                            正在写入的 exchange；进程重启时会被标为 incomplete 并归档到 raw/
    objects/、sessions/                             派生层（可删除后重建；Phase 3/4 生成）

扩展名：.json / .sse（text/event-stream）/ .txt / .multipart / .bin。
鉴权 header 与 URL 中的凭据参数已脱敏（保留末 4 位）；body 原样保存。

## meta.json 关键字段

- status：complete | incomplete（客户端断开、上游中断、崩溃恢复、强制落定）| error（handler panic）
- client.protocol / attempts[].upstream_protocol：anthropic_messages | openai_chat | openai_responses | gemini | …
- attempts[].outcome：success（读到 EOF）| closed_early（未取消、调用方读到终止事件后主动关闭，数据完整）| http_error | error | aborted（请求已取消时被关闭）| abandoned
- final_attempt：最后一个 success / closed_early 的 attempt 下标
- usage：按上游原始口径（Anthropic input 不含缓存命中；OpenAI input 含缓存命中）
- truncated：任一 body 超过 SUB2API_TRAJECTORY_MAX_BODY_BYTES 被截断
`

-- Trajectory 索引（可随时清空后由 raw/ 重建，见 internal/trajectory/index.go）。
-- 只建 trajectory_* 表，不触碰上游表。

CREATE TABLE IF NOT EXISTS trajectory_exchanges (
    exchange_id          TEXT        PRIMARY KEY,
    raw_path             TEXT        NOT NULL,
    started_at           TIMESTAMPTZ NOT NULL,
    finished_at          TIMESTAMPTZ,
    user_id              BIGINT      NOT NULL,
    api_key_id           BIGINT      NOT NULL DEFAULT 0,
    group_id             BIGINT      NOT NULL DEFAULT 0,
    status               TEXT        NOT NULL,
    status_code          INT         NOT NULL DEFAULT 0,
    client_path          TEXT        NOT NULL DEFAULT '',
    client_protocol      TEXT        NOT NULL DEFAULT '',
    upstream_protocol    TEXT        NOT NULL DEFAULT '',
    detected_client      TEXT        NOT NULL DEFAULT '',
    model                TEXT        NOT NULL DEFAULT '',
    session_id           TEXT        NOT NULL,
    session_hint         TEXT        NOT NULL DEFAULT '',
    input_tokens         BIGINT      NOT NULL DEFAULT 0,
    output_tokens        BIGINT      NOT NULL DEFAULT 0,
    cache_read_tokens    BIGINT      NOT NULL DEFAULT 0,
    cache_write_tokens   BIGINT      NOT NULL DEFAULT 0,
    reasoning_tokens     BIGINT      NOT NULL DEFAULT 0,
    response_id          TEXT        NOT NULL DEFAULT '',
    previous_response_id TEXT        NOT NULL DEFAULT '',
    -- 前缀哈希链（design.md §6）：h0 与三个规范化等级下的请求链尾 / 请求+响应链尾
    root_hash            TEXT        NOT NULL DEFAULT '',
    req_exact            TEXT        NOT NULL DEFAULT '',
    req_nothink          TEXT        NOT NULL DEFAULT '',
    req_loose            TEXT        NOT NULL DEFAULT '',
    head_exact           TEXT        NOT NULL DEFAULT '',
    head_nothink         TEXT        NOT NULL DEFAULT '',
    head_loose           TEXT        NOT NULL DEFAULT '',
    req_messages         INT         NOT NULL DEFAULT 0,
    resp_messages        INT         NOT NULL DEFAULT 0,
    tools                INT         NOT NULL DEFAULT 0,
    -- 串联结果
    parent_id            TEXT,
    edge                 TEXT        NOT NULL DEFAULT 'root',
    match_level          TEXT        NOT NULL DEFAULT '',
    prefix_messages      INT         NOT NULL DEFAULT 0,
    kind                 TEXT        NOT NULL DEFAULT 'main',
    -- compaction 串联用：纯文本响应的规范化开头与总长度
    resp_text_head       TEXT        NOT NULL DEFAULT '',
    resp_text_len        INT         NOT NULL DEFAULT 0,
    title                TEXT        NOT NULL DEFAULT '',
    index_error          TEXT        NOT NULL DEFAULT '',
    rules_version        INT         NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_user_started ON trajectory_exchanges (user_id, started_at);
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_session ON trajectory_exchanges (session_id, started_at);
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_parent ON trajectory_exchanges (parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_response ON trajectory_exchanges (response_id) WHERE response_id <> '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_req_exact ON trajectory_exchanges (req_exact) WHERE req_exact <> '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_req_nothink ON trajectory_exchanges (req_nothink) WHERE req_nothink <> '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_req_loose ON trajectory_exchanges (req_loose) WHERE req_loose <> '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_head_exact ON trajectory_exchanges (head_exact) WHERE head_exact <> '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_head_nothink ON trajectory_exchanges (head_nothink) WHERE head_nothink <> '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_head_loose ON trajectory_exchanges (head_loose) WHERE head_loose <> '';

CREATE TABLE IF NOT EXISTS trajectory_sessions (
    session_id        TEXT        PRIMARY KEY,
    user_id           BIGINT      NOT NULL,
    hint_source       TEXT        NOT NULL DEFAULT '',
    hint              TEXT        NOT NULL DEFAULT '',
    detected_client   TEXT        NOT NULL DEFAULT '',
    first_at          TIMESTAMPTZ NOT NULL,
    last_at           TIMESTAMPTZ NOT NULL,
    exchanges         INT         NOT NULL DEFAULT 0,
    input_tokens      BIGINT      NOT NULL DEFAULT 0,
    output_tokens     BIGINT      NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT      NOT NULL DEFAULT 0,
    last_model        TEXT        NOT NULL DEFAULT '',
    max_messages      INT         NOT NULL DEFAULT 0,
    title             TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_trajectory_sessions_last ON trajectory_sessions (last_at DESC);
CREATE INDEX IF NOT EXISTS idx_trajectory_sessions_user_last ON trajectory_sessions (user_id, last_at DESC);

-- 索引状态（rules_version 等）。版本变化时启动后自动全量重建。
CREATE TABLE IF NOT EXISTS trajectory_index_state (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

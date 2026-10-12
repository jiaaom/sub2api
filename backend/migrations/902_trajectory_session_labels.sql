-- 会话的自定义名称。单独建表：trajectory_sessions 是可重建的派生索引，重建时会被清空；
-- session_id 是确定性的（用户 + 会话标识的哈希，或首个 exchange id 的哈希），重建后仍能对上。

CREATE TABLE IF NOT EXISTS trajectory_session_labels (
    session_id TEXT        PRIMARY KEY,
    label      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

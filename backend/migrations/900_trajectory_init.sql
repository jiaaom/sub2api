-- Trajectory recording（fork 专有，见 openspec/changes/add-trajectory-recording）。
-- 使用 9xx_trajectory_ 前缀与上游迁移隔离；只建 trajectory_* 表，不触碰上游表。

CREATE TABLE IF NOT EXISTS trajectory_scopes (
    scope_type TEXT        NOT NULL CHECK (scope_type IN ('global', 'group', 'api_key')),
    scope_id   BIGINT      NOT NULL,
    enabled    BOOLEAN     NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (scope_type, scope_id)
);

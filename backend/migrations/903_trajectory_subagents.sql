-- 子 agent 串联（派生索引，可重建）。
-- spawn_calls：该 exchange 响应里的 tool_call 中较长的字符串参数（规范化后的开头），
-- 供后续新根按内容匹配“是哪次工具调用派出的子 agent”。

ALTER TABLE trajectory_exchanges ADD COLUMN IF NOT EXISTS spawn_calls JSONB;
ALTER TABLE trajectory_exchanges ADD COLUMN IF NOT EXISTS parent_call_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_spawn ON trajectory_exchanges (user_id, started_at) WHERE spawn_calls IS NOT NULL;

ALTER TABLE trajectory_sessions ADD COLUMN IF NOT EXISTS parent_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE trajectory_sessions ADD COLUMN IF NOT EXISTS parent_exchange_id TEXT NOT NULL DEFAULT '';
ALTER TABLE trajectory_sessions ADD COLUMN IF NOT EXISTS parent_call_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_trajectory_sessions_parent ON trajectory_sessions (parent_session_id) WHERE parent_session_id <> '';

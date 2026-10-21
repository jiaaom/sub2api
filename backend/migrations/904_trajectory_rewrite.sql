-- rewrite 边（派生索引，可重建）：请求去掉最后一条 user 消息后接上响应的链尾哈希，按等级各一列。
-- 新请求以它为前缀 = 父请求末尾的临时消息被替换（OpenClaw 每轮的运行时上下文）。

ALTER TABLE trajectory_exchanges ADD COLUMN IF NOT EXISTS trim_exact TEXT NOT NULL DEFAULT '';
ALTER TABLE trajectory_exchanges ADD COLUMN IF NOT EXISTS trim_nothink TEXT NOT NULL DEFAULT '';
ALTER TABLE trajectory_exchanges ADD COLUMN IF NOT EXISTS trim_loose TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_trim_exact ON trajectory_exchanges (trim_exact) WHERE trim_exact <> '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_trim_nothink ON trajectory_exchanges (trim_nothink) WHERE trim_nothink <> '';
CREATE INDEX IF NOT EXISTS idx_trajectory_exchanges_trim_loose ON trajectory_exchanges (trim_loose) WHERE trim_loose <> '';

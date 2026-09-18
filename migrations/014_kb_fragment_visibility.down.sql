-- 014 回滚：删除可见性列（公开搜索随之回到"仅租户过滤"旧语义）
DROP INDEX IF EXISTS idx_kb_frag_visibility;
ALTER TABLE knowledge_fragments DROP COLUMN IF EXISTS visibility;

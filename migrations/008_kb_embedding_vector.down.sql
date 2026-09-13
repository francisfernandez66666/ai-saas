BEGIN;

DROP INDEX IF EXISTS idx_kf_embedding;
ALTER TABLE knowledge_fragments DROP COLUMN IF EXISTS embedding;

COMMIT;

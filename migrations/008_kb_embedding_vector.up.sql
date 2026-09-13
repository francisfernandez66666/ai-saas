-- D3 KB 检索 pgvector 化（2026-09-13）
-- 背景：原检索使用 embedding_json + Go 内存余弦，片段增长后线性扫描成本变高。
-- 约束：保留 embedding_json 作为回滚垫；pgvector 不可用或维度不一致时运行态自动回退旧路径。
-- AutoMigrate 只补普通字段，不表达 pgvector 列与 HNSW 索引，破坏性 schema 改造落迁移。

CREATE EXTENSION IF NOT EXISTS vector;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'knowledge_fragments'
          AND column_name = 'embedding'
    ) THEN
        IF EXISTS (
            SELECT 1
            FROM pg_attribute a
            JOIN pg_class c ON c.oid = a.attrelid
            JOIN pg_type t ON t.oid = a.atttypid
            WHERE c.relname = 'knowledge_fragments'
              AND a.attname = 'embedding'
              AND a.attnum > 0
              AND t.typname = 'vector'
              AND a.atttypmod = 1536
        ) THEN
            RETURN;
        END IF;

        -- 维度或非 vector 类型不一致时重建。旧向量可由 embedding_json 回填，不丢业务文本。
        ALTER TABLE knowledge_fragments DROP COLUMN embedding;
    END IF;

    ALTER TABLE knowledge_fragments ADD COLUMN embedding vector(1536);
END
$$;

-- HNSW 近似最近邻索引。pgvector 版本过老时失败不致命：运行态会顺序扫或回退内存余弦。
DO $$
BEGIN
    BEGIN
        CREATE INDEX IF NOT EXISTS idx_kf_embedding
            ON knowledge_fragments USING hnsw (embedding vector_cosine_ops);
    EXCEPTION WHEN OTHERS THEN
        NULL;
    END;
END
$$;

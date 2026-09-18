-- 014 P2-4(2026-09-19 审计批三)：知识片段可见性列，封堵匿名端点全文枚举商家私有 KB。
-- 背景：/api/v1/knowledge/fragments/search 公开挂载（routes_public），SearchFragments 只按
-- 租户过滤不带可见性——匿名（或伪造 X-Tenant-ID）可整段拖走商家内部竞品话术全文。
-- 口径：visibility ∈ {public, private}，默认 private（fail-closed，新片段不显式设即不可见公开面）；
-- 回填：tenant_id=0 的系统预置片段（车型/品牌目录等，C 端选车页依赖）置 public。
ALTER TABLE knowledge_fragments
    ADD COLUMN IF NOT EXISTS visibility VARCHAR(10) NOT NULL DEFAULT 'private';

UPDATE knowledge_fragments SET visibility = 'public' WHERE tenant_id = 0;

CREATE INDEX IF NOT EXISTS idx_kb_frag_visibility ON knowledge_fragments (tenant_id, visibility);

-- 032（P1-2，2026-10-13）：新增请求级审计表 request_audit_logs。
--
-- 背景与必要性（见 FIXPLAN_2026-10-13.md §P1-2）：
--   原全仓审计仅靠 20 处手工埋点（auditSuperAdminAccess + 19 处业务动作 Create），
--   路由鉴权链 v1.Use(JWTAuth/TenantConsistency/OrgResolve/MustChangePasswordGuard)
--   不含任何审计中间件。后果是"审计靠记得埋点"而非强制——普通租户的常规请求
--   （查询、导出、改配额、改配置）不落审计；新增 handler 无编译期/门禁约束，漏埋零惩罚。
--
-- 为什么是独立表而非复用 tenant_audit_logs：
--   tenant_audit_logs 是管理端审计查询视图（internal/api/audit.go）的数据源，其
--   queryAuditLogs 按 action 过滤、LEFT JOIN tenant_users 取 username。若把每请求
--   高频行（method/path/status/latency）混进去，会：
--     ① 淹没业务语义审计（account_cancel / super_tenant_status 等），管理员看板变噪声；
--     ② 拖慢现有审计分页查询；
--     ③ 与 RLS 策略面（tenant_audit_logs 在 rls_coverage 清单内）纠缠。
--   两层分工：tenant_audit_logs = 业务语义层（手工埋点，保留）；
--             request_audit_logs = 传输层（全量、异步、失败容忍，本迁移新增）。
--
-- 设计要点：
--   - tenant_id / user_id 默认 0（未认证/系统请求可落 0，不违反 NOT NULL）；
--   - status / latency_ms 默认 0，由中间件在 c.Next() 后提取真实值写入；
--   - ip 用 VARCHAR(45) 兼容 IPv6；user_agent 截断到 512 防止超长 UA 撑爆行；
--   - 不存 query 串：避免把 URL 里的 token / 验证码等敏感参数写进审计库（安全架构优先）；
--   - 两个索引服务于典型审计检索：按租户时间线、按全局时间线（含清理旧数据游标）。
--
-- 原子性：整段单事务（DDL 事务性成立），与 030/031 同一纪律。
BEGIN;

CREATE TABLE IF NOT EXISTS request_audit_logs (
    id         BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    tenant_id  BIGINT      NOT NULL DEFAULT 0,
    user_id    BIGINT      NOT NULL DEFAULT 0,
    method     VARCHAR(10) NOT NULL,
    path       VARCHAR(512) NOT NULL,
    status     INT         NOT NULL DEFAULT 0,
    latency_ms INT         NOT NULL DEFAULT 0,
    ip         VARCHAR(45) NOT NULL DEFAULT '',
    user_agent VARCHAR(512) NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_request_audit_tenant_created
    ON request_audit_logs (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_audit_created
    ON request_audit_logs (created_at DESC);

COMMIT;

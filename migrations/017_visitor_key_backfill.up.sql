-- 017 批二 S1 收口(2026-09-22 全量审计)：访客密钥存量补签。
-- 背景：/chat/unauthorized 写入口的访客校验此前带 `customer.VisitorKey != ""` 前置短路，
-- 空密钥客户等于"无身份即可写"。实测 customers 表中 303 行里仅 1 行有密钥（其余全部为空），
-- 即读侧（/chat/history、/ws/client）已要求逐字节自证，写侧却对 99.7% 的客户敞开——
-- 攻击者仅凭 X-Tenant-ID + 任意 customer_id 即可向他人会话写入消息并触发真实 AI 出站。
-- 收口顺序必须是"先补签、后收紧"：CheckVisitorKey 对 expected 空串一律拒，
-- 若先删短路而不补签，存量客户将永久无法继续对话（连正确身份也进不来）。
-- 口径：与 model.GenerateVisitorKey（32 字节 → 64 位小写十六进制）保持同形态，
-- 用 PG13+ 内置 gen_random_uuid() 拼两枚（32+32=64 hex），不依赖 pgcrypto 扩展；
-- 列宽 size:64 恰好容纳。只补空值，已有密钥的行绝不重签（否则在线客户端手里的 VK 立刻失效）。
-- 幂等：二次执行命中 0 行。
UPDATE customers
SET visitor_key = replace(gen_random_uuid()::text, '-', '') || replace(gen_random_uuid()::text, '-', '')
WHERE visitor_key IS NULL OR visitor_key = '';

-- 补签后自检：仍为空即说明本迁移未覆盖到某类行（例如新增列默认值变更），显式抛错阻断部署。
DO $$
DECLARE leftover int;
BEGIN
    SELECT count(*) INTO leftover FROM customers WHERE visitor_key IS NULL OR visitor_key = '';
    IF leftover > 0 THEN
        RAISE EXCEPTION '017 补签后仍有 % 行 customer.visitor_key 为空，写入口收紧会导致该批客户不可用', leftover;
    END IF;
END $$;

-- 019 批六数据层治理(2026-09-23)：孤儿消息回填 conversation_id。
-- 背景：实测本机库 messages 963 行里 15 行 conversation_id=0（全是 sender_type='customer'
-- 的入站留痕行，时间跨 2026-07-20 ~ 09-18）。根因有两类：
--   ① chat_unauthorized 相似抑制分支把 First(&conv) 的 error 丢掉，定位不到活跃会话仍以零值
--      conversation_id 落库（本轮已在代码侧改为"定位失败即不抑制、不写留痕"）；
--   ② 早期测试/直插链路未过会话保障。
-- 为什么要清：D2「AI 贡献度」指标以 messages 为唯一真相源，conversation_id=0 的行既进不了
-- 会话维度统计、又虚增"客户消息数"分母——同一类口径错位本批已因此抓过一个真缺陷。
-- 回填口径：只回填"该 (tenant_id, customer_id) 至少存在一条会话"的行，取其 updated_at 最新
--   （同刻按 id 大者）那条会话；无会话可挂的客户保持 0，不硬造会话行（伪造会话会把活跃会话
--   单实例不变式 013 唯一索引撞出事务回滚风险）。残留量由 /status/detail 的
--   orphan_messages 观测位持续暴露（只降不升），不再靠人肉查库。
-- 幂等：二次执行命中 0 行（conversation_id=0 且可回填的已在第一轮全部改完）。
WITH target AS (
    SELECT m.id AS msg_id,
           (SELECT c.id
              FROM conversations c
             WHERE c.tenant_id = m.tenant_id
               AND c.customer_id = m.customer_id
             ORDER BY c.updated_at DESC, c.id DESC
             LIMIT 1) AS conv_id
      FROM messages m
     WHERE m.conversation_id = 0
)
UPDATE messages m
   SET conversation_id = t.conv_id,
       updated_at      = now()
  FROM target t
 WHERE m.id = t.msg_id
   AND t.conv_id IS NOT NULL;

-- 自检：不做阻断式断言——"无会话可挂的孤儿行"是设计内残留（客户被删/直插测试数据），
-- 抛错会让存量库部署卡死。这里只打 NOTICE 供部署日志核对，真实治理靠观测位盯住趋势。
DO $$
DECLARE leftover int;
BEGIN
    SELECT count(*) INTO leftover FROM messages WHERE conversation_id = 0;
    IF leftover > 0 THEN
        RAISE NOTICE '019 回填后仍有 % 行 messages.conversation_id=0（无可挂会话，属设计内残留，见 /status/detail orphan_messages）', leftover;
    ELSE
        RAISE NOTICE '019 回填完成：messages.conversation_id=0 已清零';
    END IF;
END $$;

-- 配套索引：孤儿行计数从"每次健康探测扫全表"降为索引内计数。
-- 为什么必须建：/status/detail 与告警 tick 会周期性数 conversation_id=0 的行，
-- messages 是全站最大的表（本项目已为它做过归档表），无索引即每次全表顺序扫描；
-- 部分索引只覆盖 conversation_id=0 的行，正常态接近 0 行，体积与维护成本可忽略。
-- IF NOT EXISTS 保证重放无害（down 不删：删索引会让观测位退回全表扫描，比留一个空索引更糟）。
CREATE INDEX IF NOT EXISTS idx_messages_orphan_no_conv ON messages (id) WHERE conversation_id = 0;

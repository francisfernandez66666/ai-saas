#!/bin/bash
# ============================================================
# OneID 存量重复客户报告（D2 配套，2026-09-16B，见 AUDIT_UAT_VERIFY_2026-09-16B）
# 只读：列同租户同手机号多有效客户组（历史非事务合并/留资丢失时代遗留）。
# 不做自动合并——涉及顾问归属/阶段并集等业务判断，人工确认后走线上合并路径或手删。
# 用法: ./tools/oneid_dup_report.sh            （默认库）
#       TEST_DB_URL=postgresql://... ./tools/oneid_dup_report.sh
# ============================================================
set -e
DBURL="${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}"
psql "$DBURL" -c "
SELECT tenant_id, phone, COUNT(*) AS dup_cnt,
       STRING_AGG(id::text, ',' ORDER BY id) AS customer_ids,
       STRING_AGG(journey_stage, ',' ORDER BY id) AS stages
FROM customers
WHERE status = 1 AND phone IS NOT NULL AND phone <> ''
GROUP BY tenant_id, phone
HAVING COUNT(*) > 1
ORDER BY tenant_id, dup_cnt DESC;"
psql "$DBURL" -tAc "
SELECT '共 ' || COUNT(*) || ' 组同号重复客户（等待人工合并处置）'
FROM (SELECT 1 FROM customers WHERE status = 1 AND phone IS NOT NULL AND phone <> ''
      GROUP BY tenant_id, phone HAVING COUNT(*) > 1) d;"

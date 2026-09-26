#!/bin/bash
# 存量凭据清洗（FIX-2，2026-09-26）：把修复前已经写进数据列的密钥明文替成 ***。
#
# 为什么要单独一个脚本而不是"改了代码就完事"：
# 通道取 token 失败时，Go 的 *url.Error 会把完整 URL（含 corpsecret / access_token）
# 原样落进错误列。代码侧这次已在**产生处**脱敏，但**存量行不会自己变干净**，而备份、
# pg_dump、任何直连查询读到的仍是明文。死信接口这侧另加了"读时兜底"脱敏，两道都在才算收口。
#
# 安全：默认 dry-run 只报命中行数（样例已在库内先打码再打印，终端不会落一份明文）；
# --apply 才真改，且改完**复扫残留必须为 0**，否则判 FAIL 退出 1。
# 「只会打日志并返回成功的守卫等于没有守卫」——本仓刚因备份脚本这条栽过一次。
#
# 正则口径与 Go 侧 pii.RedactSecretURL 逐字对齐（参数名单一致、值终止集一致、
# 只替参数值、host/path/其它参数保留），两侧不一致会导致"代码说干净了、库说没有"。
#
# 用法: ./tools/redact_stored_secrets.sh [--apply]
set -euo pipefail
DBURL="${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm}"
APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

SQL_FILE=$(mktemp "${TMPDIR:-/tmp}/redact_stored_secrets.XXXXXX.sql")
trap 'rm -f "$SQL_FILE"' EXIT

# ------------------------------------------------------------
# --selftest：先证明"这条正则在 PG 里真的认得出密钥"，再谈"零命中＝干净"。
#
# 为什么必须有这一段：本脚本默认输出"零命中，存量已干净"是一句**没有任何证据的漂亮话**——
# 正则少写一个转义、值终止集少算一种引号，它就会永远数出 0，而读的人会当成清洗完成。
# 本仓这类"守卫只会打日志并返回成功"的形态已经栽过两次（备份脚本、CTE 落库前置自检），
# 所以这里用四条固定样本双向自证：含密钥的两条必须判"泄露"、已打码与空值两条必须判"干净"。
# 一侧不符即 FAIL 退出 1，且后续 dry-run/apply 都不允许在自测红的情况下被采信。
# ------------------------------------------------------------
if [ "${1:-}" = "--selftest" ]; then
  # 注意：heredoc 不放进 $( ) —— macOS 自带 bash 3.2 在命令替换里解析内嵌 heredoc
  # 会错乱（实踩：把 SQL 正文当 shell 语句找引号配对）。先落临时文件再 -Atf 执行。
  ST_FILE=$(mktemp "${TMPDIR:-/tmp}/redact_selftest.XXXXXX.sql")
  cat >"$ST_FILE" <<'STSQL'
WITH re(re, rp) AS (
  SELECT $re$([?&;](?:corpsecret|appsecret|secret|access_token|jsapi_ticket|password|passwd|api_key|apikey)=)[^&;[:space:]"'\}\]]+$re$,
         $rp$\1***$rp$
), samples(name, kind, s) AS (
  VALUES
    ('企微 url.Error 形态',                 'leak',
     'Get "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=ww1&corpsecret=PLAINTEXT_ABC": dial tcp'),
    ('公众号 secret 在中间位',              'leak',
     'Get "https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=wx1&secret=PLAINTEXT_X&x=1": EOF'),
    ('已打码（幂等，不得再判泄露）',        'clean', 'Get "https://h/gettoken?corpsecret=***": x'),
    ('空值不算泄露',                        'clean', 'Get "https://h/gettoken?corpsecret=": x'),
    ('无凭据参数的普通错误',                'clean', '通道不存在 id=3'),
    ('大小写混写参数名',                    'leak',  'http://h/p?CorpSecret=Abc&x=1')
)
SELECT name, kind,
       (regexp_replace(s, re, rp, 'gi') <> s) AS detected,
       regexp_replace(s, re, rp, 'gi')        AS redacted
FROM samples, re
ORDER BY kind DESC, name;
STSQL
  ST=$(psql "$DBURL" -v ON_ERROR_STOP=1 -Atf "$ST_FILE")
  rm -f "$ST_FILE"
  echo "$ST"
  # 正向：标 leak 的必须 detected=t；反向：标 clean 的必须 detected=f
  BAD_LEAK=$(printf '%s\n' "$ST" | awk -F'|' '$2=="leak" && $3!="t"' | grep -c . || true)
  BAD_CLEAN=$(printf '%s\n' "$ST" | awk -F'|' '$2=="clean" && $3!="f"' | grep -c . || true)
  if [ "$BAD_LEAK" -ne 0 ] || [ "$BAD_CLEAN" -ne 0 ]; then
    echo "SELFTEST FAIL: 漏判 $BAD_LEAK 条、误判 $BAD_CLEAN 条 —— 正则与 Go 侧口径已漂移，" \
         "此时的\"零命中\"不可信，禁止采信 dry-run/apply 结果" >&2
    exit 1
  fi
  # 打码结果里不得残留任何明文密钥样本串
  if printf '%s\n' "$ST" | grep -q 'PLAINTEXT_\|Abc'; then
    echo "SELFTEST FAIL: 脱敏后仍见明文" >&2
    exit 1
  fi
  echo "SELFTEST PASS: PG 侧正则与 Go 侧 pii.RedactSecretURL 判定一致（6 条样本双向自证）"
  exit 0
fi

# 注意：整段用带引号的 heredoc（不做 shell 展开），SQL 里的 $$/$re$ 定界符与反斜杠
# 原样进库，避免历年这类脚本最常见的"shell 把 \\1 吃掉一层"的静默失配。
cat >"$SQL_FILE" <<'SQL'
SET scrub.apply = :'apply';

CREATE TEMP TABLE scrub_report(phase text, tbl text, col text, n bigint);

DO $do$
DECLARE
  -- 凭据参数：只替**值**，值终止集含 & ; 空白 引号 反斜杠 右括号（url.Error 用双引号包 URL）
  re    text := $re$([?&;](?:corpsecret|appsecret|secret|access_token|jsapi_ticket|password|passwd|api_key|apikey)=)[^&;[:space:]"'\}\]]+$re$;
  rp    text := $rp$\1***$rp$;
  apply boolean := current_setting('scrub.apply', true) = '1';
  t     record;
  c     bigint;
BEGIN
  FOR t IN SELECT * FROM (VALUES
        ('channel_outbound',     'error'),
        ('outreach_tasks',       'error'),
        ('deletion_requests',    'error'),
        ('webhook_deliveries',   'last_error'),
        ('chat_archive_records', 'decrypt_error')
      ) v(tbl, col) LOOP
    -- 命中判定＝"替一遍之后不一样"（等价于 Go 侧 ContainsSecretParam：已 *** 与空值都不算泄露）
    EXECUTE format(
      'SELECT count(*) FROM %I WHERE %I ~* $1 AND regexp_replace(%I, $1, $2, ''gi'') <> %I',
      t.tbl, t.col, t.col, t.col)
      INTO c USING re, rp;
    INSERT INTO scrub_report VALUES ('before', t.tbl, t.col, c);
    IF apply AND c > 0 THEN
      EXECUTE format(
        'UPDATE %I SET %I = regexp_replace(%I, $1, $2, ''gi'') WHERE %I ~* $1 AND regexp_replace(%I, $1, $2, ''gi'') <> %I',
        t.tbl, t.col, t.col, t.col, t.col, t.col)
        USING re, rp;
    END IF;
  END LOOP;

  IF apply THEN
    -- 复扫残留：清洗后逐列再数一遍，非 0 由外层脚本据此判 FAIL（守卫要能红才是守卫）
    FOR t IN SELECT * FROM (VALUES
          ('channel_outbound',     'error'),
          ('outreach_tasks',       'error'),
          ('deletion_requests',    'error'),
          ('webhook_deliveries',   'last_error'),
          ('chat_archive_records', 'decrypt_error')
        ) v(tbl, col) LOOP
      EXECUTE format(
        'SELECT count(*) FROM %I.%I WHERE %I ~* $1 AND regexp_replace(%I, $1, $2, ''gi'') <> %I',
        t.tbl, t.col, t.col, t.col, t.col)
        INTO c USING re, rp;
      INSERT INTO scrub_report VALUES ('after', t.tbl, t.col, c);
    END LOOP;
  END IF;
END
$do$;

SELECT phase, tbl, col, n FROM scrub_report ORDER BY phase, tbl;
SQL

# apply 与 dry-run 都走同一段 SQL，靠 psql 变量控制（-v 传参，不在 shell 里拼正则）
OUT=$(psql "$DBURL" -v ON_ERROR_STOP=1 -v apply="$APPLY" -Atf "$SQL_FILE" 2>&1)
echo "$OUT"

BEFORE_SUM=$(printf '%s\n' "$OUT" | awk -F'|' '$1=="before" {s+=$4} END {print s+0}')
echo "含凭据明文的存量行合计：$BEFORE_SUM"

if [ "$APPLY" = "0" ]; then
  if [ "$BEFORE_SUM" -gt 0 ]; then
    echo "DRY-RUN：确认无误后跑 $0 --apply"
  else
    echo "DRY-RUN：零命中，存量已干净"
  fi
  exit 0
fi

AFTER_SUM=$(printf '%s\n' "$OUT" | awk -F'|' '$1=="after" {s+=$4} END {print s+0}')
if [ "$AFTER_SUM" -ne 0 ]; then
  echo "FAIL: 清洗后仍残留 $AFTER_SUM 行（正则口径与 Go 侧不一致？请人工核对）" >&2
  exit 1
fi
echo "APPLIED: 存量凭据已清洗，复扫残留 0 行"

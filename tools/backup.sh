#!/bin/bash
# ============================================================
# AI-SCRM PostgreSQL 备份脚本（生产部署必挂 cron）
#
# 安装（每日 03:00 备份，保留 7 天）：
#   chmod +x tools/backup.sh
#   (crontab -l 2>/dev/null; echo "0 3 * * * $(pwd)/tools/backup.sh >> /var/log/ai-scrm-backup.log 2>&1") | crontab -
#
# 恢复演练（建议每月做一次）：
#   createdb ai_scrm_restore_test
#   pg_restore -h localhost -U <备份角色，须能旁路 RLS> -d ai_scrm_restore_test --no-owner <备份文件>
#   psql -h localhost -U <备份角色> -d ai_scrm_restore_test -c "SELECT COUNT(*) FROM customers;"
#   ⚠ 别用应用角色跑 pg_restore：它建出来的表归它、策略也 FORCE 在它身上，而 pg_restore
#     和 pg_dump 一样会请求 row_security=off，恢复会在 COPY 数据那步报错（同 rlspreflight）。
#
# 环境变量：BACKUP_DIR / PGHOST / PGPORT / PGUSER / PGPASSWORD / PGDATABASE
#           KEEP_DAYS(默认7) / BACKUP_REMOTE_CMD(异地推送,可选)
#           BACKUP_NOTIFY_WEBHOOK(企微机器人 webhook，可选；配置后成败均通知)
#           BACKUP_DB_USER / BACKUP_DB_PASSWORD(可选)：**备份专用角色**。
#           默认沿用 PGUSER（应用角色）。RLS 通电（迁移 030）后，非超级用户且无 BYPASSRLS 的
#           角色跑 pg_dump 会在 COPY 上直接报错（pg_dump 会 SET row_security = off），
#           所以生产上这里应指向编排的超级用户；脚本会在开跑前把这件事判清楚（见 rlspreflight）。
#           ⚠ 别图省事给应用角色加 BYPASSRLS——那等于把刚通电的第二道闸拆掉。
#
# 异地推送（批次三·防单点，2026-08-23）：
#   配置 BACKUP_REMOTE_CMD 后，本地备份+完整性校验通过即自动推远端；
#   {{FILE}} 占位符会被替换为本次备份文件路径。示例：
#     BACKUP_REMOTE_CMD="rclone copy {{FILE}} remote:ai-scrm-backups"
#     BACKUP_REMOTE_CMD="ossutil cp -f {{FILE}} oss://mybucket/scrm-backup/"
#     BACKUP_REMOTE_CMD="scp {{FILE}} backup@nas:/volume1/scrm-backup/"
#   推送失败仅告警不中断（本地副本仍在）；远端保留策略由远端自行管理
# ============================================================

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-$(pwd)/backups}"
KEEP_DAYS="${KEEP_DAYS:-7}"
STAMP=$(date +%Y%m%d_%H%M%S)
FILE="$BACKUP_DIR/ai_scrm_$STAMP.dump"

# ------------------------------------------------------------
# 凭据来源：环境变量优先，缺则回落 .env（2026-09-28 FIX-A 后续）
#
# 为什么要补这一层：cron 起的备份进程**没有交互式 shell 的环境**，而本仓所有 DB 凭据的
# 真源是 `.env`（应用用 godotenv 读它）。旧 backup.sh 只吃 PGHOST/PGUSER/PGDATABASE 环境变量，
# 没设就用写死的 localhost/ai_scrm/ai_scrm 默认值——于是"备份了另一个（甚至空的）库"
# 这件事在退出码上是看不出来的：连不上会响，但**连上了一台同名的旧库不会响**。
# 现在按 .env 的 DB_* 取真值，环境变量仍可覆盖（CI/手工临时换库照旧）。
# ⚠ 只回落到本机的库连接参数，不在日志里打印任何口令；.env 本身不入库（.gitignore）。
# ------------------------------------------------------------
ENV_FILE="${BACKUP_ENV_FILE:-$(pwd)/.env}"
# env_val <键> → .env 里该键的值（取最后一行，去引号与空格）；文件不存在/键缺失都返回空串
env_val() {
  [ -f "$ENV_FILE" ] || return 0
  grep -E "^[[:space:]]*${1}[[:space:]]*=" "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d '"' | tr -d ' ' || true
}
if [ -z "${PGHOST:-}" ]; then export PGHOST="$(env_val DB_HOST)"; fi
if [ -z "${PGPORT:-}" ]; then export PGPORT="$(env_val DB_PORT)"; fi
if [ -z "${PGUSER:-}" ]; then export PGUSER="$(env_val DB_USER)"; fi
if [ -z "${PGPASSWORD:-}" ]; then export PGPASSWORD="$(env_val DB_PASSWORD)"; fi
if [ -z "${PGDATABASE:-}" ]; then export PGDATABASE="$(env_val DB_NAME)"; fi

export PGHOST="${PGHOST:-localhost}"
export PGPORT="${PGPORT:-5432}"
export PGUSER="${PGUSER:-ai_scrm}"
export PGDATABASE="${PGDATABASE:-ai_scrm}"

# ------------------------------------------------------------
# 备份角色与"RLS 通电后 pg_dump 必炸"（FIX-A 后续，2026-09-28）
#
# 现象：迁移 030 给租户表补上 `ENABLE ROW LEVEL SECURITY` 之后，本机 `tools/backup.sh`
#       当场红——`pg_dump: ERROR: query would be affected by row-level security policy
#       for table "agreement_signatures"`。这不是备份脚本坏了，是**第二道闸真的通电了**，
#       而旧脚本一直用应用连接角色（表属主、非超级用户）跑 dump。
# 机理：pg_dump 在会话里执行 `SET row_security = off`（它要的是"属主视角的全量数据"）。
#       PG 的规则是：请求关掉 RLS 的角色若**正受某张表的策略约束**就直接报错，而不是
#       悄悄给你全量——`FORCE ROW LEVEL SECURITY` 让表属主也受约束，于是这条错误必然出现。
#       注意策略本身是休眠式的（`app.current_tenant` 未设置时恒真放行），所以
#       **"数据其实读得到"与"pg_dump 报错"同时为真**：千万别因此去删 FORCE 或 DISABLE
#       策略，那是把刚接上的第二道闸再拆一遍来迁就一个工具的写法。
# 修法：dump 用一个能旁路 RLS 的角色（超级用户或 BYPASSRLS），与应用角色分开。
#       生产 docker-compose 里 `POSTGRES_USER` 就是超级用户；本机 Homebrew 是同名 OS 用户。
#       红线：**不要给应用连接角色（PGUSER）加 BYPASSRLS**——应用与该角色同体，
#       加了等于所有连接无条件旁路，FIX-A 白做。
# ------------------------------------------------------------
if [ -z "${BACKUP_DB_USER:-}" ]; then BACKUP_DB_USER="$(env_val BACKUP_DB_USER)"; fi
if [ -z "${BACKUP_DB_PASSWORD:-}" ]; then BACKUP_DB_PASSWORD="$(env_val BACKUP_DB_PASSWORD)"; fi
DUMP_PGUSER="${BACKUP_DB_USER:-${PGUSER:-ai_scrm}}"
export PGUSER="$DUMP_PGUSER"
# 备份角色可以有自己的口令；没单独给就沿用当前 PGPASSWORD（本机 trust 认证时两者都不需要）
# ⚠ set -e 下 `[ 条件 ] && 命令` 这种"末句为假即退出码 1"的写法会把脚本直接杀掉，
#   所以这里必须写成 if 形式（本仓同类坑已踩过三次）。
if [ -n "${BACKUP_DB_PASSWORD:-}" ]; then export PGPASSWORD="$BACKUP_DB_PASSWORD"; fi

# rlspreflight：在真正开跑前把"这个角色能不能 dump 出全量"判清楚。
# 判不出来（连不上库/权限不足）时只 WARN 不拦——后面 pg_dump 自己会响亮地失败，
# 把探针失败变成"永久不备份"是更坏的结果（本仓的教训是守卫不能静默放行，不是守卫要一律判红）。
DUMP_ROLE_FLAGS=$(psql -tAc "SELECT COALESCE(bool_or(rolsuper OR rolbypassrls), false) FROM pg_roles WHERE rolname = current_user" 2>/dev/null || echo "")
RLS_ENABLED_TABLES=$(psql -tAc "SELECT count(DISTINCT c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace JOIN pg_policies p ON p.tablename = c.relname WHERE n.nspname = current_schema() AND p.policyname = 'tenant_isolation' AND c.relrowsecurity" 2>/dev/null || echo "")
# 数字先剥掉非数字字符：读数取空时 `[ '' -gt 0 ]` 在 set -e 下会炸成一句看不懂的 shell 报错，
# 而不是走到我们想要的那条"探测失败只 WARN"分支。
RLS_ENABLED_TABLES=$(printf '%s' "${RLS_ENABLED_TABLES:-}" | tr -dc '0-9')
if [ -z "$DUMP_ROLE_FLAGS" ] || [ -z "$RLS_ENABLED_TABLES" ]; then
  echo "[$(date '+%F %T')] [WARN] 备份角色/RLS 现状探测失败（psql 不可达？），仍继续尝试 dump，失败会照实报错" >&2
elif [ "$DUMP_ROLE_FLAGS" != "t" ] && [ "$RLS_ENABLED_TABLES" -gt 0 ]; then
  echo "[$(date '+%F %T')] [ERROR] 备份角色 $DUMP_PGUSER 既不是超级用户也没有 BYPASSRLS，而当前库里有 $RLS_ENABLED_TABLES 张租户表已 ENABLE ROW LEVEL SECURITY。pg_dump 会在会话里 SET row_security = off，PG 对受策略约束的角色请求关 RLS 直接报错，dump 必然失败。" >&2
  echo "  修法：给备份单独指定一个可旁路 RLS 的角色——.env 里设 BACKUP_DB_USER=<超级用户或 BYPASSRLS 角色>（需要口令时再设 BACKUP_DB_PASSWORD）。生产 docker-compose 用 POSTGRES_USER 即可。" >&2
  echo "  别这么修：不要给应用连接角色加 BYPASSRLS（等于拆掉刚通电的第二道闸），也不要 DISABLE/NO FORCE 那些表的策略（第二道闸回到空转）。" >&2
  exit 1
fi

mkdir -p "$BACKUP_DIR"

# L8修复(2026-08-27)：互斥锁，防止 cron 重叠并发 dump 互相冲掉（同实例串行）
#
# ⚠ 2026-09-26 修：原来这里只有一句 `flock -n 9`，而 **macOS/BSD 默认没有 flock**（它是
# util-linux 的东西）。命令不存在时 flock 返回 127，正好落进"没抢到锁"分支——脚本于是
# 每次都打一行 WARN「已有备份进程在运行」再 `exit 0`。cron 看到的是**退出码 0**，
# 于是"每日备份"在这类机器上一次都没真的跑过，而且看起来一切正常。
# 备份是数据丢失时唯一的兜底，"没跑"必须比"跑了"更响，所以改成两级：
#   ① 有 flock 就照旧用（Linux 生产机口径不变）；
#   ② 没有就用 POSIX 的 mkdir 原子锁 + 锁内 PID 活性判定（进程死了留下的陈旧锁要能接管，
#      否则一次崩溃会让此后所有备份永久跳过——那是同一个缺陷换个形态）。
# 两种实现都拿不到锁（真有并发在跑）仍按原口径跳过并 exit 0。
LOCK_FILE="$BACKUP_DIR/.backup.lock"
LOCK_DIR="$BACKUP_DIR/.backup.lock.d"
acquire_lock() {
  if command -v flock >/dev/null 2>&1; then
    exec 9>"$LOCK_FILE"
    flock -n 9
    return $?
  fi
  # 无 flock：mkdir 原子锁。抢到就把自己的 PID 写进去；抢不到先看持有者是否还活着。
  if mkdir "$LOCK_DIR" 2>/dev/null; then
    echo $$ > "$LOCK_DIR/pid"
    # 退出时释放（含 ERR/INT：trap 在函数外统一挂，见下）
    return 0
  fi
  holder=$(cat "$LOCK_DIR/pid" 2>/dev/null || true)
  if [ -n "${holder:-}" ] && kill -0 "$holder" 2>/dev/null; then
    return 1 # 真有人在跑
  fi
  echo "[$(date '+%F %T')] [WARN] 发现陈旧锁 ${LOCK_DIR}（持有者 ${holder:-未知} 已不在），接管后继续" >&2
  rm -rf "$LOCK_DIR"
  mkdir "$LOCK_DIR" 2>/dev/null || return 1
  echo $$ > "$LOCK_DIR/pid"
  return 0
}
release_lock() {
  if [ -d "$LOCK_DIR" ]; then rm -rf "$LOCK_DIR"; fi
}
if ! acquire_lock; then
  echo "[$(date '+%F %T')] [WARN] 已有备份进程在运行（锁 $LOCK_FILE / $LOCK_DIR 被占用），本次跳过" >&2
  exit 0
fi
# 锁必须随任何退出路径释放；漏放会让后续每次备份都走进"陈旧锁接管"，虽然不至于永久卡住，
# 但每一次都要靠 PID 活性判定救场，等于把互斥降级成碰运气。
trap release_lock EXIT

# 企微通知（可选）：BACKUP_NOTIFY_WEBHOOK 配置后，成败均推送
notify() {
  local level="$1"; local msg="$2"
  [ -z "${BACKUP_NOTIFY_WEBHOOK:-}" ] && return 0
  local content
  content=$(printf '**AI-SCRM 备份%s**\n时间: %s\n实例: %s\n%s' "$level" "$(date '+%F %T')" "${PGDATABASE}" "$msg")
  curl -s -m 5 -H "Content-Type: application/json" -X POST "$BACKUP_NOTIFY_WEBHOOK" \
    -d "{\"msgtype\":\"markdown\",\"markdown\":{\"content\":$(printf '%s' "$content" | python3 -c 'import sys,json;print(json.dumps(sys.stdin.read()))')}}" >/dev/null 2>&1 || true
}

# 失败即通知并退出（set -e 下任何一步失败触发）
# ⚠ 半截归档必须删掉：pg_dump 是流式写文件的，报错时 FILE 已经存在且非零字节。留着它，
#   第二天 `pg_restore --list` 也可能解得开（尾部缺数据不一定坏头），于是运维自检读到的
#   是"有当天备份、可解析"，而真实恢复会在中途缺表——这是"备份从未真跑"的同族缺陷，
#   只是换了形态。校验通过（VALIDATED=1）之后才允许文件留在盘上。
trap 'notify "失败" "备份中断，请检查日志 $(pwd)/backups" >&2; echo "[$(date "+%F %T")] [ERROR] 备份失败" >&2; if [ "${VALIDATED:-0}" != "1" ] && [ -f "$FILE" ]; then rm -f "$FILE"; echo "[$(date "+%F %T")] 已删除未通过校验的半成品: $FILE" >&2; fi' ERR

echo "[$(date '+%F %T')] 开始备份 $PGDATABASE → $FILE"
pg_dump --format=custom --compress=6 --no-owner --file="$FILE"

SIZE=$(du -h "$FILE" | cut -f1)
echo "[$(date '+%F %T')] 备份完成: $FILE ($SIZE)"

# 完整性抽检：列出归档内容头部（损坏会在此报错）
pg_restore --list "$FILE" > /dev/null && VALIDATED=1 && echo "[$(date '+%F %T')] 完整性校验通过"

# 异地推送钩子（批次三）：BACKUP_REMOTE_CMD 配置即启用，{{FILE}} 替换为本次备份路径
if [ -n "${BACKUP_REMOTE_CMD:-}" ]; then
  REMOTE_CMD="${BACKUP_REMOTE_CMD//\{\{FILE\}\}/$FILE}"
  echo "[$(date '+%F %T')] 异地推送开始: $REMOTE_CMD"
  if bash -c "$REMOTE_CMD"; then
    echo "[$(date '+%F %T')] 异地推送成功"
  else
    echo "[$(date '+%F %T')] [WARN] 异地推送失败(本地副本完好，请检查远端配置/网络)" >&2
  fi
else
  echo "[$(date '+%F %T')] 未配置 BACKUP_REMOTE_CMD，跳过异地推送"
fi

# 保留策略：删除超期备份
find "$BACKUP_DIR" -name "ai_scrm_*.dump" -mtime +"$KEEP_DAYS" -print -delete |
  while read -r f; do echo "[$(date '+%F %T')] 清理过期备份: $f"; done

echo "[$(date '+%F %T')] 全部完成"
notify "成功" "备份完成: $FILE ($SIZE)"

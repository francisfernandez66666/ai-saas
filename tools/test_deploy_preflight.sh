#!/bin/bash
# ============================================================
# deploy_preflight 的判据自证（FIX-7 批，2026-09-27）
#
# 为什么单独一个脚本：preflight 本身是"只读守卫"，而本仓这批的教训原话是
# **「只会打日志并返回成功的守卫等于没有守卫」**。把"副本>1 且 Redis 未开"从 warn 改成 bad
# 之后，如果不配反向用例，就没有任何东西证明这条 bad 真的会响——它完全可能因为
# "只在 .env 显式写了 APP_REPLICAS 才判"这类取值缺陷而永远不触发，改动的语义只剩注释。
#
# 本脚本喂四组合成 (.env, compose) 给真 preflight，逐组断言"该红的红、该绿的不红"：
#   ① APP_REPLICAS=2 + Redis 未开 → FAIL「副本数=2」（旧实现能抓到这一组）
#   ② 只把 replicas: 3 写进编排、.env 不提 APP_REPLICAS → 仍须 FAIL（旧实现看不见，本批补的那半）
#   ③ 编排起了 redis 服务但应用不连 → 必须 FAIL（CI 三个 job 恰好就是这个形态）
#   ④ 单实例且编排显式 REDIS_ENABLED:"true" → 三条腿一条都不许红（防"把闸改成恒红"这种假严格）
#   ⑤~⑦（FIX-N 2026-09-28 .env 丢失处置批）凭据回填面：模板占位/出厂默认/半配/键名写错
#      各须 FAIL，且**回填后红必须消失**（防"把闸改成恒红"）；⑧ 钉模板缺部署面键；
#      ⑨ 是判据同源锁——
#      shell 侧的占位清单与出厂默认表必须与 config/envaudit.go、.env.example 逐字相等，
#      分叉的后果不是少报一条，而是"两边都认为自己覆盖了"，凭据没回填却双绿。
#
# 用法：bash tools/test_deploy_preflight.sh    （纯文件判据，不启容器、不连库、退出码 0/1）
# ============================================================
set -u
cd "$(dirname "$0")/.."
ROOT=$(pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/preflight_selftest.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

PASS=0
FAIL=0
ok() { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# 合成一份"其它段落全绿"的 .env，让退出码只反映多实例这一段判据
mk_env() { # $1=目标文件 $2=APP_REPLICAS 值（空=不写这行） $3=REDIS_ENABLED 值（空=不写）
  {
    echo "GIN_MODE=release"
    echo "APP_ENV=prod"
    echo "ALLOW_MOCK_PAY=false"
    echo "JWT_SECRET=selftest-only-0123456789abcdef0123456789abcdef"
    echo "DB_PASSWORD=selftest_strong_pw"
    echo "DB_HOST=db"
    echo "DB_NAME=ai_scrm"
    echo "DB_USER=ai_scrm"
    echo "AI_MOCK_MODE=false"
    echo "GLM_API_KEY=selftest-key"
    [ -n "${2:-}" ] && echo "APP_REPLICAS=$2"
    [ -n "${3:-}" ] && echo "REDIS_ENABLED=$3"
  } > "$1"
}

# 合成编排：三条 §5 判据（env_file / pgvector / APP_PUBLISH_PORT）都满足，
# 并把 app 服务块写成参数：$2=是否显式 REDIS_ENABLED:"true"，$3=replicas 值（空=不写），
# $4=是否声明 redis 服务
mk_compose() {
  local f=$1 redis_line="" rep="" redissvc=""
  [ "$2" = "yes" ] && redis_line='      REDIS_ENABLED: "true"'
  [ -n "$3" ] && rep="      replicas: $3"
  [ "$4" = "yes" ] && redissvc=$'  redis:\n    image: redis:7-alpine'
  {
    echo "services:"
    echo "  postgres:"
    echo "    image: pgvector/pgvector:0.8.6-pg16"
    [ -n "$redissvc" ] && printf '%s\n' "$redissvc"
    echo "  app:"
    echo "    env_file: .env"
    echo "    environment:"
    echo "      SERVER_PORT: \"8080\""
    [ -n "$redis_line" ] && echo "$redis_line"
    echo "    ports:"
    echo "      - \"\${APP_PUBLISH_PORT:-8080}:8080\""
    echo "    deploy:"
    echo "      resources:"
    echo "        limits:"
    echo "          cpus: \"2\""
    [ -n "$rep" ] && echo "$rep"
  } > "$f"
}

# run_preflight <env文件> <compose文件> → stdout 是 preflight 全文，rc 是退出码
run_preflight() {
  COMPOSE_FILE="$2" bash tools/deploy_preflight.sh "$1" 2>&1
}

expect_fail_with() { # $1=描述 $2=必须命中的子串 $3=输出 $4=退出码
  if [ "$4" != "0" ] && printf '%s' "$3" | grep -q "$2"; then
    ok "$1"
  else
    bad "${1}（期望退出码非 0 且命中「${2}」；实际 rc=${4}）"
    printf '%s\n' "$3" | sed 's/^/        /' | tail -12
  fi
}

expect_absent() { # $1=描述 $2=不许出现的子串 $3=输出
  if printf '%s' "$3" | grep -q "$2"; then
    bad "${1}（不该出现却出现了「${2}」）"
  else
    ok "$1"
  fi
}

echo "==== deploy_preflight 判据自证 ===="

# ① APP_REPLICAS=2 且 Redis 未开
E1="$WORK/c1.env"; C1="$WORK/c1.yml"
mk_env "$E1" 2 ""
mk_compose "$C1" no "" no
O1=$(run_preflight "$E1" "$C1"); R1=$?
expect_fail_with "① APP_REPLICAS=2 且未开 Redis → FAIL" "副本数=2" "$O1" "$R1"

# ② 副本只写在编排里（.env 完全不提 APP_REPLICAS）——旧实现看不见这一路
E2="$WORK/c2.env"; C2="$WORK/c2.yml"
mk_env "$E2" "" ""
mk_compose "$C2" no 3 no
O2=$(run_preflight "$E2" "$C2"); R2=$?
expect_fail_with "② 只在编排写 replicas: 3 → FAIL（本批补的可见性）" "副本数=3" "$O2" "$R2"

# ③ 编排起了 redis 服务而应用不连（CI 三个 job 的原形态：起了没人连）
E3="$WORK/c3.env"; C3="$WORK/c3.yml"
mk_env "$E3" "" "false"
mk_compose "$C3" no "" yes
O3=$(run_preflight "$E3" "$C3"); R3=$?
expect_fail_with "③ 声明 redis 服务但应用不连 → FAIL" "起了没人连" "$O3" "$R3"

# ④ 正向：单实例 + 编排显式开 Redis → 三条腿一条都不许红
#    （这一组是防"把闸改成恒红"的假严格：任何一条腿无条件响，①~③ 都会绿得毫无意义）
E4="$WORK/c4.env"; C4="$WORK/c4.yml"
mk_env "$E4" "1" ""
mk_compose "$C4" yes "" yes
O4=$(run_preflight "$E4" "$C4"); R4=$?
expect_absent "④ 单实例且编排开 Redis：不报「副本数」红" "副本数=" "$O4"
expect_absent "④ 同上不报「起了没人连」红" "起了没人连" "$O4"
if [ "$R4" = "0" ]; then
  ok "④ 正向组整体退出码 0（除多三段判据外没有别的 FAIL）"
else
  bad "④ 正向组整体退出码应为 0（rc=${R4}）"
  printf '%s\n' "$O4" | grep FAIL | sed 's/^/        /'
fi

# ============================================================
# ⑤~⑨ 外部凭据回填判据（FIX-N 2026-09-28 .env 丢失处置批）
#
# 这四组钉的是同一条缺陷族：旧 §2b 不存在时，preflight 判"凭据有没有配"用的是 `[ -n 值 ]`，
# 而本机 .env 从模板重建之后每个键都**非空但全是模板串**（sk-your-siliconflow-key 这种），
# 于是预检对着一个整片没回填的 .env 报"AI 供应商 Key 已配"。
# ⑨ 那条不是场景用例，是**判据同源锁**：shell 清单与 config/envaudit.go 的清单一旦分叉，
# 运行期观测位与上线预检会一个说红一个说绿，而库里/机器上的凭据其实没动过。
# ============================================================

# mk_cred_env <目标文件> [附加 KEY=VALUE 行…]：除凭据段外全绿（单实例 + 编排开 Redis 时整体应为 0）
mk_cred_env() {
  local f=$1; shift
  {
    echo "GIN_MODE=release"
    echo "APP_ENV=prod"
    echo "ALLOW_MOCK_PAY=false"
    echo "JWT_SECRET=selftest-only-0123456789abcdef0123456789abcdef"
    echo "DB_PASSWORD=selftest_strong_pw"
    echo "DB_HOST=db"
    echo "DB_NAME=ai_scrm"
    echo "DB_USER=ai_scrm"
    echo "AI_MOCK_MODE=false"
    echo "GLM_API_KEY=selftest-key"
    for kv in "$@"; do echo "$kv"; done
  } > "$f"
}
CK="$WORK/cred.yml"; mk_compose "$CK" yes "" no

# ⑤ 主力 Key 仍是模板值 + 守卫令牌仍是出厂默认 → 两条都必须红
E5="$WORK/c5.env"
mk_cred_env "$E5" "SILICONFLOW_API_KEY=sk-your-siliconflow-key" "HEALTH_TOKEN=local-dev-health-2026"
O5=$(run_preflight "$E5" "$CK"); R5=$?
expect_fail_with "⑤ 模板占位 Key 必须 FAIL（旧写法在这里给 PASS）" "SILICONFLOW_API_KEY 仍是模板占位" "$O5" "$R5"
expect_fail_with "⑤ 出厂默认 HEALTH_TOKEN 必须 FAIL（守卫等于没设）" "HEALTH_TOKEN 仍是出厂默认值" "$O5" "$R5"
# 反向对照（防"把闸改成恒红"的假严格）：同一份 .env 只把这两处改成真实形态，两条红必须消失
E5R="$WORK/c5r.env"
mk_cred_env "$E5R" "SILICONFLOW_API_KEY=sk-7f3a9b12c4d5" "HEALTH_TOKEN=selftest-health-token"
O5R=$(run_preflight "$E5R" "$CK"); R5R=$?
# 匹配串取 FAIL 文案的**独有片段**（"仍是模板占位"），不能只取"模板占位"——正向那条 PASS
# 自己写着"无模板占位/出厂默认形态"，取宽了就是断言把自己判红（首跑即踩）。
expect_absent "⑤反 回填后的占位红消失" "仍是模板占位" "$O5R"
expect_absent "⑤反 回填后的出厂默认红消失" "仍是出厂默认值" "$O5R"
if [ "$R5R" != "0" ]; then
  bad "⑤反 全回填组整体退出码应为 0（rc=${R5R}）"
  printf '%s\n' "$O5R" | grep FAIL | sed 's/^/        /'
else
  ok "⑤反 全回填组整体退出码 0（凭据段不贡献任何 FAIL）"
fi

# ⑥ 半配：声明了 URL 却没配 Key（实现会真去拨号、每请求一次失败）
E6="$WORK/c6.env"
mk_cred_env "$E6" "EMBEDDING_API_URL=https://api.siliconflow.cn/v1/embeddings"
O6=$(run_preflight "$E6" "$CK"); R6=$?
expect_fail_with "⑥ URL 已配而 Key 没配 → FAIL（半配比没配更坏）" "EMBEDDING_API_URL 已声明而 EMBEDDING_API_KEY 未回填" "$O6" "$R6"
# 反向对照 A：整条都没配＝能力未启用，不报（旧实现若把它报红，运维会被一堆"没打算用的功能"淹没）
E6A="$WORK/c6a.env"; mk_cred_env "$E6A"
O6A=$(run_preflight "$E6A" "$CK"); R6A=$?
expect_absent "⑥反A 整条未启用不报半配" "未回填" "$O6A"
# 反向对照 B：RERANK 只配 URL、Key 留空但 EMBEDDING Key 可用——这是**文档写明的合法形态**
# （internal/service/rerank.go:72-75 复用 EmbeddingKey，有单测锁）。把它报成缺口就是预检自己造红。
E6B="$WORK/c6b.env"
mk_cred_env "$E6B" "RERANK_API_URL=https://api.siliconflow.cn/v1/rerank" "EMBEDDING_API_KEY=sk-emb-selftest"
O6B=$(run_preflight "$E6B" "$CK"); R6B=$?
expect_absent "⑥反B RERANK 回落 EMBEDDING Key 属合法形态，不报红" "RERANK_API_KEY 未回填" "$O6B"
if [ "$R6B" != "0" ]; then
  bad "⑥反B 回落形态整体应判 0（rc=${R6B}）"
  printf '%s\n' "$O6B" | grep FAIL | sed 's/^/        /'
else
  ok "⑥反B 回落形态整体退出码 0"
fi

# ⑦ 写了没人读的键：SMTP_PASSWORD（代码读的是 SMTP_PASS）
E7="$WORK/c7.env"
mk_cred_env "$E7" "SMTP_PASSWORD=selftest-smtp-pw"
O7=$(run_preflight "$E7" "$CK"); R7=$?
expect_fail_with "⑦ 键名写错（SMTP_PASSWORD 无人读）必须 FAIL" "键名写错" "$O7" "$R7"
E7R="$WORK/c7r.env"
mk_cred_env "$E7R" "SMTP_PASS=selftest-smtp-pw" "SMTP_HOST=smtp.qiye.aliyun.com" "SMTP_USER=bot@example.com"
O7R=$(run_preflight "$E7R" "$CK"); R7R=$?
expect_absent "⑦反 正确键名不再报键名错配" "键名写错" "$O7R"
if [ "$R7R" != "0" ]; then
  bad "⑦反 正确键名组整体退出码应为 0（rc=${R7R}）"
  printf '%s\n' "$O7R" | grep FAIL | sed 's/^/        /'
else
  ok "⑦反 正确键名组整体退出码 0"
fi

# ⑧ 模板缺部署面键：这条锁判的是"照模板重建 .env 会静默丢掉哪几行"，
#    所以反向用例喂**剥掉那五行的合成模板**（ENV_EXAMPLE_FILE 覆写），不动仓库交付物——
#    第一版这里没有覆写，"摘掉整条对账"的变异照样全绿（守卫空转而没人发现）。
E8="$WORK/c8.env"
mk_cred_env "$E8" "SILICONFLOW_API_KEY=sk-7f3a9b12c4d5" "HEALTH_TOKEN=selftest-health-token"
STRIPPED_EX="$WORK/env.example.stripped"
grep -vE "^[[:space:]]*(APP_ENV|ALLOW_MOCK_PAY|TRUSTED_PROXIES|METRICS_TOKEN|APP_REPLICAS)=" .env.example > "$STRIPPED_EX"
O8=$(ENV_EXAMPLE_FILE="$STRIPPED_EX" COMPOSE_FILE="$CK" bash tools/deploy_preflight.sh "$E8" 2>&1); R8=$?
expect_fail_with "⑧ 模板缺部署面键 → FAIL（重建 .env 时静默丢键）" "缺部署面键" "$O8" "$R8"
# 反向对照：仓库真模板必须让这条判绿（否则"补进模板"只写在注释里，没有任何东西证明它到位了）
O8R=$(run_preflight "$E8" "$CK"); R8R=$?
expect_absent "⑧反 仓库 .env.example 不报缺键" "缺部署面键" "$O8R"
if [ "$R8R" != "0" ]; then
  bad "⑧反 真模板组整体退出码应为 0（rc=${R8R}）"
  printf '%s\n' "$O8R" | grep FAIL | sed 's/^/        /'
else
  ok "⑧反 真模板组整体退出码 0"
fi

# ⑨ 判据同源锁：shell 侧的占位清单/出厂默认表必须与 Go 侧、与 .env.example 逐字相等。
#   分叉后果不是"少报一条"，是"两边各自都认为自己覆盖了"——双绿而凭据其实没回填。
DRIFT=$(python3 - "$ROOT" <<'PY'
import re, sys
root = sys.argv[1]
go = open(root + "/config/envaudit.go", encoding="utf-8").read()
sh = open(root + "/tools/deploy_preflight.sh", encoding="utf-8").read()
ex = open(root + "/.env.example", encoding="utf-8").read()
problems = []

def go_prefixes():
    m = re.search(r"var placeholderPrefixes = \[\]string\{(.*?)\n\}", go, re.S)
    if not m:
        problems.append("placeholderPrefixes 结构变了，解析器需同步")
        return set()
    return set(re.findall(r'"([^"]+)"', m.group(1)))

def sh_prefixes():
    m = re.search(r'PLACEHOLDER_PREFIXES="([^"]+)"', sh)
    if not m:
        problems.append("shell 侧 PLACEHOLDER_PREFIXES 不见了")
        return set()
    return set(m.group(1).split())

def go_defaults():
    m = re.search(r"var templateDefaults = map\[string\]string\{(.*?)\n\}", go, re.S)
    if not m:
        problems.append("templateDefaults 结构变了，解析器需同步")
        return {}
    return dict(re.findall(r'"([A-Z0-9_]+)":\s*"([^"]*)"', m.group(1)))

gp, sp = go_prefixes(), sh_prefixes()
if gp != sp:
    problems.append("占位清单分叉：Go 独有 %s / shell 独有 %s" % (sorted(gp - sp) or "-", sorted(sp - gp) or "-"))
gd = go_defaults()
# shell 的 case 分支写法是 KEY=VALUE，逐条比对
sh_def = dict(re.findall(r'([A-Z0-9_]{3,})=([^|\s)]+)', sh.split("is_template_default()")[1][:400])) if "is_template_default()" in sh else {}
if "is_template_default()" not in sh:
    problems.append("shell 侧 is_template_default 不见了")
if set(gd) != set(sh_def):
    problems.append("出厂默认表键集分叉：Go=%s shell=%s" % (sorted(gd), sorted(sh_def)))
for k, v in gd.items():
    if k in sh_def and sh_def[k] != v:
        problems.append("%s 的出厂默认值两边不等：Go=%s shell=%s" % (k, v, sh_def[k]))
    # Go 表必须等于模板真值（否则"没改过"判不出来，本批实踩过）
    m = re.search(r"(?m)^%s=(.*)$" % re.escape(k), ex)
    if not m:
        problems.append("%s 不在 .env.example 里，templateDefaults 却在判它" % k)
    elif m.group(1).strip() != v:
        problems.append("%s 的表值与 .env.example 真值不等：Go=%s 模板=%s" % (k, v, m.group(1).strip()))
print("\n".join(problems) if problems else "OK")
PY
)
if [ "$DRIFT" = "OK" ]; then
  ok "⑨ 判据同源：shell 占位清单/出厂默认表 == config/envaudit.go == .env.example"
else
  bad "⑨ 判据同源锁红：$(printf '%s' "$DRIFT" | tr '\n' '；')"
fi

echo ""
echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1
exit 0

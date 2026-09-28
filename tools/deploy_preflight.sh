#!/bin/bash
# ============================================================
# 上线前置校验（2026-09-23 D3/D4 批）
#
# 防的是同一类事故：**编排/配置层面的缺失不报错，只在真用起来时暴露**。
# 本脚本把「部署前必须为真、但机器上任何一层都不自动断言」的十余项收在一处，
# 一条命令给出可粘贴进上线工单的 PASS/FAIL 清单。
#
# 用法：
#   ./tools/deploy_preflight.sh            # 校验 .env + 编排 + 静态门禁（默认，只读）
#   ./tools/deploy_preflight.sh --apply-env-check  同上（保留位，语义不变）
# 退出码：0 全部通过；1 存在 FAIL（FAIL 项必须在上线前处置或书面豁免）。
#
# 只读纪律：本脚本不启动容器、不连生产库、不打印任何密钥值——
# 涉及 .env 的项一律只报「键存在/为空/仍是占位符」三态。
# ============================================================
set -u
cd "$(dirname "$0")/.."

ENV_FILE="${1:-.env}"
[ -f "$ENV_FILE" ] || ENV_FILE=.env
# 编排文件可覆盖（FIX-7 反向用例需要）：tools/test_deploy_preflight.sh 会喂一份合成 compose，
# 断"副本>1 且 Redis 未开"这一腿真的会 FAIL——没有这个覆写，反向用例只能靠改生产编排来做，
# 而那正是"为了让测试通过而动交付物"的开端。
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.prod.yml}"
export COMPOSE_FILE
# 模板文件可覆盖（FIX-N 反向用例）：tools/test_deploy_preflight.sh 会喂一份**剥掉部署面键**的
# 合成模板，断"照模板重建会静默丢键"这条判据真的会响。没有这个覆写，反向用例只能靠改仓库里的
# .env.example 来做——那正是"为了让测试通过而动交付物"的开端（与 COMPOSE_FILE 覆写同理由）。
ENV_EXAMPLE_FILE="${ENV_EXAMPLE_FILE:-.env.example}"

PASS=0; FAIL=0
ok()   { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $1"; }

# env_get <KEY>：只回显值；调用方**不得**把结果写进日志，仅用于判空/判占位符
env_get() { grep -E "^[[:space:]]*$1[[:space:]]*=" "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d '"' | tr -d ' '; }
env_has() { grep -qE "^[[:space:]]*$1[[:space:]]*=" "$ENV_FILE" 2>/dev/null; }

# ---- 外部凭据"占位形态"判据（FIX-N 2026-09-28 .env 丢失处置批）----
# 为什么不能沿用上面那句 `[ -n "$(env_get K)" ]`：本机 .env 被误删后只能从模板重建，
# 于是每个键都**非空但全是模板串**（sk-your-siliconflow-key 这种），旧判据一律报 PASS。
# 「键存在」≠「凭据可用」，这是同一族安静失败里最贵的一次。
# 清单与 config/envaudit.go 的 placeholderPrefixes 同源；两边集合的漂移由
# tools/test_deploy_preflight.sh 的「判据同源」用例逐词比对——分叉的后果是运行期观测位
# 与上线预检一个说红一个说绿，而 .env 其实整片没回填。
PLACEHOLDER_PREFIXES="your- your_ sk-your change-me change_me changeme placeholder xxxx todo <"
# 出厂默认值逐字表（须与 .env.example 的真值一致，同样由反证脚本比对）
is_template_default() { # $1=键名 $2=现值
  case "$1=$2" in
    HEALTH_TOKEN=local-dev-health-2026|LLM_GATEWAY_TOKEN=change-me-gateway-shared-secret) return 0 ;;
    *) return 1 ;;
  esac
}
# is_placeholder_value：空串**不**算占位（留空属"这条没启用"，由成对判据与 warn 分别表达）
is_placeholder_value() {
  local s
  s=$(printf '%s' "${1:-}" | tr -d '[:space:]' | tr 'A-Z' 'a-z')
  [ -z "$s" ] && return 1
  for p in $PLACEHOLDER_PREFIXES; do
    case "$s" in "$p"*) return 0 ;; esac
  done
  return 1
}

echo "==== 上线前置校验（env=${ENV_FILE}） ===="

# ---- 1. 生产闸门：这三项错了就是资金/合规事故 ----
echo "-- 1) 生产闸门"
if env_has GIN_MODE && [ "$(env_get GIN_MODE)" = "release" ]; then
  ok "GIN_MODE=release（mock-pay/SSRF/SQL 日志三闸的前提）"
else
  bad "GIN_MODE 未显式为 release（当前 '$(env_get GIN_MODE)'）——IsDevModeConfirmed 会把生产当开发态放行"
fi
if [ "$(env_get APP_ENV)" = "prod" ]; then
  ok "APP_ENV=prod（与 GIN_MODE 组成启动互断言，二者缺一即拒启）"
else
  bad "APP_ENV 未设为 prod（当前 '$(env_get APP_ENV)'）——prod 且 GIN_MODE!=release 直接 Fatalf 的护栏形同虚设"
fi
AMP="$(env_get ALLOW_MOCK_PAY)"
if [ -z "$AMP" ] || [ "$AMP" = "false" ]; then
  ok "ALLOW_MOCK_PAY 未开（模拟支付默认 403）"
else
  bad "ALLOW_MOCK_PAY=${AMP}——生产严禁开模拟支付，演练请显式 docker compose run -e ALLOW_MOCK_PAY=true"
fi

# ---- 2. 密钥与必需项：只判形态，不回显值 ----
echo "-- 2) 密钥/必需项"
JS="$(env_get JWT_SECRET)"
case "$JS" in
  "") bad "JWT_SECRET 为空（服务启动即失败）";;
  *change-me*|*your-*|*placeholder*) bad "JWT_SECRET 仍是占位符（CI 弱密钥守卫同判）";;
  *) [ ${#JS} -ge 32 ] && ok "JWT_SECRET 已设且长度 ${#JS}>=32" || bad "JWT_SECRET 长度 ${#JS}<32，熵不足";;
esac
DP="$(env_get DB_PASSWORD)"
if [ -z "$DP" ] || [ "$DP" = "dev123" ]; then
  bad "DB_PASSWORD 为空或仍是模板值 dev123（公网机器上等于开门）"
else
  ok "DB_PASSWORD 已设为非模板值（长度 ${#DP}）"
fi
for k in DB_HOST DB_NAME DB_USER; do
  env_has "$k" && ok "$k 已声明" || bad "$k 缺失"
done

# ---- 2b. 外部凭据回填形态：键在≠凭据可用（FIX-N 2026-09-28）----
# 只报键名与原因，**任何情况下都不回显值**（值可能就是真凭据，一旦进终端/工单就是泄露面）。
echo "-- 2b) 外部凭据回填形态（值不回显）"
CRED_KEYS="SILICONFLOW_API_KEY DEEPSEEK_API_KEY ZHIPU_API_KEY GLM_API_KEY EMBEDDING_API_KEY RERANK_API_KEY SMTP_HOST SMTP_USER SMTP_PASS SMTP_FROM HEALTH_TOKEN LLM_GATEWAY_TOKEN COLLECTOR_KEY"
CRED_KEY_N=0
UNBACKFILLED=0
for k in $CRED_KEYS; do
  env_has "$k" || continue            # 键不存在＝这条能力没启用，另有成对判据与 warn 覆盖
  CRED_KEY_N=$((CRED_KEY_N+1))
  v="$(env_get "$k")"
  [ -z "$v" ] && continue             # 显式留空＝设计内关闭（降级链少一路兜底，不是配置错）
  if is_placeholder_value "$v"; then
    bad "外部凭据 $k 仍是模板占位形态（.env 从模板重建后没回填；值不回显）"
    UNBACKFILLED=$((UNBACKFILLED+1))
  elif is_template_default "$k" "$v"; then
    bad "外部凭据 $k 仍是出厂默认值：守卫/共享密钥等于没设（值不回显）"
    UNBACKFILLED=$((UNBACKFILLED+1))
  fi
done
[ "$UNBACKFILLED" = 0 ] && ok "已声明的外部凭据无模板占位/出厂默认形态（本文件内共判 $CRED_KEY_N 个凭据键）"
# 成对判据：URL 配了而凭据没配 = 半配，后果不是"没这功能"而是"每次请求真去拨号、每次必失败"。
# RERANK_API_KEY 留空时代码复用 EMBEDDING_API_KEY（rerank.go:72-75），故回落键可用即算配好——
# 把它报成半配就是预检自己在造红。
for pair in "EMBEDDING_API_URL EMBEDDING_API_KEY -" "RERANK_API_URL RERANK_API_KEY EMBEDDING_API_KEY" "LLM_GATEWAY_URL LLM_GATEWAY_TOKEN -" "SMTP_HOST SMTP_PASS -"; do
  read -r uk kk fk <<< "$pair"
  [ "$fk" = "-" ] && fk=""
  uv="$(env_get "$uk")"; kv="$(env_get "$kk")"
  if [ -n "$fk" ] && { [ -z "$kv" ] || is_placeholder_value "$kv"; }; then
    fbv="$(env_get "$fk")"
    if [ -n "$fbv" ] && ! is_placeholder_value "$fbv"; then kv="$fbv"; fi
  fi
  u_set=0; [ -n "$uv" ] && u_set=1
  k_set=0; if [ -n "$kv" ] && ! is_placeholder_value "$kv"; then k_set=1; fi
  if [ "$u_set" != "$k_set" ]; then
    if [ "$u_set" = 1 ]; then
      bad "$uk 已声明而 $kk 未回填（半配：实现会真的拨号，每请求吃一次失败）"
    else
      warn "$kk 已配而 $uk 为空——这条增强不会生效（代码按 $uk 拨号）"
    fi
  fi
done
# 写了没人读的键：本机这次事故里最阴的一种形态——.env 里明明有这行，代码从不读它，
# 于是运维以为配好了。SMTP_PASSWORD vs SMTP_PASS 是真实踩过的键名（notifier.go 读 SMTP_PASS）。
if env_has SMTP_PASSWORD && ! env_has SMTP_PASS; then
  bad "键名写错：本仓 SMTP 口令读的是 SMTP_PASS（internal/notify/notifier.go），SMTP_PASSWORD 无人读取"
fi
if [ -n "$(env_get WECOM_WEBHOOK_URL)$(env_get wecom_webhook_url)" ]; then
  warn "WECOM_WEBHOOK_URL 写在 .env 里不生效：告警群 webhook 是 system_configs 平台级键 wecom_webhook_url（tenant_id=0，经后台/超管配置面写）"
fi
# 模板对账：部署面那几个键（模式/代理/指标令牌/副本数）历史上只在真 .env 里有、模板没有，
# 重建 .env 时整片丢掉且不报错。模板里缺这些行本身就是缺陷，这里当 FAIL 报。
if [ -f "$ENV_EXAMPLE_FILE" ]; then
  MISSING_DOC=""
  for k in APP_ENV ALLOW_MOCK_PAY TRUSTED_PROXIES METRICS_TOKEN APP_REPLICAS; do
    grep -qE "^[[:space:]]*#?[[:space:]]*${k}=" "$ENV_EXAMPLE_FILE" || MISSING_DOC="$MISSING_DOC $k"
  done
  if [ -n "$MISSING_DOC" ]; then
    bad "模板 $ENV_EXAMPLE_FILE 缺部署面键（照模板重建 .env 会静默丢掉）：$MISSING_DOC"
  else
    ok "模板已覆盖部署面键（APP_ENV/ALLOW_MOCK_PAY/TRUSTED_PROXIES/METRICS_TOKEN/APP_REPLICAS）"
  fi
fi

# ---- 3. 网络与多实例：经代理/多副本必配项 ----
echo "-- 3) 网络与多实例"
if env_has TRUSTED_PROXIES && [ -n "$(env_get TRUSTED_PROXIES)" ]; then
  ok "TRUSTED_PROXIES 已声明（限流/登录防爆破按真实客户端 IP 聚合）"
else
  warn "TRUSTED_PROXIES 未设：直连部署可接受；**经 Nginx/云网关部署则必配**，否则限流按代理 IP 聚合"
fi
# FIX-7(2026-09-27)：这一段原来只在一个角落里判——".env 显式写了 APP_REPLICAS 且 !=1 且 Redis 没开"才 bad，
# 于是三种真实扩容形态全部看不见：compose 里写 `deploy.replicas: 3`、`docker compose up --scale app=3`、
# 以及"编排声明了 redis 服务但应用不连它"（CI 三个 job 恰好就是这个形态：起了一个没人连的 Redis）。
# 判据统一到 readiness 已有的那条（redis_declared_but_down），不新造口径：**声明要开而连不上 = crit**。
# 副本>1 而 Redis 没开在这里就是 FAIL，不再 warn —— 因为后果不是"性能差一点"，
# 而是合并队列/触达/催缴 sweep 每个实例各发一遍（双答、双封、双发信），本仓已因同族问题改过两次。
REDIS_ON_ENV=0
[ "$(env_get REDIS_ENABLED)" = "true" ] && REDIS_ON_ENV=1
# app 服务块：从 `  app:` 到下一个两空格缩进的服务键为止（只读文本，不启容器）
APP_BLOCK=""
if [ -f "$COMPOSE_FILE" ]; then
  APP_BLOCK=$(sed -n '/^  app:[[:space:]]*$/,/^[[:space:]]\{2\}[a-z][a-z0-9_-]*:/p' "$COMPOSE_FILE" 2>/dev/null || true)
fi
REDIS_ON_COMPOSE=0
if printf '%s' "$APP_BLOCK" | grep -qE '^[[:space:]]+REDIS_ENABLED:[[:space:]]*"?true' ; then
  REDIS_ON_COMPOSE=1
fi
# 副本数取"两处声明的最大值"：.env 的 APP_REPLICAS 与编排里的 deploy.replicas
REPLICAS="$(env_get APP_REPLICAS)"
case "$REPLICAS" in ''|*[!0-9]*) REPLICAS=1 ;; esac
CREP=$(printf '%s' "$APP_BLOCK" | grep -E '^[[:space:]]+replicas:' | head -1 | sed 's/.*replicas:[[:space:]]*//' | tr -d '"'"'"' ' || true)
case "$CREP" in
  ''|*[!0-9]*) : ;;
  *) [ "$CREP" -gt "$REPLICAS" ] && REPLICAS="$CREP" ;;
esac

if [ "$REDIS_ON_ENV" = 1 ] || [ "$REDIS_ON_COMPOSE" = 1 ]; then
  ok "Redis 已声明启用（env=$REDIS_ON_ENV / prod 编排=${REDIS_ON_COMPOSE}），多实例协调语义有前提"
else
  warn "REDIS_ENABLED 非 true 且编排未覆写：单实例可接受，多副本会退化成「每实例各发一遍」（触达/催缴/sweep 全受影响）"
fi
if [ "$REPLICAS" -gt 1 ] && [ "$REDIS_ON_ENV" != 1 ] && [ "$REDIS_ON_COMPOSE" != 1 ]; then
  bad "副本数=$REPLICAS 但未开 Redis（G6 红灯项，合并裁决/WS 路由/单活跃会话会各实例单干）——来源：APP_REPLICAS 或编排 deploy.replicas"
fi
if [ -f "$COMPOSE_FILE" ] && grep -qE '^  redis:' "$COMPOSE_FILE" \
   && [ "$REDIS_ON_ENV" != 1 ] && [ "$REDIS_ON_COMPOSE" != 1 ]; then
  bad "编排里起了 redis 服务、应用却不连它（REDIS_ENABLED 未 true）——「起了没人连」比没起更糟：容量按双实例算，语义却按单实例跑"
fi
# 连不上这一半由启动期 readiness 判（redis_declared_but_down 在声明启用而未连通时判 crit），
# 本脚本不启容器、不探端口，故此处只登记口径并在冒烟里断言那个观测位（tools/smoke_redis.sh）。
echo "     多实例语义判据：声明启用而连不上 → /status/detail 的 redis_declared_but_down=declared_but_down(crit)；副本>1 且未开 → 本段 FAIL"

# ---- 4. AI 与触达通道：缺了不报错，只是不出货 ----
echo "-- 4) AI/触达通道（缺失静默降级，最容易漏）"
if [ "$(env_get AI_MOCK_MODE)" = "true" ]; then
  bad "AI_MOCK_MODE=true：生产不会调真实模型（回复全是模板）"
else
  AIKEY=""
  # 判据与 §2b 同源：**非空但不算回填**的模板串不能算"有 Key"（旧写法就在这里给
  # sk-your-siliconflow-key 报过 PASS，而真实 AI 链路每次调用都 401）。
  for k in SILICONFLOW_API_KEY DEEPSEEK_API_KEY GLM_API_KEY LLM_GATEWAY_TOKEN; do
    av="$(env_get "$k")"
    [ -n "$av" ] && ! is_placeholder_value "$av" && ! is_template_default "$k" "$av" && AIKEY="$k"
  done
  [ -n "$AIKEY" ] && ok "AI 供应商 Key 至少一条已回填（${AIKEY}，只报键名）" || bad "AI 供应商 Key 全空或仍是模板值且 AI_MOCK_MODE!=true——AI 链路必然全程失败"
fi
# SMTP 三件（HOST/USER/PASS）齐且非占位才算"可用"：只看 HOST/USER 会放过一个口令没填的
# 配置，而 readiness 的 smtp 位同样按 HOST&&USER 判 configured——三条触达链路（到期邮件/
# 用量预警/催缴）会天天在登录那步失败且不报错。§2b 的成对判据已把 PASS 缺口报成 FAIL，
# 这里再按"齐了没有"给结论，两处口径一致。
SMTP_OK=1
for k in SMTP_HOST SMTP_USER SMTP_PASS; do
  sv="$(env_get "$k")"
  { [ -z "$sv" ] || is_placeholder_value "$sv"; } && SMTP_OK=0
done
if [ "$SMTP_OK" = 1 ]; then
  ok "SMTP 已配齐（到期邮件/用量预警/催缴触达可用）"
else
  warn "SMTP 未配齐（HOST/USER/PASS 任一项空或仍是模板值）：到期邮件、D3 用量预警与催缴会**降级为日志**（不报错，但客户收不到）"
fi
# 口径更正（FIX-N 2026-09-28）：这一项过去按 .env 里有没有 WECOM_WEBHOOK_URL 判"已配"，
# 而**代码从不读这个环境变量**（internal/notify 读的是 system_configs 平台级键
# wecom_webhook_url，tenant_id=0）。照旧写法：在 .env 里写一行就得到一条 PASS，
# 而群通知实际上一条都发不出去——这正是"键存在≠凭据可用"的教科书现场。
# 预检不连库（只读纪律），所以这里如实报"本脚本判不了，须查库/后台"，不再给假 PASS。
warn "告警群 webhook 不在 .env 里：须查 system_configs 平台级键 wecom_webhook_url（后台「触达通知」或 /status/detail 的 alert_channel 观测位）——死信/包质量低分/催缴升级全靠它"

# ---- 5. 编排文件自身 ----
echo "-- 5) 生产编排"
if [ -f "$COMPOSE_FILE" ]; then
  grep -q "env_file" "$COMPOSE_FILE" && ok "prod 编排已挂 env_file（SMTP/AI Key 等运行期直读键可进容器）" \
    || bad "prod 编排未挂 env_file：显式 environment 之外的键全部进不了容器"
  grep -q 'image: pgvector/pgvector' "$COMPOSE_FILE" && ok "PG 镜像含 pgvector（迁移 008 建 vector 扩展的前提）" \
    || bad "PG 镜像非 pgvector：新库首启迁移 008 会失败导致 crash-loop"
  grep -q 'APP_PUBLISH_PORT' "$COMPOSE_FILE" && ok "宿主发布端口用 APP_PUBLISH_PORT（不再随 .env SERVER_PORT 漂移）" \
    || bad "宿主端口仍复用 SERVER_PORT：容器内监听 8080 而宿主按 .env 值发布，探测与真实端口不一致"
else
  bad "缺少编排文件 $COMPOSE_FILE"
fi

# ---- 6. 与迁移/契约相关的静态事实（不连库，只查文件） ----
echo "-- 6) 迁移与契约"
MIG=$(ls migrations/*.up.sql 2>/dev/null | wc -l | tr -d ' ')
echo "     迁移文件数（up）= $MIG"
[ -f api.schema.json ] && ok "api.schema.json 在位（契约层 golden）" || bad "api.schema.json 缺失"
[ -f frontend-react/dist/index.html ] && ok "前端产物已在位（SPA 托管 / 与 /app；容器构建会自行重建）" \
  || warn "本地无 frontend-react/dist：只影响本机直跑，镜像构建阶段会 npm run build"

echo ""
echo "==== 结果: PASS=$PASS FAIL=$FAIL ===="
[ "$FAIL" = "0" ] || exit 1

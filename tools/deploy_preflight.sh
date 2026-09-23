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

PASS=0; FAIL=0
ok()   { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $1"; }

# env_get <KEY>：只回显值；调用方**不得**把结果写进日志，仅用于判空/判占位符
env_get() { grep -E "^[[:space:]]*$1[[:space:]]*=" "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d '"' | tr -d ' '; }
env_has() { grep -qE "^[[:space:]]*$1[[:space:]]*=" "$ENV_FILE" 2>/dev/null; }

echo "==== 上线前置校验（env=$ENV_FILE） ===="

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
  bad "ALLOW_MOCK_PAY=$AMP——生产严禁开模拟支付，演练请显式 docker compose run -e ALLOW_MOCK_PAY=true"
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

# ---- 3. 网络与多实例：经代理/多副本必配项 ----
echo "-- 3) 网络与多实例"
if env_has TRUSTED_PROXIES && [ -n "$(env_get TRUSTED_PROXIES)" ]; then
  ok "TRUSTED_PROXIES 已声明（限流/登录防爆破按真实客户端 IP 聚合）"
else
  warn "TRUSTED_PROXIES 未设：直连部署可接受；**经 Nginx/云网关部署则必配**，否则限流按代理 IP 聚合"
fi
if [ "$(env_get REDIS_ENABLED)" = "true" ]; then
  ok "REDIS_ENABLED=true（多实例锁/广播/限流双轨的前提）"
else
  warn "REDIS_ENABLED 非 true：单实例可接受，多副本会退化成「每实例各发一遍」（触达/催缴/sweep 全受影响）"
fi
if env_has APP_REPLICAS && [ "$(env_get APP_REPLICAS)" != "1" ] && [ "$(env_get REDIS_ENABLED)" != "true" ]; then
  bad "APP_REPLICAS=$(env_get APP_REPLICAS) 但未开 Redis——G6 红灯项，会话单活跃与 WS 路由会不一致"
fi

# ---- 4. AI 与触达通道：缺了不报错，只是不出货 ----
echo "-- 4) AI/触达通道（缺失静默降级，最容易漏）"
if [ "$(env_get AI_MOCK_MODE)" = "true" ]; then
  bad "AI_MOCK_MODE=true：生产不会调真实模型（回复全是模板）"
else
  AIKEY=""
  for k in SILICONFLOW_API_KEY DEEPSEEK_API_KEY GLM_API_KEY LLM_GATEWAY_TOKEN; do
    [ -n "$(env_get $k)" ] && AIKEY="$k"
  done
  [ -n "$AIKEY" ] && ok "AI 供应商 Key 至少一条非空（$AIKEY）" || bad "AI 供应商 Key 全空且 AI_MOCK_MODE!=true——AI 链路必然全程失败"
fi
[ -n "$(env_get SMTP_HOST)" ] && [ -n "$(env_get SMTP_USER)" ] \
  && ok "SMTP 已配置（到期邮件/用量预警/催缴触达可用）" \
  || warn "SMTP 未配置：到期邮件、D3 用量预警与催缴会**降级为日志**（不报错，但客户收不到）"
[ -n "$(env_get WECOM_WEBHOOK_URL)$(env_get wecom_webhook_url)" ] \
  && ok "告警群机器人已配" || warn "告警群未配：死信/包质量低分/催缴升级等告警只落日志"

# ---- 5. 编排文件自身 ----
echo "-- 5) 生产编排"
if [ -f docker-compose.prod.yml ]; then
  grep -q "env_file" docker-compose.prod.yml && ok "prod 编排已挂 env_file（SMTP/AI Key 等运行期直读键可进容器）" \
    || bad "prod 编排未挂 env_file：显式 environment 之外的键全部进不了容器"
  grep -q 'image: pgvector/pgvector' docker-compose.prod.yml && ok "PG 镜像含 pgvector（迁移 008 建 vector 扩展的前提）" \
    || bad "PG 镜像非 pgvector：新库首启迁移 008 会失败导致 crash-loop"
  grep -q 'APP_PUBLISH_PORT' docker-compose.prod.yml && ok "宿主发布端口用 APP_PUBLISH_PORT（不再随 .env SERVER_PORT 漂移）" \
    || bad "宿主端口仍复用 SERVER_PORT：容器内监听 8080 而宿主按 .env 值发布，探测与真实端口不一致"
else
  bad "缺少 docker-compose.prod.yml"
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

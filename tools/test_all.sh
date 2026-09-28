#!/bin/bash
# ============================================================
# test_all.sh —— 自动化测试统一编排入口（能力基建，2026-09-05）
#
# 解决的历史欠账：
#   1. 八套 E2E 脚本共享"改全局开关再恢复"模式，无并发护栏——
#      本入口用 flock 文件锁强制单实例，并跑即拒绝（防互相踩配置）；
#   2. 单测/E2E/前端各自为战，无统一汇总——这里串行编排并输出
#      PASS/FAIL 总账，任一环节失败 exit=1（CI 可直接消费）。
#
# 2026-09-15 价值增强批纳入的自动化内容（无需新增阶段，走既有用例层）：
#   - smoke.sh +2 断言：/status readiness 生产就绪探针（ready 字段 + 检查项清单）；
#   - go test 层：internal/metrics readiness 判级、internal/logx slog 桥接级别映射、
#     internal/archive 冷数据归档（租户范围原子搬移/默认关空转，均连真库）；
#   - 前端 vitest 层：pages/__tests__/smoke.test.tsx 10 核心页 jsdom 冒烟 + 登录漏斗；
#   - CI go job 另有 govulncheck 供应链扫描（首月观察模式，不在本编排内）。
#
# 2026-09-21 商业化批（D1/D2）纳入的自动化内容：
#   - 阶段零新增两道独立门禁（都不依赖服务在跑，秒级失败定位）：
#     · D1 AI 黄金问答集：tools/eval_golden.sh（80 条冻结集，阈值 95%，退出码 0/1/2 三态）；
#     · 导出面中文文档注释棘轮：tools/check_doc_comments.py（Go 38 处 + 前端 38 处基线，只降不升）。
#   - smoke.sh +6 断言（第二十九节）：AI 贡献度看板口径自洽——含"会话数与消息量
#     必须同零或同正"的缺陷复现断言（该断言用历史数字 0/261 双向自证过）。
#   - 前端 vitest +6 例：AIContributionCard（展示值禁复算/窗口切换真发请求/失败态可见）。
#   - playwright +2 例：AI 贡献度卡片真浏览器断言（超管须先选代管租户，否则租户作用域
#     Tab 被橙条顶替，DashboardTab 根本不挂载——首跑即踩到，已记入 spec 注释）。
#
# 用法：
#   ./tools/test_all.sh            # 完整回归（含 uat，耗时约 20-40 分钟）
#   ./tools/test_all.sh --fast     # 快回归：阶段零+单测+构建+tsc+契约+九套 E2E+playwright（跳过 uat）
#   ./tools/test_all.sh --unit     # 仅单元测试层（go test -cover + 前端 vitest）
#   ./tools/test_all.sh --static   # 仅阶段零静态门禁（14 项，不起服务、不跑单测/E2E，分钟级）
#   ./tools/test_all.sh --capacity # 单测+构建+T7契约+C5 双实例 WS/Redis 广播矩阵（本地环境）
#   SERVER_PORT=9090 ./tools/test_all.sh   # 指定服务端口（默认 9090）
#
# 为什么要有 --static（2026-09-28 FIX-A 后续）：阶段零门禁本身也可能是空转的，而"没人去复查"
#   的结构性原因是——想只看一眼静态门禁有没有真在拦东西，却要等十几分钟跑完整回归。有了
#   --static，改完门禁脚本能立刻单跑它自己那一档（deploy_preflight / ops_daily / backup.sh
#   三套反证用例都在这一档里）。
#
# 阶段顺序：阶段零静态门禁（14 项）→ 单元层 → 构建+tsc+契约 → E2E 层（九套断言脚本
#   + playwright）→ 汇总。每阶段失败继续跑后续
#（除非 --failfast），最终以总账定 exit code。
# ============================================================
set -u

# ---- 并发护栏：整仓测试单实例（锁文件在项目根，避免 /tmp 被清）----
LOCK_FILE="$(dirname "$0")/../.test_all.lock"
acquire_lock() {
  # 跨平台：Linux(CI)=flock，macOS 默认无 flock 用 shlock（原子文件锁）
  if command -v flock >/dev/null 2>&1; then
    exec 9>"$LOCK_FILE"
    flock -n 9 || return 1
  elif command -v shlock >/dev/null 2>&1; then
    shlock -f "$LOCK_FILE" -p $$ || return 1
  else
    # 兜底：mkdir 原子性作简易锁
    mkdir "$LOCK_FILE" 2>/dev/null || return 1
  fi
  return 0
}
if ! acquire_lock; then
  echo "[test_all] ✗ 已有测试在跑（$LOCK_FILE 被占）——八套 E2E 共享全局开关，禁止并发，请等待其结束。"
  exit 1
fi
release_lock() {
  if command -v flock >/dev/null 2>&1; then :; elif command -v shlock >/dev/null 2>&1; then rm -f "$LOCK_FILE"; else rmdir "$LOCK_FILE" 2>/dev/null; fi
}
trap 'release_lock' EXIT

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
PORT="${SERVER_PORT:-9090}"
MODE="full"
[ "${1:-}" = "--fast" ] && MODE="fast"
[ "${1:-}" = "--unit" ] && MODE="unit"
[ "${1:-}" = "--capacity" ] && MODE="capacity"
# --static（2026-09-28 新增）：只跑阶段零那批静态门禁就收工。加它是为了"改完门禁脚本能立刻验
# 接线"——以前只能 --unit/--fast 从头跑，等十几分钟才看到阶段零那一屏结果，于是没人会去复查
# 门禁本身是不是空转的（阶段零的红也常被后面阶段的噪声盖掉）。
[ "${1:-}" = "--static" ] && MODE="static"
[ "${1:-}" = "--failfast" ] && FAILFAST=1 || FAILFAST=0

PASS=0; FAIL=0
step() { printf "\n=== [test_all] %s ===\n" "$1"; }
verdict() { # verdict <名称> <退出码>
  if [ "$2" -eq 0 ]; then echo "  PASS  $1"; PASS=$((PASS+1)); else echo "  FAIL  $1"; FAIL=$((FAIL+1)); [ "$FAILFAST" = "1" ] && exit 1; fi
}

# ---------- 阶段零：静态断言（G-12，P2-6 白名单棘轮化 2026-09-22） ----------
step "静态断言：裸 db.DB 白名单棘轮门禁"
# 旧内联段两分支均硬编码 verdict 0，结构上不可能失败（假绿门禁，实测报 480 处仍 PASS）。
# 已改造为：tools/classify_bare_db.py 白名单分类（A–F + g12:platform 豁免）+
# tools/check_bare_db.sh 基线棘轮（.bare_db_baseline=281，只降不升，新增违规即红）。
bash "$ROOT/tools/check_bare_db.sh"
verdict "G-12 裸 db.DB 白名单棘轮" $?

# ---------- 阶段零：静态断言（G-6 防回潮） ----------
step "静态断言：G-6 防回潮回归检查"
G6_FAIL=0
# 1. 注释吞码：sb.WriteString 在注释中（P2-36 修复防回潮）
if grep -rn --include="*.go" '^\s*//.*sb\.WriteString' internal/ | grep -v '_test.go' | grep -q .; then
  echo "  FAIL  G-6: 注释中 sb.WriteString 残留"; grep -rn --include="*.go" '^\s*//.*sb\.WriteString' internal/ | grep -v '_test.go' | head -5; G6_FAIL=1
fi
# 2. 脏常量：uint(2) 用于分配语义（P2-24 修复防回潮）
if grep -rn --include="*.go" 'uint(2)' internal/ | grep -v '_test.go' | grep -v '//' | grep -q .; then
  echo "  FAIL  G-6: uint(2) 脏常量残留"; grep -rn --include="*.go" 'uint(2)' internal/ | grep -v '_test.go' | grep -v '//' | head -5; G6_FAIL=1
fi
# 3. gorm:query_option 废弃 API（P1-23 修复防回潮）
if grep -rn --include="*.go" 'gorm:query_option' internal/ | grep -v '_test.go' | grep -v '//' | grep -q .; then
  echo "  FAIL  G-6: gorm:query_option 废弃 API 残留"; grep -rn --include="*.go" 'gorm:query_option' internal/ | grep -v '_test.go' | grep -v '//' | head -5; G6_FAIL=1
fi
# 3.5 开发态谓词防回潮（P2-1，2026-09-19 批二）：安全判定必须走 config.IsDevModeConfirmed()
#     （GIN_MODE 未设=config 默认严格态），gin.Mode() 未设时隐式 debug——两谓词默认相反，
#     混用即 fail-open。此前 tenant.go 三处信任代理/本地回退、billing.go mock-webhook 后门
#     均因此形同虚设，已全部收口；此断言封新增。
if grep -rn --include="*.go" 'gin\.Mode()' internal/ | grep -v '_test.go' | grep -v '//' | grep -q .; then
  echo "  FAIL  P2-1: gin.Mode() 谓词残留（应统一 config.IsDevModeConfirmed）"; grep -rn --include="*.go" 'gin\.Mode()' internal/ | grep -v '_test.go' | grep -v '//' | head -5; G6_FAIL=1
fi
# 3.7 版本号字面量防分叉（2026-09-23 收尾批）：对外版本曾有**三处**字面量
#     （/status、/status/detail、Sentry Release），全部停在 v2.16.0，而 README 主版本线已 v2.28.0——
#     探针报旧版本会让运维核对错发布批次，Sentry 按旧 release 分组会让按版本排缺陷翻到空组。
#     现统一读 cmd/server/main.go 的 appVersion 常量；此断言封"再写一份字面量"。
#     负向 grep 只判非注释行（注释里保留 v2.16.0 作为事故形态说明，属预期）。
G6_VER=$(grep -rnE --include="*.go" '"version":[[:space:]]*"v[0-9]+\.[0-9]+\.[0-9]|ai-scrm@v' cmd/ internal/ 2>/dev/null | grep -vE '^[^:]+:[0-9]+:[[:space:]]*//')
if [ -n "$G6_VER" ]; then
  echo "  FAIL  版本号字面量分叉（应统一用 appVersion 常量）："; echo "$G6_VER" | head -5; G6_FAIL=1
fi
# 3.8 裸成功信封防回潮（G-13 错误码全量迁移，2026-09-24）：internal/api 的 handler 一律走
#     RespOK / respOK / RespErr / RespErrInternal 四个出口。历史上有一批 handler 直接
#     c.JSON(200, gin.H{"code":0}) 或手写 schema.Response{Code: 0…}，代价不是"格式不齐"，
#     而是**错误在这条路上无处可去**——chat_unauthorized 里两处人工提前退场的 DB 写就把 err
#     直接丢掉、一处相似消息抑制落库失败仍回 200 成功（前端拿到 CustomerMsgID=0 的幽灵消息行）。
#     收敛后整个包只剩两个助手定义本身是裸写，故放行 code.go / response.go，
#     其余文件再出现字面量 code=0 即红（先跑过自证：把 routes_public 的 sitekey 改回裸写会抓到）。
G6_ENVEL=$(grep -rnE --include="*.go" '"code": ?0|Code: +(0|int\(CodeOK\))' internal/api/ 2>/dev/null | grep -v '_test.go' | grep -vE '^internal/api/(code|response)\.go:' | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(//|\*)')
if [ -n "$G6_ENVEL" ]; then
  echo "  FAIL  G-13: internal/api 裸成功信封残留（应走 RespOK/respOK 统一出口）"; echo "$G6_ENVEL" | head -5; G6_FAIL=1
fi
# 3.9 CI 触发档拆分（G-25，2026-09-25）：慢档（全场景 UAT，`test_all --full`）不得再挂在
#     每次 main push 上。它与快档同频等于没有分档——UAT 段要改全局开关再恢复、且与本仓
#     "绝不并发跑两套 E2E"的铁律相撞（抢同一测试库与同一份 ai-scrm.log），跑不完就等于
#     "今天的合入没有回归"。故三向锁：
#       负向：`github.ref == 'refs/heads/main'`（旧条件本身）不得再出现在 ci.yml；
#       正向：必须有 schedule(cron) 与 workflow_dispatch，否则慢档无处可跑＝把回归从 CI 删掉；
#       正向：快慢两档命令必须都在（--fast 每次跑 / --full 慢档跑）。
if grep -q "github.ref == 'refs/heads/main'" .github/workflows/ci.yml 2>/dev/null; then
  echo "  FAIL  G-25: CI 慢档又挂回 main push（应改由 schedule / release tag / 人工触发）"; G6_FAIL=1
fi
if ! grep -q "cron:" .github/workflows/ci.yml 2>/dev/null || ! grep -q "workflow_dispatch:" .github/workflows/ci.yml 2>/dev/null; then
  echo "  FAIL  G-25: CI 缺定时档或人工触发档，慢档无处可跑（分档≠删掉深回归）"; G6_FAIL=1
fi
if ! grep -q "test_all.sh --full" .github/workflows/ci.yml 2>/dev/null || ! grep -q "test_all.sh --fast" .github/workflows/ci.yml 2>/dev/null; then
  echo "  FAIL  G-25: 快慢两档命令缺失（--fast 每次 push/PR、--full 慢档）"; G6_FAIL=1
fi
# 4. D6 盖章护栏（2026-09-16）：db.DB.Create/Save 写租户表必须显式 TenantID——
#    C7 事故形态（无 ctx 盖章落 0）历史上命中两次（C7、P1-5），接入即第三次被抓现行
#    （message_queue.go WriteDegradedNotice，已修）。精准模式检测，见脚本头注释。
python3 tools/check_tenant_stamp.py; verdict "D6 盖章护栏（db.DB 写租户表漏章检测）" $?
# FIX-2 防回潮（2026-09-26 审计批）：通道 HTTP 传输层错误（*url.Error 含完整 URL 与 corpsecret）
# 外抛前必须过 redactErr。判据按"Do 调用点紧跟的 if err != nil 分支"扫，自带六样本 --selftest
# （违规必抓/合规必放/扫描面非空/零调用点可判空转），首版按函数体扫在三处 io.ReadAll 上误伤已收窄。
python3 tools/check_channel_err_redact.py; verdict "通道错误外抛脱敏护栏（FIX-2 防回潮）" $?
# 3.10 退款单号防回潮（FIX-1，2026-09-26 审计批）：发给 PSP 的退款单号必须是**稳定号**
#      （库里 refund_out_no，缺失时按 "RF"+订单号 现推），三处出款适配器共用 refundOutNo() 单点。
#      旧实现是 "RF"+秒级时间戳+订单号在微信/支付宝/通用网关各拼一遍——同一订单第二次请求
#      （重试、双实例补呼、人工再点）在 PSP 侧是一个全新单子，"按 out_refund_no 幂等"从未成立。
#      时间戳一旦回到号里，幂等就又变成一句注释，而这条退化在功能测试里看不出来
#      （第一次永远成功），只有对账/重复出款时才炸。
#      负向：号里不得混入时间格式；正向：三处调用点必须都还在（防止"把三处都删了"也判绿）。
G6_RFNO=$(grep -rnE --include="*.go" '"RF" *\+ *(time\.Now\(\)|fmt\.Sprintf\("%d", *time)|time\.Now\(\)\.Format\("2006[^"]*" *\+)' internal/billing/ 2>/dev/null | grep -v '_test.go' | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(//|\*)')
if [ -n "$G6_RFNO" ]; then
  echo "  FAIL  FIX-1: 退款单号又掺进秒级时间戳（同一次退款会算成两笔，PSP 侧无从幂等）："; echo "$G6_RFNO" | head -5; G6_FAIL=1
fi
G6_RFCALL=$(grep -rlE --include="*.go" 'refundOutNo\(order\)' internal/billing/ 2>/dev/null | grep -v '_test.go' | wc -l | tr -d ' ')
if [ "${G6_RFCALL:-0}" -lt 3 ]; then
  echo "  FAIL  FIX-1: 取稳定退款单号的调用点不足 3 处（微信/支付宝/通用网关必须共用 refundOutNo，当前 ${G6_RFCALL}）"; G6_FAIL=1
fi
# 3.11 后台节拍循环 / 停机序列 / 指标覆盖防回潮（FIX-F，2026-09-28 审计复核批）。
#     本批在 cmd/server/main.go 立了四条结构，退化后**功能测试全绿、生产才炸**，只能静态锁：
#       (a) 17 处周期任务循环必须是 `for { select { case <-bgCtx.Done(): … } }` 形态。
#           退回裸 `for range tk.C` 的后果：`defer tk.Stop()` 一次都不执行，SIGTERM 到来时
#           goroutine 被硬杀——正在跑的 50s 存档 / 15min PIPL 扫描拦腰断，锁残留到 600s TTL。
#           判据用「Done 腿数 = ticker 数」而不是写死 17，加/减循环都不必回来改门禁。
#       (b) 每个循环必须向 bgLoops 报到（停机时"等它跑完"而非"睡一秒猜"），Add 与 Done 数必须相等。
#       (c) main 必须 `<-shutdownDone` 等停机序列走完再返回。Go 运行时在 main 返回时终止所有
#           goroutine——少了这一行，HTTP 宽限/队列排空/通道排空/计量 flush 全是尽力而为
#           （本批实跑日志里整段只剩"收到退出信号"一行，即 P2-8、FIX-5 两批的排空从未真生效）。
#       (d) 请求计数中间件必须注册在 `r.NoRoute`（SPA 回落）**之前**。gin 按注册时刻捕获中间件，
#           排在后面时所有前端深路由对 Prometheus 恒不可见：API 数正常、页面访问数为 0，
#           看板会告诉你"前端没人用"——绿着的盲区比没有指标更危险。
#       (e) smoke.sh 的段号自检 guard 必须钉 LC_ALL=C：BSD sort/uniq 在 en_US.UTF-8 下会把
#           46 个中文段号误折叠成一条，guard 判"全部重复"并在一条断言都没跑时 exit 1。
G6_MAIN="cmd/server/main.go"
G6_BARE_TK=$(grep -nE '^[[:space:]]*for range (tk|ticker)\.C \{' "$G6_MAIN" 2>/dev/null || true)
if [ -n "$G6_BARE_TK" ]; then
  echo "  FAIL  FIX-F(a): main.go 又出现裸 ticker 循环（bgCtx 双通道是唯一形态）："; echo "$G6_BARE_TK" | head -3; G6_FAIL=1
fi
G6_TICK_N=$(grep -c 'time\.NewTicker(' "$G6_MAIN" 2>/dev/null || true)
G6_DONE_N=$(grep -c 'case <-bgCtx\.Done():' "$G6_MAIN" 2>/dev/null || true)
if [ "${G6_TICK_N:-0}" -lt 15 ] || [ "${G6_TICK_N:-0}" != "${G6_DONE_N:-0}" ]; then
  echo "  FAIL  FIX-F(a): ticker 数(${G6_TICK_N:-?}) 与 bgCtx 退出腿数(${G6_DONE_N:-?}) 不等——有循环没接停机"; G6_FAIL=1
fi
G6_ADD_N=$(grep -c 'bgLoops\.Add(1)' "$G6_MAIN" 2>/dev/null || true)
G6_WG_N=$(grep -c 'defer bgLoops\.Done()' "$G6_MAIN" 2>/dev/null || true)
if [ "${G6_ADD_N:-0}" != "${G6_TICK_N:-0}" ] || [ "${G6_WG_N:-0}" != "${G6_TICK_N:-0}" ]; then
  echo "  FAIL  FIX-F(b): bgLoops 登记不齐（Add=${G6_ADD_N:-?} Done=${G6_WG_N:-?} ticker=${G6_TICK_N:-?}）——停机不再等后台轮次收尾"; G6_FAIL=1
fi
G6_WAIT_LINE=$(grep -n '^[[:space:]]*<-shutdownDone$' "$G6_MAIN" 2>/dev/null | head -1 | cut -d: -f1)
G6_EXIT_LINE=$(grep -n 'log\.Println("服务已退出")' "$G6_MAIN" 2>/dev/null | head -1 | cut -d: -f1)
if [ -z "${G6_WAIT_LINE:-}" ] || [ -z "${G6_EXIT_LINE:-}" ] || [ "$G6_WAIT_LINE" -gt "$G6_EXIT_LINE" ]; then
  echo "  FAIL  FIX-F(c): main 未等停机序列完成就返回（shutdownDone 缺失或位置错）——整段排空会变尽力而为"; G6_FAIL=1
fi
G6_MET_LINE=$(grep -n 'metrics\.IncRequest()' "$G6_MAIN" 2>/dev/null | head -1 | cut -d: -f1)
G6_NOROUTE_LINE=$(grep -n 'r\.NoRoute(' "$G6_MAIN" 2>/dev/null | head -1 | cut -d: -f1)
if [ -z "${G6_MET_LINE:-}" ] || [ -z "${G6_NOROUTE_LINE:-}" ] || [ "$G6_MET_LINE" -gt "$G6_NOROUTE_LINE" ]; then
  echo "  FAIL  FIX-F(d): 请求计数中间件又排到 SPA/NoRoute 之后（${G6_MET_LINE:-?} vs ${G6_NOROUTE_LINE:-?}）——前端流量对监控不可见"; G6_FAIL=1
fi
if ! grep -q 'LC_ALL=C sort | LC_ALL=C uniq -d' tools/smoke.sh 2>/dev/null; then
  echo "  FAIL  FIX-F(e): smoke.sh 段号 guard 丢了 LC_ALL=C（locale 异常时会在零断言下整段误杀）"; G6_FAIL=1
fi
verdict "G-6 防回潮断言" $G6_FAIL

# ---------- 阶段零：CI 同口径 gofmt 门禁（2026-09-21 补，PLAN_FIX A2）----------
# 教训（2026-09-21 实测）：test_all 全绿 ≠ CI 绿灯。CI go job 的 gofmt 检查是**独立门禁**
# 且不参与编译——批四提交时 6 个文件未格式化（注释列对齐/import 字典序/doc 注释缩进），
# 被「构建 0 错 + 单测全绿 + 9 套 E2E 全过」完全掩盖，合入即 CI go job 红。
# 此处按 CI 同口径（全仓 Go，排除 vendor/frontend-react）内置，把该形态挡在本地。
step "静态门禁：gofmt -l（与 CI go job 同口径）"
UNFMT="$(gofmt -l $(find . -name '*.go' -not -path './vendor/*' -not -path './frontend-react/*') 2>/dev/null)"
if [ -n "$UNFMT" ]; then
  echo "  FAIL  gofmt 未格式化（本地执行 gofmt -w 修复）："
  echo "$UNFMT" | head -10
  verdict "gofmt -l 门禁（CI 同口径）" 1
else
  verdict "gofmt -l 门禁（CI 同口径）" 0
fi

# ---------- 阶段零：AI 黄金问答集门禁（D1，2026-09-21 新增）----------
# 动机：AI 行为（询价词表 / 路由判定 / 话术评分口径）改动"看起来没事但实际改坏"极其常见，
# 而既有断言脚本只覆盖接口契约与落库，覆盖不到"判定语义有没有漂"。
# 本门禁跑 internal/golden 的冻结集（80 条：routing/keyword/reply/safety 四家族），
# **不联网、不读库、不烧 token**，故可进每次提交；通过率低于阈值即红。
# 双向自证（2026-09-21 实测）：正常集 → RC=0；故意写错 want_route → RC=1；
# 非法用例（缺 id/want_route）→ RC=2。另注：本门禁首次上手即抓到 RouteFish 不可达缺陷。
step "AI 黄金问答集门禁：tools/eval_golden.sh（阈值 95%）"
./tools/eval_golden.sh >/tmp/test_all_golden.log 2>&1
GOLDEN_RC=$?
if [ "$GOLDEN_RC" -eq 0 ]; then
  grep -E '用例数|门禁通过' /tmp/test_all_golden.log | head -3
else
  echo "  详情见 /tmp/test_all_golden.log（失败用例表在报告尾部）"
fi
verdict "AI 黄金问答集门禁（D1）" $GOLDEN_RC

# ---------- 阶段零：导出面中文文档注释棘轮（2026-09-21 新增，注释全量化配套）----------
# 为什么放在阶段零：它是纯静态扫描（不依赖服务/DB），秒级出结果，失败时定位到具体文件+成员名。
# 棘轮口径（只降不升）而非"必须为 0"，与 check_api_contract.sh 的 any 基线同思路；
# 当前基线已清零，故等价于"任何新增导出成员都必须带中文文档注释"。
step "静态门禁：导出面中文文档注释（tools/check_doc_comments.py）"
python3 tools/check_doc_comments.py >/tmp/test_all_doccom.log 2>&1
DOCCOM_RC=$?
tail -3 /tmp/test_all_doccom.log
verdict "导出面中文文档注释棘轮" $DOCCOM_RC

# ---------- 阶段零：超长函数棘轮（FIX-G，2026-09-28 审计复核批）----------
# 口径：非测试 Go 文件里 >150 行的函数**个数只准降不准升**（基线 .longfunc_baseline）。
# 为什么用棘轮而不是"硬顶 150 行"：本批确实把 17 处拆了，但拆完不等于守得住——
# 长函数是"赶进度时最省事的加法"的必然产物（在已经很长的 if 尾巴上再接一段），
# 没有任何机器声音会提示下一个人。写注释"请勿再加长"等于没写。
# 检查器自带 --selftest（造一个 200 行函数必须被抓、30 行必须放行、_test.go 必须排除、
# 真实扫描面必须非空），因为它一旦与语言形态失配就会**静默返回空清单**、门禁恒绿。
step "静态门禁：超长函数棘轮（tools/check_function_length.py）"
python3 tools/check_function_length.py --selftest
LF_SELFTEST=$?
python3 tools/check_function_length.py
LF_RC=$?
verdict "超长函数检查器自证" $LF_SELFTEST
verdict "超长函数棘轮（>150 行个数只降不升）" $LF_RC

# ---------- 阶段零：显式 DB ctx 透传棘轮（AI 链 ctx 批，2026-09-23 批六）----------
# 与上一条同为纯静态扫描（不依赖服务/DB）。口径：`db.DB.WithContext(` 透传点只升不降，
# 与 G-12 裸 db.DB 白名单（只降不升）同向，防"把带 ctx 的写法改回裸句柄"这种静默回退。
step "静态门禁：显式 DB ctx 透传棘轮（tools/check_withcontext_ratchet.sh）"
bash tools/check_withcontext_ratchet.sh
CTX_RC=$?
verdict "显式 DB ctx 透传棘轮" $CTX_RC

# ---------- 阶段零：工作树卫生护栏（O2 配套，2026-09-23 批六）----------
# 钉三件本次审计实际踩到过的事，全部 fail-closed（缺 git 时 SKIP 不假红）：
#   1. 未跟踪的 .go/.sh 没有对应文档条目 —— 新工具/新包不写进 AGENTS.md 就等于不存在；
#   2. 工作树不得残留 *.bak 之类的批量编辑中间产物（会把损坏的旧代码留在树里）；
#   3. 生成物/基线文件不得裸 000 权限（批量脚本写文件的常见事故）。
step "静态门禁：工作树卫生（未跟踪产物 / .bak 残留 / 异常权限）"
bash tools/check_worktree_hygiene.sh
HYG_RC=$?
verdict "工作树卫生护栏" $HYG_RC

# ---------- 阶段零：部署体检脚本的反证用例（FIX-7 运维批，2026-09-27）----------
# 这里 gate 的是 tools/test_deploy_preflight.sh（**反证用例**），不是 deploy_preflight 本体。
# 为什么不 gate 本体：它对本机开发 .env 必然报红（GIN_MODE=debug、缺 APP_ENV、弱 DB 口令），
# 那是它该抓的东西，不是回归失败——把它直接接进每次提交等于长红，长红的门禁等于没有门禁。
# 所以接进来的是"它抓得到这些红"的那六条用例：副本>1 无 Redis（env 与 compose 两个来源各一例）、
# 编排起了 Redis 却没人连、单实例正向不误报。教训同备份脚本那次：
# **一个只会打日志并返回成功的守卫，等于没有守卫**。
step "静态门禁：deploy_preflight 反证用例（tools/test_deploy_preflight.sh，6 例）"
bash tools/test_deploy_preflight.sh >/tmp/test_all_preflight_selftest.log 2>&1
PFS_RC=$?
if [ "$PFS_RC" -ne 0 ]; then tail -15 /tmp/test_all_preflight_selftest.log; fi
verdict "deploy_preflight 反证用例（FIX-7）" $PFS_RC

# ---------- 阶段零：每日运维自检的反证用例（FIX-7 运维批，2026-09-27）----------
# 同上：gate 的是 tools/test_ops_daily.sh（**反证用例**），不是 ops_daily.sh 本体。
# 为什么不 gate 本体：本机 backups/ 里那份 .dump 什么时候过期，取决于这台机器有没有挂 cron，
# 与本次提交有没有改坏东西毫无关系——把它直接接进每次提交等于长红，长红的门禁等于没有门禁。
# 接进来的是五组合成 BACKUP_DIR（不存在 / 零个 dump / 距今 30h / 10 字节 / 当天真 dump），
# 逐组断"该红的红、该绿的不红"。用例全程 OPS_NOTIFY_DISABLED=1，判红只落 stdout 不推运维群。
step "静态门禁：ops_daily 反证用例（tools/test_ops_daily.sh，5 组）"
bash tools/test_ops_daily.sh >/tmp/test_all_ops_daily_selftest.log 2>&1
ODS_RC=$?
if [ "$ODS_RC" -ne 0 ]; then tail -15 /tmp/test_all_ops_daily_selftest.log; fi
verdict "ops_daily 反证用例（FIX-7）" $ODS_RC

# ---------- 阶段零：备份守卫的反证用例（FIX-A 后续，2026-09-28）----------
# 同上两条的口径：gate 的是 tools/test_backup_guard.sh（**反证用例**），不是 backup.sh 本体。
# 为什么不 gate 本体：它要连真库、要写 backups/，接进每次提交等于把回归绑在一台机器的 cron 上。
# 接进来的是三条守卫各自的判别力：RLS 预检（必须拦、且拦在 pg_dump 之前）、未通电库不得被误杀、
# 半截归档不留盘、探针失明只 WARN。全部跑在 stub 的 psql/pg_dump/pg_restore 上，不碰真库。
# 这一组判据自己也被变异检验过（四刀，见该文件头），因为第一版就有"删掉 exit 1 仍全绿"的空转。
step "静态门禁：backup.sh 守卫反证用例（tools/test_backup_guard.sh，6 例）"
bash tools/test_backup_guard.sh >/tmp/test_all_backup_guard_selftest.log 2>&1
BGS_RC=$?
if [ "$BGS_RC" -ne 0 ]; then tail -15 /tmp/test_all_backup_guard_selftest.log; fi
verdict "backup.sh 守卫反证用例（FIX-A 后续）" $BGS_RC

# 阶段零跑完即可收工的模式（--static）：不碰数据库、不编译、不起服务，专给"改门禁验门禁"用。
if [ "$MODE" = "static" ]; then
  echo ""
  echo "==== 阶段零静态门禁总账: PASS=$PASS FAIL=$FAIL（--static：未跑单测/构建/契约/E2E）===="
  if [ "$FAIL" -ne 0 ]; then exit 1; fi
  exit 0
fi

# ---------- 阶段一：单元测试层 ----------
# 单测前置：先把数据库让给单测独占。历史那条「internal/billing 与在线服务后台 ticker 共库存在
# 偶发竞态（首跑红、复跑即绿）」的登记残项，根因不是用例并发，而是**开发者手工起的 ./ai-scrm
# 还在跑**：它的到期巡检 / UsageSink / 存档同步 ticker 会在同一批 tenants、billing_orders 行上
# 与单测断言抢同一个时间窗。E2E 层本来就会 stop→build→重启（阶段三），这里只是把「起服务前先静默」
# 提前到单测之前，让 --unit/--fast 的总账也可信。
# 只杀「本仓自己起的那个实例」：按端口找监听者，再用**进程 cwd** 认领归属，绝不按进程名通杀
# （同名 ./ai-scrm 在另一个 checkout 里也叫这个名，按名字杀会互踩）。
STRAY_PID=$(lsof -ti ":$PORT" -sTCP:LISTEN 2>/dev/null | head -1 || true)
STRAY_CWD=$(lsof -p "${STRAY_PID:-0}" -a -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -1 || true)
if [ -n "$STRAY_PID" ] && [ "$STRAY_CWD" = "$ROOT" ]; then
  step "单元测试层前置：停掉本机已起的本仓实例（PID ${STRAY_PID}，避免与单测共库竞态）"
  ./stop.sh >/dev/null 2>&1 || true
  kill "$STRAY_PID" 2>/dev/null || true
  sleep 2
  # 仍在监听就判红：静默不下来等于整层单测跑在脏库上，总账不可信，别往下走
  if lsof -ti ":$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "  FAIL 端口 ${PORT} 仍被占用（PID ${STRAY_PID} 未退出），请先手工停止再起回归"
    verdict "单元测试层前置：停止残留实例" 1
  else
    verdict "单元测试层前置：停止残留实例" 0
  fi
fi

step "单元测试层：go vet + go test -cover（含 DB 依赖用例，连不上自动跳过）"
go vet ./... >/tmp/test_all_vet.log 2>&1
verdict "go vet ./..." $?
# §八-7(2026-09-18) 防"静默绿"：go test 改跑 -json 单次采集，再由内联 python 拆成两份：
#   1) /tmp/test_all_go.log   —— 原文本视图（PASS/FAIL 判定与下方 grep 完全不变）
#   2) skip 明细统计         —— 逐包累加 "Action":"skip" 用例数并打到汇总区
#   背景：SetupTestDB 连不上库时 t.Skipf，整包 DB 用例可以一个不跑而 go test ./... 全绿，
#   文本日志里 --- SKIP 被 tail -20 截掉后无人发现。-json 是单次运行，不额外增加一遍全量耗时。
GO_TEST_RC=0
# P1-5(2026-09-22)：加 -coverprofile 采集全量覆盖率产物，供单测判定后接覆盖率棘轮门禁复用
#（不再额外跑一遍全量 go test）。
# 2026-09-24 连接槽根因：`go test ./...` 默认按 CPU 核数并发跑包二进制，每个二进制的池上限
# DB_MAX_OPEN_CONNS=25，而本机 PG max_connections=100（另有 3 个超管保留位）——并发 4~8 个包
# 即可能打出 100~200 连接。现场是 internal/billing 的两条用例红：
#   · TestConsumeAIQuotaConcurrentStats：100 个 goroutine 里恰有 12 次
#     "[Usage] 计数失败 … SQLSTATE 53300 remaining connection slots are reserved"，
#     断言 used_ai_calls +100 实得 88（递增本身是 SQL 原子的，丢的是取不到连接的那 12 次）；
#   · TestReconcileBillingLedgerDriven：期望 400 万实得 200 万。同一次运行日志里有 60 处 53300，
#     但对账链自身没报错，真机制是 ReconcileBilling 的全局扫描带 Limit(200) 且无 ORDER BY——
#     库里"paid 缺台账"历史行一多（当日 257 个测试租户残留），本用例那条单就被挤出这 200 行。
#     清理测试数据后该谓词命中 0 行，用例恢复稳定；Limit 无排序的饿死风险另计待决项。
# 两条都与产品代码无关（错误被统计旁路只记日志不抛出），是测试层自伤。两道收口：
#   1) -p 限并发包数（机器更强时用 GO_TEST_PARALLEL 放宽）；
#   2) 单测层把每包连接池压到 8——-p 3 仍见过一次同样的 53300（并发包二进制 + 各自 25 池
#      之和可以逼近 100），而 8×3=24 距上限很远；池满时 goroutine 只是排队等连接，
#      100 个 goroutine 跑一发短 UPDATE 用 8 条连接足够，不改变用例语义。
#      godotenv 不覆盖已存在的环境变量，故此处 export 优先于 .env 的 DB_MAX_OPEN_CONNS=25。
DB_MAX_OPEN_CONNS="${GO_TEST_DB_MAX_CONNS:-8}" go test -json -p "${GO_TEST_PARALLEL:-3}" -cover -coverprofile=/tmp/ai_scrm_coverage.out ./... >/tmp/test_all_go.json 2>&1 || GO_TEST_RC=$?
SKIP_SUMMARY="$(python3 - /tmp/test_all_go.json /tmp/test_all_go.log <<'PY'
import json, sys, collections

src, dst = sys.argv[1], sys.argv[2]
texts = []                      # 文本视图（原 go test 输出逐行还原）
seen = set()                    # (package, test) 去重：skip 事件可能重复
per_pkg = collections.Counter()
total = 0

with open(src, encoding="utf-8", errors="replace") as fh:
    for raw in fh:
        line = raw.rstrip("\n")
        if not line.startswith("{"):
            # 非 JSON 行（go 命令级报错、panic 前的裸输出）原样保留，别把日志弄丢
            if line:
                texts.append(line)
            continue
        try:
            ev = json.loads(line)
        except ValueError:
            if line:
                texts.append(line)
            continue
        out = ev.get("Output")
        if out is not None:
            texts.extend(out.rstrip("\n").split("\n"))
        if ev.get("Action") == "skip" and ev.get("Test"):
            key = (ev.get("Package", ""), ev.get("Test", ""))
            if key in seen:
                continue
            seen.add(key)
            total += 1
            per_pkg[ev.get("Package", "?")] += 1

with open(dst, "w", encoding="utf-8") as fh:
    fh.write("\n".join(texts) + ("\n" if texts else ""))

print(total)
for pkg, n in sorted(per_pkg.items(), key=lambda kv: (-kv[1], kv[0])):
    print("  ⚠ %s 跳过 %d 个用例" % (pkg, n))
PY
)"
SKIP_TOTAL="${SKIP_SUMMARY%%$'\n'*}"
case "$SKIP_TOTAL" in
  ''|*[!0-9]*) SKIP_TOTAL=0 ;;  # python 异常退出时降级为 0，不影响既有 PASS/FAIL 总账
esac
verdict "go test ./...（覆盖率见下方）" $GO_TEST_RC

# ---------- 覆盖率棘轮（P1-5，2026-09-22 新增） ----------
# 复用上方 -coverprofile 产物，总覆盖率只升不降（基线 .coverage_baseline，2026-09-22 落 22.8）。
step "单元测试层：覆盖率棘轮门禁（tools/check_coverage_ratchet.sh）"
# G1-a 修复（2026-09-22 全量审计）：原实现写成
#   `if GO_TEST_RC==0 && 产物存在 → 判定 else verdict 0`
# ——单测一红就跳过并直接打 PASS，属于"恒真假绿门禁"（与本轮已改造的裸 db.DB 内联段同族）。
# 而独立模式的 check_coverage_ratchet.sh 本来在 go test 不绿时 exit 1，两口径互斥。
# 新口径（三条，任一不满足即 FAIL，绝不以跳过冒充通过）：
#   ① 缺产物 = 从未测量 → FAIL；
#   ② 单测未全绿 = 判定前提不成立（红包可能中止测试二进制、覆盖率贡献丢失，实测曾把 23.1 读成 20.6）
#      → 照常跑出数字供参考，但总账记 FAIL，与上方 go test 那条同时红；
#   ③ 全绿且产物齐 → 只升不降正常判定。
if [ ! -f /tmp/ai_scrm_coverage.out ]; then
  echo "  FAIL 覆盖率棘轮: 缺 /tmp/ai_scrm_coverage.out（单测未产出覆盖率，禁止按通过计）"
  verdict "覆盖率棘轮（缺产物，拒绝判定）" 1
else
  COV_PROFILE=/tmp/ai_scrm_coverage.out bash "$ROOT/tools/check_coverage_ratchet.sh"
  COV_RC=$?
  if [ "$GO_TEST_RC" -ne 0 ]; then
    echo "  ⚠ 覆盖率判定前提不成立：go test 有 FAIL（上方红项），其覆盖率数字可能因包内 panic/中止而偏低"
    verdict "覆盖率棘轮（单测未全绿，判定不可信）" 1
  else
    verdict "覆盖率棘轮（只升不降）" $COV_RC
  fi
fi
grep -E "^(ok|FAIL|---)" /tmp/test_all_go.log | tail -20 || true
# 单测红时**必须点名失败用例**：上面那行 tail -20 会把 `--- FAIL: TestXxx` 挤出屏幕，
# 2026-09-23 实跑即踩到——billing 包 FAIL，总账里只剩一行裸 `FAIL`，等于让人回 /tmp 自己猜。
# 失败用例本就有限，红时全量列出（含包级行），零失败时不打扰既有输出。
if [ "$GO_TEST_RC" -ne 0 ]; then
  echo "  ── 失败用例清单（-json 还原文本，全量不截断）──"
  grep -E "^(--- FAIL|FAIL[[:space:]]|FAIL$)" /tmp/test_all_go.log \
    || echo "  （未匹配到 --- FAIL 行：可能是测试二进制中止/panic，见 /tmp/test_all_go.log 末尾）"
fi
# 跳过统计（§八-7）：非零必须显式报警，DB 不可用时"全绿"不再是可信信号
if [ "$SKIP_TOTAL" -gt 0 ]; then
  echo "⚠ 本地跳过 ${SKIP_TOTAL} 个用例（含 -short/DB 不可用），明细："
  printf '%s\n' "${SKIP_SUMMARY#*$'\n'}"
  echo "  （CI 侧同样用例走 t.Fatal 而非 Skip，见 internal/testutil）"
else
  echo "✓ 零跳过（go test 无用例被 skip）"
fi

# D5 配套(2026-09-16B)：核心并发包 -race 抽查——合并队列/实时 Hub 是双发/挂死类
# 缺陷高发区，此前 CI 无 race 检测器，解锁读共享字段（D5）长期隐身。
# 欠账批一(2026-09-24，G-7/G-8)补两包：
#   ./internal/ai   —— stage_models 覆盖链路的请求体记录用 mutex（无 -race 看不见竞争）
#   ./internal/llm  —— 硬拦截矩阵用 atomic 计数断言"端点零请求"，且整包在改包级全局
#                      （config.GlobalConfig.AI / 五个 ai.* 客户端单例 / 知识缓存单例）
# 连接池口径与主单测段一致（本机 PG max_connections=100，默认 -p 按核数会打出 53300）。
step "单元测试层：并发包 -race 抽查（service/realtime/chatflow/channel/billing/ai/llm）"
DB_MAX_OPEN_CONNS="${GO_TEST_DB_MAX_CONNS:-8}" go test -race -count=1 -p 3 \
  ./internal/service ./internal/realtime ./internal/chatflow ./internal/channel \
  ./internal/billing ./internal/ai ./internal/llm >/tmp/test_all_race.log 2>&1
verdict "go test -race（并发包）" $?
grep -E "DATA RACE|^(ok|FAIL)" /tmp/test_all_race.log | tail -12 || true

step "单元测试层：前端 vitest"
( cd frontend-react && npm run test >/tmp/test_all_fe.log 2>&1 )
FE_RC=$?
verdict "frontend vitest" $FE_RC
if [ "$FE_RC" != "0" ]; then tail -20 /tmp/test_all_fe.log || true; fi

if [ "$MODE" = "unit" ]; then
  echo "==== [test_all] 汇总: PASS=$PASS FAIL=${FAIL}（unit 模式）===="
  [ "$FAIL" = "0" ] && exit 0 || exit 1
fi

# ---------- 阶段二：构建层 ----------
step "构建层：后端编译 + 前端 build + typecheck"
go build -o ai-scrm ./cmd/server >/tmp/test_all_build.log 2>&1
verdict "go build ./cmd/server" $?
( cd frontend-react && npx tsc --noEmit >/tmp/test_all_tsc.log 2>&1 )
verdict "前端 tsc --noEmit" $?
# P2-5 修复(2026-09-19 审计批一)：CI 跑 lint:ci 而本地不跑 → "本地绿 CI 红"错位。
# 0 error 硬门 + warning 棘轮基线（当前 154，只降不升），与 GitHub Actions ci.yml 对齐。
( cd frontend-react && npm run lint:ci >/tmp/test_all_lint.log 2>&1 )
verdict "前端 eslint lint:ci（0 error + warning≤基线）" $?
( cd frontend-react && npm run build >/tmp/test_all_febuild.log 2>&1 )
verdict "前端 vite build" $?

# ---------- 阶段二.5：契约层（T7 codegen 防漂移） ----------
step "契约层：T7 apidump golden + api.d.ts + FE 路径孤儿 + as-any 基线"
./tools/check_api_contract.sh >/tmp/test_all_contract.log 2>&1
CONTRACT_RC=$?
verdict "check_api_contract.sh" $CONTRACT_RC
if [ "$CONTRACT_RC" != "0" ]; then tail -20 /tmp/test_all_contract.log || true; fi

# ---------- 阶段二.6：C5 容量矩阵（本地双实例 Redis 广播，可选模式） ----------
if [ "$MODE" = "capacity" ]; then
  step "容量矩阵：C5 双实例 WS ${MATRIX_N:-1000} 建连 + Redis 跨实例广播"
  MATRIX_N="${MATRIX_N:-1000}" ./tools/capacity_matrix.sh >/tmp/test_all_capacity.log 2>&1
  CAP_RC=$?
  verdict "capacity_matrix.sh" $CAP_RC
  if [ "$CAP_RC" != "0" ]; then tail -40 /tmp/test_all_capacity.log || true; fi
  echo "==== [test_all] 汇总: PASS=$PASS FAIL=${FAIL}（capacity 模式）===="
  [ "$FAIL" = "0" ] && exit 0 || exit 1
fi

# ---------- 阶段三：E2E 层（七套断言脚本 + uat） ----------
step "E2E 层：起服务（端口 ${PORT}）"
./stop.sh >/dev/null 2>&1 || true
# 兜底清端口：stop.sh 依赖 .pid 文件，nohup 直启未写时会残留旧进程占端口
pkill -f '^\./ai-scrm' >/dev/null 2>&1 || true
sleep 1
nohup ./ai-scrm > ai-scrm.log 2>&1 &
echo $! > .pid
for i in $(seq 1 60); do sleep 2; [ "$(curl -s -o /dev/null -w '%{http_code}' -m 2 "http://localhost:$PORT/health" 2>/dev/null)" = "200" ] && break; done
# D9 修复(2026-09-16B，见 AUDIT_UAT_VERIFY_2026-09-16B)：各脚本各自清 flag、smoke.sh 执行中
# 还会置 admin=true（B4 改密用例），个别脚本中断即残留，卡死后续所有套件的登录（403）。
# E2E 前置统一复位（admin + 出厂弱密码 sales*，与各套件自清口径一致）。
psql ${TEST_DB_URL:-postgresql://ai_scrm:dev123@localhost/ai_scrm} -tAc \
  "UPDATE tenant_users SET must_change_password=false WHERE username IN ('admin','sales1','sales2','sales3')" >/dev/null 2>&1 || true

# 段名台账写法（2026-09-28 收官批）：**不再硬编码断言项数**。旧写法写死"608 项"，而 smoke 每批只加不减，
# 数字从 608→711→今日更多却一行没改——标题变成一份"看起来权威的过期口径"，比没有数字更坏（读的人会拿它核对结果）。
# 项数以本次实跑 tail 为准；下面只登记段落台账（段号重复由 smoke.sh 段头自检测门禁兜，见其 LC_ALL=C 判据）。
step "E2E 层：smoke.sh（项数以实跑输出为准；段台账 §二十~二十五 批二/三+E4/E2/E3/E9/E10、§二十六~二十七 2026-09-20 审计批、§二十八 B2、§二十九 D2 贡献度口径、§三十 AI 销售闭环、§三十一 数据层治理/版本声明单点锁/微信验签观测位、§三十二 主动触达、§三十三 用量预警与催缴、§三十四 贡献度下钻同源、§三十五 获客活码、§三十六 商机与报价版本链、§三十七 E8 会话存档、§三十八 超管租户检索、§三十九 MQ 两段台账、§四十 行业包档位门槛、§四十一 同编码单上架版本、§四十二~四十六 2026-09-27 端到端审计批（通道密钥脱敏四路/退款单号与终态/分页与标签字典/发票交付五档/PIPL 本人副本）、§四十七 DB 级 RLS 通电〔2026-09-28 FIX-A〕）"
./tools/smoke.sh "$PORT" >/tmp/test_all_smoke.log 2>&1; verdict "smoke.sh" $?; tail -2 /tmp/test_all_smoke.log

step "E2E 层：smoke_perm.sh（角色权限矩阵 33 项）"
./tools/smoke_perm.sh "$PORT" >/tmp/test_all_perm.log 2>&1; verdict "smoke_perm.sh" $?; tail -2 /tmp/test_all_perm.log

step "E2E 层：smoke_chat_identity.sh（聊天身份缺口+clear-delay 身份闸+A1 锁定超时落库+§8.11 归属门禁 fail-closed 30 项）"
./tools/smoke_chat_identity.sh "$PORT" >/tmp/test_all_identity.log 2>&1; verdict "smoke_chat_identity.sh" $?; tail -2 /tmp/test_all_identity.log

step "E2E 层：smoke_org.sh（11 项）"
./tools/smoke_org.sh "$PORT" >/tmp/test_all_org.log 2>&1; verdict "smoke_org.sh" $?; tail -2 /tmp/test_all_org.log

step "E2E 层：smoke_saas.sh（注册漏斗+组织管理 E2E 12 项）"
./tools/smoke_saas.sh "$PORT" >/tmp/test_all_saas.log 2>&1; verdict "smoke_saas.sh" $?; tail -2 /tmp/test_all_saas.log

step "E2E 层：smoke_pay.sh（§W 支付回调验签+防重放+C6 资金安全+M2 nonce 消费点后移+E1-2 平台证书验签政策 49 项）"
./tools/smoke_pay.sh "$PORT" >/tmp/test_all_pay.log 2>&1; verdict "smoke_pay.sh" $?; tail -2 /tmp/test_all_pay.log

step "E2E 层：smoke_channel.sh（企微/微信客服/公众号通道 E2E 81 项，含侧边栏双签名 §十，自建 9091+mockwx；四c 段锁合并队列接管路径——连发 5 条恰好 2 条 AI 回复 + 2 条出站 + 5 条客户消息全落库，双答/丢答/丢历史三个方向同时封，且先按 messages.route_result 证明「这些行来自本段链路」再比等式；§十一 分支D 锁到店第二句的接管复核闸——顾问接手后计划中的补发句必须不开口，等满 48s 覆盖 25~45s 上限）"
./tools/smoke_channel.sh >/tmp/test_all_channel.log 2>&1; verdict "smoke_channel.sh" $?; tail -2 /tmp/test_all_channel.log

step "E2E 层：uat_advisor.sh（顾问工作台字节级 88 断言，2026-09-20 缺陷核实批并入：补齐 advisor 域覆盖缺口；2026-09-25 欠账批 +3：反馈额度口径护栏——20 条评分行不吃「每日 20 条反馈」额度、20 条真反馈仍挡得住第 21 条，配落库前置自检防零行空转）"
# 只读写测试客户/标签/阶段，不动全局开关，可安全并入串行队列（DEFECT_VERIFY §六建议落地）。
./tools/uat_advisor.sh "$PORT" >/tmp/test_all_advisor.log 2>&1; verdict "uat_advisor.sh" $?; tail -2 /tmp/test_all_advisor.log

step "E2E 层：smoke_redis.sh（服务端 Redis 双轨冒烟 23 断言，FIX-7 运维批：本仓十余处「Redis 可用走 Redis、不可用退化内存」的双轨语义，此前**十套 E2E 全跑在退化轨上**，多实例会不会双答/锁会不会共享从未有机器判据。本段自建 9093/9094/9095 三台 release+APP_REPLICAS=2 实例：①连上 Redis 的观测位 connected、公开 /status 不判 crit；②登录防爆破锁真落 Redis（键可见、计数=阈值、TTL 有限），且**第二实例**认同一把锁；③反向对照——声明启用却连不上时观测位转 crit、公开 /status 必须判 crit、失败计数**不得**落进 Redis、也不得共享别人的锁。附带抓到并修掉一条真缺陷：/status/detail 不在 skipTenantPaths 里，release 下按 IP 探针被租户解析拦成 403，观测面自己不可观测（debug 兜底长期遮住）。本机 Redis 不可达时整段显式 SKIP，**不计 PASS**）"
# 本段自己起三台实例、只写自己那两把守卫键（跑完就地清并复查已消失），不动 9090、不动全局开关。
./tools/smoke_redis.sh >/tmp/test_all_redis.log 2>&1
REDIS_RC=$?
if grep -q '^  SKIP' /tmp/test_all_redis.log; then
  echo "  SKIP  smoke_redis.sh：本机 Redis 不可达，多实例协调语义本轮未验证（请 docker compose up -d redis）"
  tail -1 /tmp/test_all_redis.log
else
  verdict "smoke_redis.sh（Redis 双轨）" $REDIS_RC; tail -2 /tmp/test_all_redis.log
fi

step "E2E 层：playwright 真浏览器 E2E（21 项，D3 修复：孤儿套件接门禁；E10 补 /docs/api 文档站渲染；P1-10 补 390px 响应式；2026-09-21 D2 补 AI 贡献度卡片真浏览器断言；2026-09-23 批二补 S2 找回密码文案、第 9/10 项改断 S1 匿名写 400；2026-09-23 触达批补主动触达 Tab 渲染，且 D2/触达两项改为先探一个真进得去的代管租户——按下标取 option 会在清库后命中过期 trial 租户，断言打成 402；2026-09-23 D3/D4 批补第 19 项看板数字下钻〔卡片值取自响应 JSON 而非页面文本，且必须等响应而非等请求——只等请求会在数据回来前读到 0，实测 flake 过一次〕；2026-09-23 获客批补第 20 项活码建码→扫码归因→漏斗下钻，扫码格子必须不可点〔单位是次不是人〕，二维码链接断言必须作用域到 .t-dialog__body〔列表每行渲染同一条链接，全局 getByText 会 strict-mode 命中多元素〕；2026-09-24 残项批补第 21 项超管代管检索——把当年"直接写 localStorage 绕过下拉"的 e2e 收回界面：请求必须带 q=、候选必须收窄成搜索结果、选定后 X-Tenant-ID 必须真的进请求头、搜无命中时不得把当前代管冲掉）"
# 真浏览器渲染/跳转/登录漏斗断言，jsdom 冒烟与 curl 断言都覆盖不了的白屏级回归。
# ⚠ 9090 托管的是 frontend-react/dist **产物**而非源码：改完 .tsx 必须先 build 再跑本套件，
#   否则断言打的是旧 bundle（2026-09-23 实踩：S2 文案已改、页面仍渲染"服务端日志"，误判成修复无效）。
#   编排器在本 step 之前的「前端 vite build」阶段已构建，故 test_all 路径天然安全；单独跑本套件才需手动 build。
# 无 chromium 缓存时 SKIP（不 FAIL——离线机器不该被下载卡死），CI e2e job 已显式安装。
case "$(uname)" in Darwin) PW_CACHE="$HOME/Library/Caches/ms-playwright";; *) PW_CACHE="$HOME/.cache/ms-playwright";; esac
if ls "$PW_CACHE" >/dev/null 2>&1 && ls "$PW_CACHE" | grep -q '^chromium'; then
  (cd frontend-react && npx playwright test --reporter=line) >/tmp/test_all_pw.log 2>&1
  verdict "playwright 浏览器 E2E" $?; tail -3 /tmp/test_all_pw.log
else
  echo "  SKIP  chromium 未安装（安装：cd frontend-react && npx playwright install chromium）"
fi

if [ "$MODE" != "fast" ]; then
  # 断言数写法（2026-09-28 收官批）：与 smoke 同一口径——**不再在标题里写死项数**（旧写"112"，实跑 115）。
  # 段内容变化：§八 Token 三桶级联此前在零凭证环境里只断到"全空→降级"那一格（模拟模式在调模型前就返回，
  # 台账零行、三桶零扣减，中间三格空转）；本轮补 ai_mock_usage_tokens（出厂 0＝行为不变）后四格全判。
  step "E2E 层：uat.sh（全场景，较长；含 M1 账目守恒、A1 正式链落库双入口、§十五 退款口径批、§八 三桶扣减级联〔模拟模式虚拟用量，FIX-M〕）"
  # uat 含真实 AI 调用与长时间等待，默认纳入 full 模式；CI 建议 --fast
  ./tools/uat.sh "$PORT" >/tmp/test_all_uat.log 2>&1; verdict "uat.sh" $?; tail -3 /tmp/test_all_uat.log
else
  echo "  [test_all] fast 模式：跳过 uat.sh（用 --full 跑全场景）"
fi

# ---------- 收尾：恢复现场 + 总账 ----------
./stop.sh >/dev/null 2>&1 || true
echo ""
echo "=========================================================="
echo " [test_all] 自动化测试总账: PASS=$PASS FAIL=${FAIL}（mode=${MODE}）"
echo "=========================================================="
[ "$FAIL" = "0" ] && exit 0 || exit 1

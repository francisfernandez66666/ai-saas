#!/usr/bin/env bash
# T7 契约检查：
#   1. golden 文件与代码一致（apidump --check）
#   2. 前端类型生成无漂移（gen_api_types 重生成 diff）
#   3. 前端调用路径与后端路由清单孤儿检测（FE 调了不存在的路由即红）
# 用法：tools/check_api_contract.sh
set -u
cd "$(dirname "$0")/.."

# ---- 0. grep 方言自证（PLAN_FIX_2026-09-21 A3）----
# 本机命令执行环境的 PATH 会被注入 toybox 版 grep（不支持 GNU 扩展 \s / \b，也不支持
# BRE 的 \| 交替），且**静默返回 0 命中不报错**：
#   · EXPLICIT_ANY 恒算成 0 → 又被"下降自动收紧"规则写进基线（实测 26→0），
#     下次在 GNU/BSD grep 下跑必然 FAIL——污染的是棘轮这类长期机制；
#   · 裸 fetch 的 `grep -v "apiFetch\|lib/api.ts"` 在 toybox 下当字面量、过滤失效
#     （实测 20→22）→ 本机假红，而 CI（GNU grep）是 20，本地与 CI 结论相反。
# 这里先用已知样本自证方言，不通就回退到系统 grep（BSD/GNU 均支持本脚本用到的扩展）。
probe_grep() {
  printf ': any\nas any\n' | "$1" -E -o ':\s*any\b' 2>/dev/null | head -1 | grep -q any 2>/dev/null
}
GREP=grep
if ! probe_grep "$GREP"; then
  for cand in /usr/bin/grep /bin/grep; do
    if [ -x "$cand" ] && probe_grep "$cand"; then GREP="$cand"; break; fi
  done
fi

FAIL=0

# ---- 1. golden 路由清单 ----
if ! go run ./cmd/apidump -out api.schema.json -check > /dev/null; then
  echo "  FAIL  api.schema.json 与路由代码不一致（请跑 go run ./cmd/apidump -out api.schema.json）"
  FAIL=1
fi

# ---- 1.5 E10 开放面规格 golden：spec 与代码同源 + 路由清单双向核对 ----
# openapi.spec.json 由 cmd/apidump -format openapi 生成（内部核对 /openapi/v1 真实路由 ↔ spec paths，
# 新增端点漏文档 / 文档写了未注册端点都会在这里 FAIL），运行时端点与 golden 共用同一构建函数。
if ! go run ./cmd/apidump -format openapi -out openapi.spec.json -check > /dev/null; then
  echo "  FAIL  openapi.spec.json 与规格代码/路由清单不一致（请跑 go run ./cmd/apidump -format openapi -out openapi.spec.json）"
  FAIL=1
fi

# ---- 2. 前端类型漂移 ----
TMP_API_TS=$(mktemp)
trap 'rm -f "$TMP_API_TS"' EXIT
if ! node scripts/gen_api_types.mjs api.schema.json "$TMP_API_TS" > /dev/null; then
  echo "  FAIL  gen_api_types 执行失败"
  FAIL=1
elif ! diff -q "$TMP_API_TS" frontend-react/src/types/api.d.ts > /dev/null; then
  echo "  FAIL  frontend-react/src/types/api.d.ts 与 api.schema.json 不一致（请跑 npm run gen:api）"
  FAIL=1
fi

# ---- 3. 前端调用路径 ↔ 后端路由清单 ----
# G2：脚本现以退出码表示"有孤儿/方法错配"，INFO 反向清单只报告不影响判定，
# 故改用 exit code 而非"stdout 非空即红"。
go run ./cmd/apidump -format paths -out - > /tmp/_api_routes.txt
CONTRACT_OUT=$(node scripts/check_api_contract.mjs /tmp/_api_routes.txt)
CONTRACT_RC=$?
if [ "$CONTRACT_RC" -ne 0 ]; then
  echo "  FAIL  前端调用路径/方法与后端路由清单不一致："
  echo "$CONTRACT_OUT" | grep -vE '^    (GET|POST|PUT|DELETE|PATCH) ' 
  FAIL=1
else
  echo "$CONTRACT_OUT" | grep -E '^  INFO' || true
fi

# ---- 4. 显式 any 基线只降不升 ----
# G3 口径修正(2026-09-14)：原名"as any"实为只统计 `: any` 类型注解，漏计 `as any` 断言。
# 改为同时统计两处显式 any 逃逸（`: any` 注解 + `as any` 断言），与 no-explicit-any 语义对齐。
# D1 口径修正(2026-09-16，AUDIT_DEFECT_VERIFY)：排除 __tests__ 与 *.test.* 文件——
# 基线治理的对象是业务代码的类型逃逸，测试里用 any 做 mock 断言是合理逃生舱；
# 原口径把 13 处测试 any 计入，HEAD 深审批未动业务基线却把门禁顶红（34→39），
# CI contract job 自合入起必红。排除后由"只降不升"规则自动收紧到新基线。
EXPLICIT_ANY=$($GREP -rhoE "(:\s*any\b|\bas any\b)" frontend-react/src --include="*.ts" --include="*.tsx" --exclude-dir="__tests__" --exclude="*.test.ts" --exclude="*.test.tsx" | wc -l | tr -d ' ')
BASELINE_FILE="frontend-react/src/.as_any_baseline"
if [ ! -f "$BASELINE_FILE" ]; then
  echo "$EXPLICIT_ANY" > "$BASELINE_FILE"
  echo "  INFO  显式 any 基线已建立: $EXPLICIT_ANY"
else
  BASELINE=$(tr -d '[:space:]' < "$BASELINE_FILE")
  if [ "$EXPLICIT_ANY" -gt "$BASELINE" ]; then
    echo "  FAIL  显式 any 数量上升: $BASELINE → ${EXPLICIT_ANY}（只降不升）"
    FAIL=1
  elif [ "$EXPLICIT_ANY" -lt "$BASELINE" ]; then
    echo "  INFO  显式 any 数量下降: $BASELINE → ${EXPLICIT_ANY}（基线自动收紧）"
    echo "$EXPLICIT_ANY" > "$BASELINE_FILE"
  fi
fi

# ---- 4.5 生成物 unknown 棘轮（E-1，2026-09-23 批五门禁换目标）----
# api.d.ts 是 codegen 产物：某路由的响应类型来自 handler 文档注释里的
# `// apidump:ts <前端类型名>` 注解（cmd/apidump 扫描 → api.schema.json → gen_api_types.mjs）。
# 没有注解就落成 unknown —— 前端拿 payload 只能自己 as 一遍，契约漂移在编译期无人守，
# 这正是"payload 类型契约"名存实亡的形态（本轮实测 244 条 unknown / 250 条路由）。
# 治理方式与 as-any/裸 fetch 同族：按页分批补注解 + 在 types.ts 落真接口，此处封住新增。
# **二进制端点不计入**：`.csv` 导出与 `.png` 二维码返回的不是 JSON 信封，补 `apidump:ts`
# 只会编造一个"看着像类型"的空接口，反而误导前端去 JSON.parse 一张图（见 §4.5.1）。
# 注意 BSD grep 计零也退出 1，故不能用 `|| echo 0` 兜底（会拼出 "00"）；先取原样输出再判空。
UNKNOWN_TYPES=$($GREP -E '^[[:space:]]*"[A-Z]+ [^"]+": unknown$' frontend-react/src/types/api.d.ts | $GREP -vE '\.(csv|png)\": unknown$' | $GREP -c . || true)
UNKNOWN_TYPES=$(printf '%s' "$UNKNOWN_TYPES" | tr -d '[:space:]')
[ -z "$UNKNOWN_TYPES" ] && UNKNOWN_TYPES=0
UNK_BASELINE_FILE="frontend-react/src/types/.api_dts_unknown_baseline"
if [ ! -f "$UNK_BASELINE_FILE" ]; then
  echo "$UNKNOWN_TYPES" > "$UNK_BASELINE_FILE"
  echo "  INFO  api.d.ts unknown 基线已建立: $UNKNOWN_TYPES"
else
  UNK_BASELINE=$(tr -d '[:space:]' < "$UNK_BASELINE_FILE")
  if [ "$UNKNOWN_TYPES" -gt "$UNK_BASELINE" ]; then
    echo "  FAIL  api.d.ts 响应 unknown 数量上升: $UNK_BASELINE → ${UNKNOWN_TYPES}（新端点请在 handler 上补 apidump:ts 注解并在 types.ts 定义接口，只降不升）"
    FAIL=1
  elif [ "$UNKNOWN_TYPES" -lt "$UNK_BASELINE" ]; then
    echo "  INFO  api.d.ts 响应 unknown 数量下降: $UNK_BASELINE → ${UNKNOWN_TYPES}（基线自动收紧）"
    echo "$UNKNOWN_TYPES" > "$UNK_BASELINE_FILE"
  fi
fi

# ---- 4.6 响应体级字段对账（残项收口，2026-09-24）----
# §4.5 管到"每条路由的响应有没有类型名"，但**类型名对得上不代表字段对得上**：
# 后端改一个 json tag、前端 interface 忘了跟，tsc 与 apidump 都照样绿（interface 是前端自己的
# 断言，运行时无人校验），事故形态是"页面某一列永远空白"。这里补上字段级的两条对账
# （A 后端吐而前端没声明 / B 前端在读后端从不吐的键）+ 检查器自身的 --selftest。
# 无基线、当场归零：首跑即抓到 Customer.acquisition_code 一例真漂移（活码批加了列没同步类型）。
if ! python3 tools/check_body_contract.py --selftest > /dev/null; then
  echo "  FAIL  响应体契约检查器自证失败（解析器已失配，此时的\"零差异\"不可信）"
  FAIL=1
fi
if ! BODY_OUT=$(python3 tools/check_body_contract.py 2>&1); then
  printf '%s\n' "$BODY_OUT" | sed 's/^/  /'
  echo "  FAIL  响应体级契约不一致（见上：改 frontend-react/src/types.ts 或后端响应，别改门禁口径）"
  FAIL=1
else
  printf '%s\n' "$BODY_OUT" | sed 's/^/  /'
fi

# ---- 5. 裸 fetch 棘轮只降不升（P2-6，2026-09-19 审计批三）----
# 统一请求层 lib/api.ts 负责 30s 超时/401 登出/租户头；业务页裸 fetch( 每多一处
# 就多一个击穿点（DashboardTab 超管代管 400 静默即坐实例）。存量 46 处分批迁移，
# 此处封新增（对照 .as_any_baseline 模式，下降自动收紧基线）。
BARE_FETCH=$($GREP -rn "fetch(" frontend-react/src --include="*.ts" --include="*.tsx" \
  --exclude-dir="__tests__" --exclude="*.test.ts" --exclude="*.test.tsx" \
  | $GREP -vE "apiFetch|lib/api\.ts" | wc -l | tr -d ' ')
FETCH_BASELINE_FILE="frontend-react/src/.bare_fetch_baseline"
if [ ! -f "$FETCH_BASELINE_FILE" ]; then
  echo "$BARE_FETCH" > "$FETCH_BASELINE_FILE"
  echo "  INFO  裸 fetch 基线已建立: $BARE_FETCH"
else
  FETCH_BASELINE=$(tr -d '[:space:]' < "$FETCH_BASELINE_FILE")
  if [ "$BARE_FETCH" -gt "$FETCH_BASELINE" ]; then
    echo "  FAIL  裸 fetch 数量上升: $FETCH_BASELINE → ${BARE_FETCH}（新请求请走 apiFetch/AUTH，只降不升）"
    FAIL=1
  elif [ "$BARE_FETCH" -lt "$FETCH_BASELINE" ]; then
    echo "  INFO  裸 fetch 数量下降: $FETCH_BASELINE → ${BARE_FETCH}（基线自动收紧）"
    echo "$BARE_FETCH" > "$FETCH_BASELINE_FILE"
  fi
fi

if [ "$FAIL" = "0" ]; then
  echo "  PASS  T7 契约检查（golden/类型/路径/any 基线/unknown 棘轮/裸 fetch 棘轮）"
fi
exit $FAIL

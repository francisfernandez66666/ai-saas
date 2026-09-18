#!/usr/bin/env bash
# T7 契约检查：
#   1. golden 文件与代码一致（apidump --check）
#   2. 前端类型生成无漂移（gen_api_types 重生成 diff）
#   3. 前端调用路径与后端路由清单孤儿检测（FE 调了不存在的路由即红）
# 用法：tools/check_api_contract.sh
set -u
cd "$(dirname "$0")/.."

FAIL=0

# ---- 1. golden 路由清单 ----
if ! go run ./cmd/apidump -out api.schema.json -check > /dev/null; then
  echo "  FAIL  api.schema.json 与路由代码不一致（请跑 go run ./cmd/apidump -out api.schema.json）"
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
EXPLICIT_ANY=$(grep -rhoE "(:\s*any\b|\bas any\b)" frontend-react/src --include="*.ts" --include="*.tsx" --exclude-dir="__tests__" --exclude="*.test.ts" --exclude="*.test.tsx" | wc -l | tr -d ' ')
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

# ---- 5. 裸 fetch 棘轮只降不升（P2-6，2026-09-19 审计批三）----
# 统一请求层 lib/api.ts 负责 30s 超时/401 登出/租户头；业务页裸 fetch( 每多一处
# 就多一个击穿点（DashboardTab 超管代管 400 静默即坐实例）。存量 46 处分批迁移，
# 此处封新增（对照 .as_any_baseline 模式，下降自动收紧基线）。
BARE_FETCH=$(grep -rn "fetch(" frontend-react/src --include="*.ts" --include="*.tsx" \
  --exclude-dir="__tests__" --exclude="*.test.ts" --exclude="*.test.tsx" \
  | grep -v "apiFetch\|lib/api.ts" | wc -l | tr -d ' ')
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
  echo "  PASS  T7 契约检查（golden/类型/路径/any 基线/裸 fetch 棘轮）"
fi
exit $FAIL

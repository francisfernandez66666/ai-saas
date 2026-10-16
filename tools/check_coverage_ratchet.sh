#!/usr/bin/env bash
# Go 覆盖率棘轮门禁（P1-5 总量 + P1-4 逐包，2026-10-16 扩展）：
#   ① 总覆盖率只升不降（原口径，2026-09-22）
#   ② **逐包**覆盖率只升不降（P1-4 收口，2026-10-16 新增）
#
# 为什么加逐包：总量棘轮有个结构性漏洞——纯逻辑包补测可以把总量抬上去，同时
# internal/api（19.8%）/ cmd/server（2.8%）这类骨架包继续烂，总量不动甚至微升。
# "覆盖率倒置"因此能绕过总量门禁长期存在。逐包底线让每个包各自只升不降，
# 补测收益落在哪个包，哪个包的底线就收紧（--update-baseline）。
#
# 口径与裕量：
#   - 逐包百分比从 coverprofile 聚合（dirname 分组，count==0 的语句计未覆盖），
#     与 go test 自报的 per-package 覆盖率同式；不解析文本日志（格式随 go 版本漂移）。
#   - 裕量：总量 0.3pp、逐包 0.5pp——DB 不可用时 testutil 会 skip 整包用例，
#     单包抖动比总量大（总量是加权平均，跳过一个包只掉几个 pp 的分母权重）。
#     没裕量的棘轮会把"今天 PG 没起"变成永久假红，最后被人默契跳过整道门禁。
#   - 基线文件只降不升（数值是底线，补测后 --update-baseline 收紧）。
#
# 用法：
#   COV_PROFILE=/tmp/ai_scrm_coverage.out tools/check_coverage_ratchet.sh   # 复用单测阶段产物（test_all.sh 走这条）
#   tools/check_coverage_ratchet.sh                                        # 独立模式：自跑全量 go test 收产物
#   tools/check_coverage_ratchet.sh --update-baseline                      # 补测后收紧两份基线
#
# 本机坑：go test 退出时 Go telemetry 会删 ~/Library/Application Support/go/telemetry
# 下的 token 文件，被沙箱 file-delete 拦截后命令非零——GOTELEMETRY=off 关掉遥测即可，
# 对 CI 无影响（CI 本来也没有那个目录）。
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BASELINE_FILE="${COV_BASELINE_OVERRIDE:-$SCRIPT_DIR/../.coverage_baseline}"
PKG_BASELINE_FILE="${COV_PKG_BASELINE_OVERRIDE:-$SCRIPT_DIR/../.coverage_pkg_baseline}"
COV_OUT="${TMPDIR:-/tmp}/ai_scrm_coverage.out"

# ---------- --selftest：双向自证（防"永远通过"） ----------
# 合成一份 coverprofile（pkg_a 100% / pkg_b 50% / 总量 75%），配临时基线跑判定本体：
#   已知应放行：底线全低于实测 → exit 0
#   已知应拦截：总量底线高于实测 / 某包底线高于实测 / 基线缺包（profile 有它） /
#               profile 出现未登记新包 → 各自 exit 1 且报错文案点名对应包
# 判定逻辑与主流程同一份代码（仅基线路径与产物路径走 env 覆盖），不是另写一套。
if [ "${1:-}" = "--selftest" ]; then
  ST=$(mktemp -d)
  trap 'rm -rf "$ST"' EXIT
  printf 'mode: set\nai-scrm/pkg/a/a.go:1.1,2.2 2 2\nai-scrm/pkg/b/b.go:1.1,2.2 2 1\n' > "$ST/prof"
  # go tool cover -func 对合成产物的总量 = 3/4 = 75%
  printf '70\n' > "$ST/base_total"
  printf '# tmp\nai-scrm/pkg/a 99\nai-scrm/pkg/b 40\n' > "$ST/base_pkg"

  run_case() { # run_case <期望exit> <描述> [total_base] [pkg_base_content]
    local want="$1" desc="$2" tb="${3:-70}" pb="${4:-ai-scrm/pkg/a 99
ai-scrm/pkg/b 40}"
    printf '%s\n' "$tb" > "$ST/base_total"
    printf '%s' "$pb" > "$ST/base_pkg"
    local out rc
    out=$(COV_PROFILE="$ST/prof" COV_TOTAL_OVERRIDE=75 COV_BASELINE_OVERRIDE="$ST/base_total" \
          COV_PKG_BASELINE_OVERRIDE="$ST/base_pkg" bash "$0" 2>&1)
    rc=$?
    if [ "$rc" -eq "$want" ]; then
      echo "  PASS  selftest: ${desc}（exit=$rc 符合预期）"
    else
      echo "  FAIL  selftest: $desc 期望 exit=$want 实得 $rc"
      echo "$out" | head -5 | sed 's/^/        /'
      SELFTEST_RC=1
    fi
  }
  SELFTEST_RC=0

  run_case 0 "全底线低于实测应放行"
  run_case 1 "总量底线高于实测应拦" 80 "ai-scrm/pkg/a 99
ai-scrm/pkg/b 40"
  run_case 1 "pkg_a 底线高于实测应拦" 70 "ai-scrm/pkg/a 101
ai-scrm/pkg/b 40"
  run_case 1 "基线缺 pkg_b（profile 有）应拦" 70 "ai-scrm/pkg/a 99"
  # 新包未登记：profile 加一个 pkg_c
  printf 'mode: set\nai-scrm/pkg/a/a.go:1.1,2.2 2 2\nai-scrm/pkg/b/b.go:1.1,2.2 2 1\nai-scrm/pkg/c/c.go:1.1,2.2 2 2\n' > "$ST/prof"
  run_case 1 "profile 出现未登记新包应拦" 70 "ai-scrm/pkg/a 99
ai-scrm/pkg/b 40"

  if [ "$SELFTEST_RC" -eq 0 ]; then
    echo "  PASS  覆盖率棘轮 selftest（5 例双向：4 拦 1 放）"
    exit 0
  fi
  echo "  FAIL  覆盖率棘轮 selftest 未全过"
  exit 1
fi


# 复用模式：test_all.sh 单测阶段已用 -coverprofile 跑完全量，直接复用产物。
if [ -n "${COV_PROFILE:-}" ] && [ -f "${COV_PROFILE}" ]; then
  COV_OUT="$COV_PROFILE"
else
  # 独立模式：自己跑全量单测并收集覆盖率（不跑 E2E）
  if ! GOTELEMETRY=off go test -count=1 -coverprofile="$COV_OUT" ./... >/dev/null 2>&1; then
    echo "  FAIL  覆盖率棘轮: go test ./... 未全绿，先修测试再谈覆盖率"
    GOTELEMETRY=off go test -count=1 ./... 2>&1 | /usr/bin/grep -E '^(FAIL|ok.*FAIL)' | head -10
    exit 1
  fi
fi

# ---------- ① 总量口径 ----------
# COV_TOTAL_OVERRIDE 仅供 --selftest 注入已知总量（合成 profile 的路径不是真实源文件，
# go tool cover -func 读不了）；真实跑永远走 go tool cover 解析。
if [ -n "${COV_TOTAL_OVERRIDE:-}" ]; then
  TOTAL="$COV_TOTAL_OVERRIDE"
else
  TOTAL=$(go tool cover -func="$COV_OUT" 2>/dev/null | tail -1 | /usr/bin/grep -oE '[0-9]+\.?[0-9]*%' | tr -d '%')
fi
if [ -z "$TOTAL" ]; then
  echo "  FAIL  覆盖率棘轮: 无法解析总覆盖率（go tool cover 输出异常）"
  exit 1
fi

# ---------- ② 逐包口径：从同一份 profile 聚合 ----------
PKG_JSON=$(python3 - "$COV_OUT" <<'PY'
import sys, json, os
path = sys.argv[1]
stmts = {}   # pkg -> [total, uncovered]
with open(path, encoding="utf-8", errors="replace") as fh:
    for i, line in enumerate(fh):
        if i == 0 or not line.strip():
            continue  # 首行 mode: set/count
        # 形如: ai-scrm/internal/api/chat_main.go:101.34,104.2 3 1
        # 三段：定位串、语句数、命中数（定位串自身不含空格，split 三分最稳）
        parts = line.split()
        if len(parts) != 3:
            continue
        try:
            n, hit = int(parts[1]), int(parts[2])
        except ValueError:
            continue
        fpath = parts[0].split(":")[0]
        pkg = os.path.dirname(fpath)
        t, u = stmts.get(pkg, (0, 0))
        stmts[pkg] = (t + n, u + (n if hit == 0 else 0))
out = {}
for pkg, (t, u) in stmts.items():
    if t > 0:
        out[pkg] = round(100.0 * (t - u) / t, 1)
print(json.dumps(out))
PY
)
if [ -z "$PKG_JSON" ] || [ "$PKG_JSON" = "{}" ]; then
  echo "  FAIL  覆盖率棘轮: coverprofile 聚合不出任何包（产物异常，禁止按通过计）"
  exit 1
fi

# ---------- --update-baseline：两份基线一起收紧 ----------
if [ "${1:-}" = "--update-baseline" ]; then
  NEW_TOTAL=$(python3 -c "print(round(float('$TOTAL') - 0.3, 1))")   # 总量裕量 0.3pp
  printf '%s\n' "$NEW_TOTAL" > "$BASELINE_FILE"
  PKG_JSON="$PKG_JSON" python3 - "$PKG_BASELINE_FILE" <<'PY'
import sys, json, os
target = sys.argv[1]
data = json.loads(os.environ["PKG_JSON"])
lines = [
    "# 逐包覆盖率棘轮底线（P1-4，2026-10-16；tools/check_coverage_ratchet.sh 同口径）",
    "# 取值 = 实测 - 0.5pp 反抖裕量（DB 不可用时 testutil 会 skip 整包用例，单包抖动大于总量）。",
    "# 只降不升：某包补测后跑 check_coverage_ratchet.sh --update-baseline 收紧该包底线。",
    "# 不在表里的包（profile 无线程/零语句）不参与判定。",
]
for pkg in sorted(data):
    v = data[pkg]
    lines.append(f"{pkg} {round(max(v - 0.5, 0.0), 1)}")
open(target, "w", encoding="utf-8").write("\n".join(lines) + "\n")
print(f"  已写 {len(data)} 个包的底线")
PY
  echo "  OK    覆盖率基线已更新：总量 $NEW_TOTAL%（实测 $TOTAL% - 0.3pp 裕量）+ 逐包底线"
  exit 0
fi

# ---------- 判定：总量 ----------
BASE=$(grep -v '^#' "$BASELINE_FILE" 2>/dev/null | grep -v '^$' | tail -1)
if [ -z "$BASE" ]; then
  echo "  FAIL  覆盖率棘轮: 缺少基线文件 .coverage_baseline"
  exit 1
fi
TOTAL_RC=0
if ! python3 -c "import sys; sys.exit(0 if float('$TOTAL') >= float('$BASE') else 1)"; then
  echo "  FAIL  覆盖率棘轮: 总覆盖率 $TOTAL% < 基线 $BASE%（覆盖率回吐，禁止合入）"
  TOTAL_RC=1
fi

# ---------- 判定：逐包 ----------
PKG_RC=0
PKG_REPORT=$(BASE_FILE="$PKG_BASELINE_FILE" PKG_JSON="$PKG_JSON" python3 <<'PY'
import sys, os, json
base_file = os.environ["BASE_FILE"]
cur = json.loads(os.environ["PKG_JSON"])
base = {}
try:
    with open(base_file, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            parts = line.split()
            if len(parts) == 2:
                base[parts[0]] = float(parts[1])
except FileNotFoundError:
    print("MISSING")
    sys.exit(2)
bad = []
for pkg, floor in sorted(base.items()):
    v = cur.get(pkg)
    if v is None:
        bad.append(f"{pkg}: 基线 {floor}% 但本轮 profile 无该包（包被删/改名/编译失败？）")
    elif v < floor:
        bad.append(f"{pkg}: {v}% < 底线 {floor}%")
for pkg, v in sorted(cur.items()):
    if pkg not in base:
        bad.append(f"{pkg}: {v}% 新包未登记底线（补测后跑 --update-baseline）")
if bad:
    print("DROP")
    for b in bad:
        print("    " + b)
    sys.exit(1)
print("OK")
PY
)
if [ "$PKG_REPORT" = "MISSING" ]; then
  echo "  FAIL  覆盖率棘轮: 缺少逐包基线 .coverage_pkg_baseline（跑 --update-baseline 生成）"
  PKG_RC=1
elif [ "$PKG_REPORT" != "OK" ]; then
  echo "  FAIL  覆盖率棘轮: 逐包回吐/未登记——"
  printf '%s\n' "$PKG_REPORT" | tail -n +2
  PKG_RC=1
fi

if [ "$TOTAL_RC" -eq 0 ] && [ "$PKG_RC" -eq 0 ]; then
  PKG_N=$(python3 -c "import json,sys;print(len(json.loads(sys.argv[1])))" "$PKG_JSON" 2>/dev/null || echo '?')
  echo "  PASS  覆盖率棘轮（总量 $TOTAL% / 基线 $BASE%；逐包 $PKG_N 个包全部不低于底线）"
  exit 0
fi
exit 1

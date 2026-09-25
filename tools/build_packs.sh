#!/usr/bin/env bash
# 行业包批量打包（G-21/G-22，2026-09-24）
#
# 为什么要有这个脚本：包源内容一改就得重出 .aipack，而每个包的 code/名称/层级/父包/版本号
# 都是散在命令行里手敲的——上一批 auto 的 1.0.0 甚至被误标成 enterprise（同一个 code 下
# 两行 active、层级不一致），靠人记忆迟早再犯。这里把十件套的打包参数固化成一张表，
# 表就是"发布清单"：改了哪个包源，就在那一行把版本号 +1。
#
# 版本纪律（务必看懂再改）：
#   AutoRegisterLocalPacks 按 (code, version) 幂等注册——**版本号不变 = 不会出新行 =
#   线上仍读旧文件**。所以内容改动必须伴随版本递增，否则"改了但没人吃到"。
#   已注册租户侧要吃到新版，走超管 POST /super/tenants/:id/pack/reapply。
#
# 用法：
#   ./tools/build_packs.sh            # 全量重打包（已存在的输出文件直接覆盖）
#   ./tools/build_packs.sh --check    # 只校验包源内容与三级树参数，不落盘
set -u
cd "$(dirname "$0")/.."
export PATH="$PATH:/usr/local/go/bin"

FAIL=0

# ---- 0. 内容门禁先行 ----
# 空话术、字段名写成 template/content、flows 里 ai_chat 这类"结构对但消费端不读"的缺陷，
# 打进包里不会报错、物化后才表现为"租户拿到一个空壳包"。所以打包前必须先过校验器。
if ! python3 tools/check_pack_content.py; then
  echo "FAIL 包源内容校验未通过，已停止打包（先修 packs-src 再重跑）"
  exit 1
fi

# 打包参数表：源目录|code|名称|版本|层级|父包|行业
PACKS=$(cat <<'EOF'
packs-src/auto|auto|汽车行业包|1.2.0|industry||auto
packs-src/general|general|通用行业包（中立兜底）|1.0.0|industry||general
packs-src/auto_rox|auto_rox|极石汽车|1.1.1|enterprise|auto|auto
packs-src/auto_rox_sales|auto_rox_sales|极石汽车·销售一部|1.0.0|department|auto_rox|auto
packs-src/b2b|b2b|B2B 工业品|1.0.1|industry||b2b
packs-src/crossborder|crossborder|跨境电商|1.0.1|industry||crossborder
packs-src/ecom|ecom|电商零售|1.0.1|industry||ecom
packs-src/edu|edu|教育培训|1.0.1|industry||edu
packs-src/realty|realty|房产置业|1.0.1|industry||realty
packs-src/wedding|wedding|婚庆服务|1.0.1|industry||wedding
EOF
)

if [ "${1:-}" = "--check" ]; then
  echo "INFO --check：仅校验参数表与源目录存在性"
  # 用 <<< 而不是 `echo | while`：管道右边的 while 在**子 shell** 里跑，
  # 子 shell 里的 exit 1 传不出来，脚本照样以 0 收尾——那种"永远绿"的前置检查比没有更糟。
  CHECK_FAIL=0
  while IFS='|' read -r src code name ver level parent industry; do
    [ -n "$src" ] || continue
    if [ ! -d "$src" ]; then
      echo "FAIL 源目录缺失 $src"
      CHECK_FAIL=1
      continue
    fi
    echo "ok   $code v$ver ($level${parent:+ ← $parent}) industry=$industry"
  done <<< "$PACKS"
  exit "$CHECK_FAIL"
fi

while IFS='|' read -r src code name ver level parent industry; do
  [ -n "$src" ] || continue
  if [ ! -d "$src" ]; then
    echo "FAIL 源目录缺失：$src"
    FAIL=1
    continue
  fi
  out="data/packs/${code}_${ver}.aipack"
  args=(-src "$src" -out "$out" -keys keys -code "$code" -name "$name" -version "$ver" -level "$level" -industry "$industry")
  [ -n "$parent" ] && args+=(-parent "$parent")
  if ! go run ./cmd/pack build "${args[@]}" >/dev/null; then
    echo "FAIL 打包失败：$code v$ver"
    FAIL=1
    continue
  fi
  echo "ok   $code v$ver → $out（$(wc -c < "$out" | tr -d ' ') 字节）"
done <<< "$PACKS"

if [ "$FAIL" -ne 0 ]; then
  echo "FAIL 存在打包失败项"
  exit 1
fi
echo "PASS 行业包批量打包完成（$(echo "$PACKS" | grep -c '') 个）"

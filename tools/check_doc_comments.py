#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""导出面中文文档注释棘轮门禁（PLAN_FIX_2026-09-21 · 注释全量化配套）。

## 为什么要这道门禁

"给代码加注释"这件事最大的问题是**没有守门人**：补完当次有效，下一个提交又漏，
半年后回到原样。所以这里把"导出面必须有文档注释"变成一条可执行、可回归的断言，
并用**棘轮**（只降不升）而非"必须为 0"，理由与 `check_api_contract.sh` 的 any 基线一致：
全仓一次性清零对存量改动面太大，棘轮能保证"新代码不再欠账、存量只减不增"。

当前基线已清零（Go 0 / 前端 0），因此棘轮等价于"任何新增导出成员都必须带中文文档注释"。

## 判定规则（刻意保守，避免误报）

Go（`*.go`，跳过 vendor / frontend-react / archived）：
  - `func Foo(...)` / `func (r R) Foo(...)` / `type Foo struct|interface` 且 Foo 首字母大写（导出的）
  - 要求**紧邻上一行**是 `//` 注释。Go 的 doc comment 语法就要求紧邻，
    中间空行即不算 doc comment，故此处不做"向上跳过空行"的宽松处理。

前端（`frontend-react/src/**/*.ts|tsx`，跳过自动生成的 `*.d.ts`）：
  - `export function` / `export async function` / `export default function` / `export const` /
    `export interface` / `export type`
  - 要求紧邻上一行以 `//`、`/*` 或 `*` 开头（兼容 `//` 行注释与 `/** */` JSDoc 块）。
  - `*.d.ts` 整体跳过：它是 `gen_api_types.mjs` 的产物，手写注释会在下次生成时被抹掉。

## 用法

  python3 tools/check_doc_comments.py              # 门禁：超过基线则 exit 1
  python3 tools/check_doc_comments.py --list       # 只列清单，不判基线
  python3 tools/check_doc_comments.py --update-baseline   # 把当前值写入基线（仅当下降时使用）
  python3 tools/check_doc_comments.py --selftest   # 自证：注入探针必须被命中
"""

import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASELINE_FILE = os.path.join(ROOT, ".doc_comments_baseline")

GO_SKIP_DIRS = ("/vendor/", "/frontend-react/", "/archived/", "/.git/", "/node_modules/")
FE_ROOT = os.path.join(ROOT, "frontend-react", "src")

GO_EXPORT = re.compile(r"^func (\([^)]*\) )?([A-Z]\w*)")
GO_TYPE = re.compile(r"^type ([A-Z]\w*) (struct|interface)\b")
FE_EXPORT = re.compile(r"^export (default )?(async )?function (\w+)")
FE_CONST = re.compile(r"^export const (\w+)")
FE_TYPE = re.compile(r"^export (interface|type) (\w+)")


def _has_comment_above(lines, idx, prefixes):
    """紧邻上一行是否注释（idx==0 视为无注释）。"""
    if idx == 0:
        return False
    return lines[idx - 1].strip().startswith(prefixes)


def scan_go():
    """返回 {相对路径: 缺注释的导出成员名列表}。"""
    out = {}
    for dirpath, dirnames, filenames in os.walk(ROOT):
        posix = dirpath.replace(os.sep, "/") + "/"
        if any(s in posix for s in GO_SKIP_DIRS):
            dirnames[:] = []
            continue
        for fn in filenames:
            if not fn.endswith(".go"):
                continue
            path = os.path.join(dirpath, fn)
            try:
                lines = open(path, encoding="utf-8", errors="ignore").read().splitlines()
            except OSError:
                continue
            missing = []
            for i, line in enumerate(lines):
                name = None
                m = GO_EXPORT.match(line)
                if m:
                    name = m.group(2)
                else:
                    m = GO_TYPE.match(line)
                    if m:
                        name = m.group(1)
                if name and not _has_comment_above(lines, i, ("//",)):
                    missing.append(name)
            if missing:
                out[os.path.relpath(path, ROOT)] = missing
    return out


def scan_fe():
    """返回 {相对路径: 缺注释的导出成员名列表}。"""
    out = {}
    for dirpath, dirnames, filenames in os.walk(FE_ROOT):
        for fn in filenames:
            if not fn.endswith((".ts", ".tsx")) or fn.endswith(".d.ts"):
                continue
            path = os.path.join(dirpath, fn)
            try:
                lines = open(path, encoding="utf-8", errors="ignore").read().splitlines()
            except OSError:
                continue
            missing = []
            for i, line in enumerate(lines):
                name = None
                for pat, grp in ((FE_EXPORT, 3), (FE_CONST, 1), (FE_TYPE, 2)):
                    m = pat.match(line)
                    if m:
                        name = m.group(grp)
                        break
                if name and not _has_comment_above(lines, i, ("//", "/*", "*")):
                    missing.append(name)
            if missing:
                out[os.path.relpath(path, ROOT)] = missing
    return out


def load_baseline():
    """.doc_comments_baseline 形如 go=0 / fe=0，缺失视为 0（即最严）。"""
    base = {"go": 0, "fe": 0}
    if not os.path.exists(BASELINE_FILE):
        return base
    for line in open(BASELINE_FILE, encoding="utf-8"):
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        if k.strip() in base:
            try:
                base[k.strip()] = int(v.strip())
            except ValueError:
                pass
    return base


def report(title, found):
    total = sum(len(v) for v in found.values())
    print(f"  {title}: {total} 处 / {len(found)} 文件")
    for path, names in sorted(found.items(), key=lambda kv: -len(kv[1])):
        print(f"      {len(names):3d}  {path}  ->  {', '.join(names[:6])}"
              + (" …" if len(names) > 6 else ""))
    return total


def selftest():
    """自证：构造一个「有注释」与一个「无注释」的导出，判定必须一放一行一拦。"""
    ok = _has_comment_above(["// 中文注释", "func Foo() {}"], 1, ("//",))
    bad = not _has_comment_above(["", "func Bar() {}"], 1, ("//",))
    go_hit = bool(GO_EXPORT.match("func (s *Svc) Run() error"))
    fe_hit = bool(FE_EXPORT.match("export function Card() {}")) and bool(FE_CONST.match("export const X = 1"))
    if ok and bad and go_hit and fe_hit:
        print("  PASS 自证：判定逻辑能命中无注释、能放行有注释")
        return 0
    print(f"  FAIL 自证失败 ok={ok} bad={bad} go_hit={go_hit} fe_hit={fe_hit}")
    return 1


def main():
    args = set(sys.argv[1:])
    if "--selftest" in args:
        return selftest()

    go_found, fe_found = scan_go(), scan_fe()
    print("==== 导出面中文文档注释检查 ====")
    go_total = report("Go", go_found)
    fe_total = report("前端 TS/TSX", fe_found)

    if "--list" in args:
        return 0

    base = load_baseline()
    print(f"  基线 go={base['go']} fe={base['fe']}；当前 go={go_total} fe={fe_total}")

    if "--update-baseline" in args:
        with open(BASELINE_FILE, "w", encoding="utf-8") as f:
            f.write("# 导出面缺文档注释数量的棘轮基线（只降不升）；由 tools/check_doc_comments.py --update-baseline 生成\n")
            f.write(f"go={go_total}\nfe={fe_total}\n")
        print("  已写入基线")
        return 0

    regressed = []
    if go_total > base["go"]:
        regressed.append(f"Go {base['go']}→{go_total}")
    if fe_total > base["fe"]:
        regressed.append(f"前端 {base['fe']}→{fe_total}")
    if regressed:
        print(f"  FAIL 导出成员缺文档注释回升：{'；'.join(regressed)}")
        print("       修法：给对应导出成员加紧邻的中文文档注释（说明用途/边界，而非复述代码）")
        return 1
    if go_total < base["go"] or fe_total < base["fe"]:
        print("  INFO 注释覆盖已优于基线，可执行 --update-baseline 收紧棘轮")
    print("  PASS 导出面中文文档注释不低于基线")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""全量中文文档注释棘轮门禁（PLAN_FIX_2026-09-21 · 注释全量化配套；2026-10-16 扩为全量）。

## 为什么要这道门禁

"给代码加注释"这件事最大的问题是**没有守门人**：补完当次有效，下一个提交又漏，
半年后回到原样。所以这里把"声明必须有中文文档注释"变成一条可执行、可回归的断言，
并用**棘轮**（只降不升）而非"必须为 0"，理由与 `check_api_contract.sh` 的 any 基线一致：
全仓一次性清零对存量改动面太大，棘轮能保证"新代码不再欠账、存量只减不增"。

当前基线已清零（Go 0 / 前端 0 / 包注释 0），棘轮等价于"任何新增声明都必须带中文文档注释"。

## 覆盖范围（2026-10-16 起为全量，不再是仅导出面）

Go（`*.go`，跳过 vendor / frontend-react / archived）：
  - 全部 `func`（导出 + 非导出）、全部 `type X struct|interface`（导出 + 非导出）
  - 每个包（按目录）至少一个文件的 `package` 行上方有中文注释（整包口径：
    Go 惯例是包注释只写在一个文件里，通常是 doc.go 或主文件）

前端（`frontend-react/src/**/*.ts|tsx`，跳过自动生成的 `*.d.ts`）：
  - `export function` / `export const` / `export interface` / `export type`（导出面）
  - 非导出 `function` 声明、`const` 箭头函数、`type` / `interface`
  - `*.d.ts` 整体跳过：它是 `gen_api_types.mjs` 的产物，手写注释会在下次生成时被抹掉

## 三条硬判定（2026-10-16 实测踩坑后固定，改判定前必读）

1. **注释块必须含中文（CJK）**。旧版只查"有没有注释"，纯英文注释
   （`// TODO: refactor`、从上游抄来的英文 doc、只写标识符名的占位注释）会静默通过——
   门禁名字里的"中文"当时没有任何断言在守。多行块只要任一行含中文即放行：
   首行中文摘要 + 后续行英文参考链接是常见且允许的形态。

2. **块注释要整块提取**。`/* */` 块内不以 `*` 开头的正文行也是注释内容；
   只取闭行 `*/` 会把 org.go / database.go 两处中文块注释误判成"无注释"（实测踩中）。
   单行闭合块 `/** 中文 */` 同样算注释。

3. **字符串里的声明形态不是声明**。仓里有把函数声明当**文本**用的场景：
   errcodes/registry_test.go 用原始字符串给扫描器当样本（内含三处 `func f(c *gin.Context)`）、
   chat_conv_lock_leak_test.go 的好坏样本同理。原始字符串 / 模板字符串 / 块注释内部的行
   一律跳过——纯行正则会把它们当声明，**对测试文件永久误报**（实测一次性误报 4 处）。
   故逐字符扫描并跨行保持字符串/块注释状态。

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

GO_FUNC = re.compile(r"^func (\([^)]*\) )?(\w+)")
GO_TYPE = re.compile(r"^type (\w+) (struct|interface)\b")
GO_PACKAGE = re.compile(r"^package (\w+)")
FE_EXPORT_FUNC = re.compile(r"^export (default )?(async )?function (\w+)")
FE_EXPORT_CONST = re.compile(r"^export const (\w+)")
FE_EXPORT_TYPE = re.compile(r"^export (interface|type) (\w+)")
FE_FUNC = re.compile(r"^(async )?function (\w+)\s*\(")
FE_ARROW = re.compile(r"^const (\w+)\s*(?::[^=]+)?=\s*(async\s*)?\(")
FE_TYPE = re.compile(r"^type (\w+)")
FE_IFACE = re.compile(r"^interface (\w+)")

# CJK 统一表意文字（基本区 + 扩展 A）。中文注释的判据只需要"块内出现汉字"，
# 不要求整篇中文——首行中文摘要 + 英文细节是常见且允许的形态。
CJK_RE = re.compile(r"[\u4e00-\u9fff\u3400-\u4dbf]")


def _line_states(lines):
    """逐行标注行首状态，返回与 lines 等长的列表：

    - 'raw'       行首处于 `...` 原始字符串（Go）/ 模板字符串（TS）内部
    - 'block'     行首处于 /* */ 块注释内部（非开口行）
    - 'block_open'本行开出块注释（含单行即闭合的 /** ... */）
    - 'code'      其余（含行注释行——行注释不跨行，不影响下一行状态）

    为什么需要：声明形态可能出现在字符串里当文本（见模块 docstring 第 3 条），
    只有行首在代码中的行才可能是真声明。逐字符扫描，跨行保持 raw/block 状态。
    """
    states = []
    in_raw = False
    in_block = False
    for line in lines:
        if in_raw:
            states.append("raw")
        elif in_block:
            states.append("block")
        else:
            states.append("code")
        i = 0
        n = len(line)
        while i < n:
            ch = line[i]
            if in_raw:
                if ch == "`":
                    in_raw = False
                i += 1
            elif in_block:
                if ch == "*" and i + 1 < n and line[i + 1] == "/":
                    in_block = False
                    i += 2
                else:
                    i += 1
            elif ch == "`":
                in_raw = True
                i += 1
            elif ch == '"':
                i += 1
                while i < n and line[i] != '"':
                    i += 2 if line[i] == "\\" else 1
                i += 1
            elif ch == "'":
                i += 1
                while i < n and line[i] != "'":
                    i += 2 if line[i] == "\\" else 1
                i += 1
            elif ch == "/" and i + 1 < n and line[i + 1] == "/":
                break  # 行注释：本行余下不是代码
            elif ch == "/" and i + 1 < n and line[i + 1] == "*":
                in_block = True
                states[-1] = "block_open"
                i += 2
            else:
                i += 1
    return states


def _comment_block_above(lines, states, idx):
    """取紧邻上方的完整注释块文本；无注释紧邻（含 idx==0）时返回 None。

    支持三种紧邻形态并混合：`//` 行注释、`/* */` 多行块（块内不以 `*` 开头的
    正文行也纳入）、单行闭合块 `/** ... */`。语义与 Go/JS doc comment 一致：
    以"紧邻上一行"为前提，不向上跳过空行。
    """
    if idx == 0:
        return None
    prev = lines[idx - 1].strip()
    if not (prev.startswith("//") or states[idx - 1] in ("block", "block_open")):
        return None
    out = []
    j = idx - 1
    while j >= 0:
        s = lines[j].strip()
        if s.startswith("//") and states[j] == "code":
            out.append(s)
            j -= 1
            continue
        if states[j] in ("block", "block_open"):
            # 块注释区域：连同开口行一起向上收，块内所有行（含无 * 前缀正文）纳入
            while j >= 0 and states[j] in ("block", "block_open"):
                out.append(lines[j].strip())
                j -= 1
            continue
        break
    out.reverse()
    return "\n".join(out)


def _classify(lines, states, idx):
    """单条声明的注释合规性。返回 None=合规；'NO_COMMENT'=无注释；'NON_CJK'=有注释但无中文。

    idx 行若不是代码（位于字符串/块注释内部）直接返回 None——那不是声明，是文本。
    """
    if states[idx] != "code":
        return None
    block = _comment_block_above(lines, states, idx)
    if block is None:
        return "NO_COMMENT"
    if not CJK_RE.search(block):
        return "NON_CJK"
    return None


def _read(path):
    try:
        return open(path, encoding="utf-8", errors="ignore").read().splitlines()
    except OSError:
        return None


def scan_go():
    """返回 {相对路径: [(声明名, 违例类型)]}——全部 func 与 type struct/interface。"""
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
            lines = _read(path)
            if lines is None:
                continue
            states = _line_states(lines)
            missing = []
            for i, line in enumerate(lines):
                name = None
                m = GO_FUNC.match(line)
                if m:
                    name = m.group(2)
                else:
                    m = GO_TYPE.match(line)
                    if m:
                        name = m.group(1)
                if name:
                    kind = _classify(lines, states, i)
                    if kind:
                        missing.append((name, kind))
            if missing:
                out[os.path.relpath(path, ROOT)] = missing
    return out


def scan_fe():
    """返回 {相对路径: [(声明名, 违例类型)]}——导出成员 + 非导出函数/箭头/类型。"""
    out = {}
    for dirpath, dirnames, filenames in os.walk(FE_ROOT):
        for fn in filenames:
            if not fn.endswith((".ts", ".tsx")) or fn.endswith(".d.ts"):
                continue
            path = os.path.join(dirpath, fn)
            lines = _read(path)
            if lines is None:
                continue
            states = _line_states(lines)
            missing = []
            for i, line in enumerate(lines):
                name = None
                m = FE_EXPORT_FUNC.match(line)
                if m:
                    name = m.group(3)
                else:
                    m = FE_EXPORT_CONST.match(line)
                    if m:
                        name = m.group(1)
                    else:
                        m = FE_EXPORT_TYPE.match(line)
                        if m:
                            name = m.group(2)
                        else:
                            m = FE_FUNC.match(line)
                            if m:
                                name = m.group(2)
                            else:
                                m = FE_ARROW.match(line)
                                if m:
                                    name = m.group(1)
                                else:
                                    m = FE_TYPE.match(line)
                                    if m:
                                        name = m.group(1)
                                    else:
                                        m = FE_IFACE.match(line)
                                        if m:
                                            name = m.group(1)
                if name:
                    kind = _classify(lines, states, i)
                    if kind:
                        missing.append((name, kind))
            if missing:
                out[os.path.relpath(path, ROOT)] = missing
    return out


def scan_go_packages():
    """返回 {相对目录: [(包名, '')]}——该包没有任何文件的 package 行上方带中文注释。

    整包口径：同一目录下任一 .go 文件的 package 行合规即放行整个包。
    """
    out = {}
    for dirpath, dirnames, filenames in os.walk(ROOT):
        posix = dirpath.replace(os.sep, "/") + "/"
        if any(s in posix for s in GO_SKIP_DIRS):
            dirnames[:] = []
            continue
        go_files = sorted(fn for fn in filenames if fn.endswith(".go"))
        if not go_files:
            continue
        pkg_name = ""
        has_comment = False
        for fn in go_files:
            path = os.path.join(dirpath, fn)
            lines = _read(path)
            if lines is None:
                continue
            states = _line_states(lines)
            for i, line in enumerate(lines):
                if line.startswith("package "):
                    parts = line.split()
                    if len(parts) > 1 and not pkg_name:
                        pkg_name = parts[1]
                    if _classify(lines, states, i) is None:
                        has_comment = True
                    break
            if has_comment:
                break
        if not has_comment:
            out[os.path.relpath(dirpath, ROOT)] = [(pkg_name or "?", "")]
    return out


def load_baseline():
    """.doc_comments_baseline 形如 go=0 / fe=0 / pkg=0，缺失视为 0（即最严）。"""
    base = {"go": 0, "fe": 0, "pkg": 0}
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
    for path, items in sorted(found.items(), key=lambda kv: -len(kv[1])):
        names = ", ".join(f"{n}[{k}]" if k else n for n, k in items[:6])
        print(f"      {len(items):3d}  {path}  ->  {names}"
              + (" …" if len(items) > 6 else ""))
    return total


def selftest():
    """自证：判定逻辑必须"英文抓、中文放、无注释抓、空行隔断抓、字符串内不抓"。

    为什么必须自证：本门禁是纯静态扫描，一旦与语言形态失配就会**静默返回空清单**、
    门禁恒绿——那比没有门禁更坏（给人"已覆盖"的错觉）。2026-10-16 实测三类失配
    （英文注释放行、块注释正文行漏收、字符串内声明误报）全部靠这类样本钉住。
    """
    cases = [
        # （说明, 行表, 声明所在行索引, 期望）
        ("Go 中文 // 注释放行", ["// Foo 做某件事", "func Foo() {}"], 1, None),
        ("Go 纯英文注释记欠账", ["// Foo does something", "func Foo() {}"], 1, "NON_CJK"),
        ("Go TODO 式英文记欠账", ["// TODO: refactor", "func Foo() {}"], 1, "NON_CJK"),
        ("Go 只写标识符名记欠账", ["// Foo", "func Foo() {}"], 1, "NON_CJK"),
        ("Go 无注释记欠账", ["", "func Foo() {}"], 1, "NO_COMMENT"),
        ("Go 空行隔断不算 doc", ["// 中文", "", "func Foo() {}"], 2, "NO_COMMENT"),
        ("Go 多行块任一行中文即放行", ["// Foo 做X：", "// see foo_test.go", "func Foo() {}"], 2, None),
        ("Go 非导出函数同样管", ["// helper 做某件事", "func helper() {}"], 1, None),
        ("Go 非导出函数无注释记欠账", ["", "func helper() {}"], 1, "NO_COMMENT"),
        ("Go type 中文注释放行", ["// T 枚举", "type T int"], 1, None),
        ("Go type 英文注释记欠账", ["// T enum", "type T int"], 1, "NON_CJK"),
        ("Go 单行块注释中文放行", ["/** T 枚举 */", "type T int"], 1, None),
        ("Go 单行块注释英文记欠账", ["/** T enum */", "type T int"], 1, "NON_CJK"),
        ("Go 多行块无星号正文行放行", ["/*", "T 枚举：块内正文行不以 * 开头也是注释", "*/", "type T int"], 3, None),
        ("Go 多行块英文记欠账", ["/*", "T enum, plain interior line", "*/", "type T int"], 3, "NON_CJK"),
        ("Go 块注释上方再叠行注释放行", ["// 前缀说明", "/* 块注释 */", "func Foo() {}"], 2, None),
        # 字符串内的声明形态不是声明（errcodes 样本形态，实测误报 4 处的根因）
        ("Go 原始字符串内的 func 不判", ["x := `package p", "func f(c *gin.Context) {", "}`"], 1, None),
        ("Go 原始字符串内的 type 不判", ["s := `abc", "type T struct{}", "`"], 1, None),
        ("Go 块注释内的 func 不判", ["/*", "func Foo() {}", "*/"], 1, None),
        ("FE 中文 JSDoc 放行", ["/** 卡片组件 */", "export function Card() {}"], 1, None),
        ("FE 英文 JSDoc 记欠账", ["/** Card component */", "export function Card() {}"], 1, "NON_CJK"),
        ("FE 多行 JSDoc 中文放行", ["/*", " * 卡片组件", " */", "export function Card() {}"], 3, None),
        ("FE 无注释记欠账", ["", "export const X = 1"], 1, "NO_COMMENT"),
        ("FE 非导出 function 无注释记欠账", ["", "function helper() {}"], 1, "NO_COMMENT"),
        ("FE 非导出箭头函数中文放行", ["// helper 做某件事", "const helper = (a: number) => a"], 1, None),
        ("FE 非导出 type 无注释记欠账", ["", "type Opt = { id: number }"], 1, "NO_COMMENT"),
        ("FE 非导出 interface 中文放行", ["// Repo 数据访问面", "interface Repo {}"], 1, None),
        ("FE 模板字符串内的 function 不判", ["const s = `abc", "function helper() {}", "`"], 1, None),
        ("包中文注释放行", ["// 包说明", "package foo"], 1, None),
        ("包英文注释记欠账", ["// package foo does X", "package foo"], 1, "NON_CJK"),
        ("包无注释记欠账", ["package foo"], 0, "NO_COMMENT"),
        ("包注释只贴构建标签记欠账", ["//go:build linux", "package foo"], 1, "NON_CJK"),
    ]
    failed = 0
    for label, lines, idx, want in cases:
        states = _line_states(lines)
        got = _classify(lines, states, idx)
        ok = got == want
        failed += 0 if ok else 1
        print(f"  {'PASS' if ok else 'FAIL'}  {label}: 期望={want} 实得={got}")
    regex_checks = [
        ("正则认全 Go 形态",
         bool(GO_FUNC.match("func (s *Svc) Run() error")) and bool(GO_FUNC.match("func Foo()"))
         and bool(GO_FUNC.match("func helper() {}")) and bool(GO_TYPE.match("type T struct{"))
         and bool(GO_PACKAGE.match("package main"))),
        ("正则认全前端形态",
         bool(FE_EXPORT_FUNC.match("export function Card() {}"))
         and bool(FE_EXPORT_FUNC.match("export default function P() {}"))
         and bool(FE_EXPORT_CONST.match("export const X = 1"))
         and bool(FE_EXPORT_TYPE.match("export interface I {}"))
         and bool(FE_EXPORT_TYPE.match("export type T = 1"))
         and bool(FE_FUNC.match("function helper() {}"))
         and bool(FE_ARROW.match("const helper = (a: number) => a"))
         and bool(FE_TYPE.match("type Opt = { id: number }"))
         and bool(FE_IFACE.match("interface Repo {}"))),
    ]
    for label, ok in regex_checks:
        failed += 0 if ok else 1
        print(f"  {'PASS' if ok else 'FAIL'}  {label}")
    total = len(cases) + len(regex_checks)
    if failed:
        print(f"  FAIL 自证失败 {failed}/{total}")
        return 1
    print(f"  PASS 自证：{total}/{total}"
          "（英文/无注释/空行隔断必拦截，中文必放行，字符串与块注释内的形态不误报）")
    return 0


def main():
    args = set(sys.argv[1:])
    if "--selftest" in args:
        return selftest()

    go_found, fe_found, pkg_found = scan_go(), scan_fe(), scan_go_packages()
    print("==== 全量中文文档注释检查 ====")
    go_total = report("Go（函数+类型）", go_found)
    fe_total = report("前端 TS/TSX", fe_found)
    pkg_total = report("Go 包注释", pkg_found)

    if "--list" in args:
        return 0

    base = load_baseline()
    print(f"  基线 go={base['go']} fe={base['fe']} pkg={base['pkg']}；"
          f"当前 go={go_total} fe={fe_total} pkg={pkg_total}")

    if "--update-baseline" in args:
        with open(BASELINE_FILE, "w", encoding="utf-8") as f:
            f.write("# 声明缺中文文档注释数量的棘轮基线（只降不升）；"
                    "由 tools/check_doc_comments.py --update-baseline 生成\n")
            f.write(f"go={go_total}\nfe={fe_total}\npkg={pkg_total}\n")
        print("  已写入基线")
        return 0

    regressed = []
    if go_total > base["go"]:
        regressed.append(f"Go {base['go']}→{go_total}")
    if fe_total > base["fe"]:
        regressed.append(f"前端 {base['fe']}→{fe_total}")
    if pkg_total > base["pkg"]:
        regressed.append(f"包注释 {base['pkg']}→{pkg_total}")
    if regressed:
        print(f"  FAIL 声明缺中文文档注释回升：{'；'.join(regressed)}")
        print("       修法：给对应声明加紧邻的中文文档注释（说明用途/边界，而非复述代码）；"
              "纯英文注释同样算欠账")
        return 1
    if go_total < base["go"] or fe_total < base["fe"] or pkg_total < base["pkg"]:
        print("  INFO 注释覆盖已优于基线，可执行 --update-baseline 收紧棘轮")
    print("  PASS 全量中文文档注释不低于基线")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""超长函数棘轮门禁（FIX-G，2026-09-28 审计复核批）。

扫非测试 Go 文件里的函数，统计**行数 > 阈值（默认 150）**的函数个数，与基线
`.longfunc_baseline` 比对——**只准降不准升**（比基线多即 FAIL）。

为什么要这道门禁（而不是"这次把它们拆完就算了"）：
- 一个 300 行的函数不是"代码风格问题"，它是**缺陷藏身处**：本仓历史上最难抓的几个 bug
  （留资判定分支、退款单号幂等、归属校验被 DB 错误跳过、事务里漏章）全都长在
  "一个函数里同时做解析/判据/落库/发信/返回"的那种函数里。段与段之间共享十几个局部变量时，
  改一处不知道另一处也读它。
- 更关键的是**它会长回来**。拆分是纯结构工作，下一次赶进度的人最省事的加法就是在那个
  已经很长的 `if` 尾巴上再接一段——没有任何机器声音会提示他。写注释说"请勿再加长"
  等于没写，历史上这类注释下面平均还会再长几十行。
- 棘轮而不是"硬顶 150 行"：现存 17 处一次拆完的风险（行为回归）大于收益，
  所以口径是"本批拆掉的这些不许回来，新写的超长函数一律红"。

函数行数怎么算：从 `func` 声明行（含多行参数签名的起始行）到与之配对的右花括号行，
按**物理行数**计（含注释与空行）。刻意不做语义分析——这个指标要的是"一屏读不完的东西
有多少"，注释算在内是对的（注释多的长函数仍然要分段）。

用法：
    python3 tools/check_function_length.py              # 比对基线
    python3 tools/check_function_length.py --list       # 列清单（行数 文件:行号 签名）
    python3 tools/check_function_length.py --top 20     # 只看最长的 20 个
    python3 tools/check_function_length.py --update-baseline   # 收紧基线（拆完再跑）
    python3 tools/check_function_length.py --selftest   # 自证：违规必抓 / 合规必放
"""

import argparse
import os
import re
import subprocess
import sys
import tempfile

# 扫描范围：后端全部 Go 代码（internal/ 与 cmd/ 与 seed/）。
# 不含 frontend-react（那是 TS，另有 tsc/eslint 口径）。
SCAN_DIRS = ("internal", "cmd", "seed")
BASELINE_FILE = ".longfunc_baseline"
DEFAULT_THRESHOLD = 150
FUNC_RE = re.compile(r"^func(\s|\()")  # `func name(` / `func (r *T) name(` / `func(`

# 有意豁免清单（**逐条写理由，不是"太长就算了"**）。
# 判据：一个长函数该不该拆，看的不是行数而是**认知负荷**——
# 有没有"同时持有十几个可变局部变量 + 多条分支各自读写不同子集"这种交叉。
# 下面四条的共同形态是**线性装配表**：一句接一句做同类的事，没有分支交叉、没有共享可变状态，
# 读它像读清单；把它们拆成 12 个函数只会得到"12 个参数互传的样板 + 装配顺序散落在两处"
# ——而装配**顺序**在本仓是带语义的（路由注册时序＝鉴权边界、ticker 前置任务＝启动依赖）。
# 所以这里豁免的是"顺序敏感的线性序列"，同时用静态锁保证它们不被当成"反正豁免"的垃圾桶：
# main.go 的停机/指标/后台循环结构另有 test_all G-6 §3.11 五向锁在守。
EXEMPT = {
    # 相对路径 -> (函数名首词, 理由)
    "cmd/server/main.go": (
        {"main", "registerRoutes"},
        "启动装配序列：配置→DB→seed→缓存→引擎→17 处后台 ticker→中间件链→路由→监听。"
        "顺序本身带语义（路由注册时序＝鉴权边界、后台任务的前置运行＝启动依赖），"
        "拆散后顺序散在多个文件反而更易错；停机结构由 test_all G-6 §3.11 静态锁守",
    ),
    "seed/seed_kb.go": (
        {"seedTemplates"},
        "种子数据字面量表（一批模板逐条 Add），无分支交叉；拆成多函数只是把一张表切成多张",
    ),
    "config/config.go": (
        {"LoadConfig"},
        "环境变量→结构字段的逐字段装载表：每行同形（读 env、给默认、赋值），"
        "集中一处便于与 .env.example 逐条对照；拆出去会让「某个键有没有默认值」更难核",
    ),
}


def func_name_of(decl_line: str) -> str:
    """从 `func (r *T) Name(...)` / `func Name(...)` 里取出函数名（取不到就返回空串）。"""
    m = re.match(r"^func\s+(?:\([^)]*\)\s+)?([A-Za-z_][\w]*)", decl_line)
    return m.group(1) if m else ""


def is_exempt(rel_path: str, decl_line: str) -> bool:
    """该函数是否在有意豁免清单里（按相对路径 + 函数名匹配）。

    豁免**按函数名而不是按文件**：同一个 main.go 里如果新写一个 300 行的业务函数，
    它照样得红——只有清单上点名的那几个"线性装配序列"免。
    """
    entry = EXEMPT.get(rel_path)
    if not entry:
        return False
    names = entry[0]
    return func_name_of(decl_line) in names


def is_repo_root(path: str) -> bool:
    """判断给定目录是否仓库根（有 go.mod）。"""
    return os.path.isfile(os.path.join(path, "go.mod"))


def find_repo_root() -> str:
    """从脚本位置向上找仓库根，保证从任意 cwd 调用都能读到基线。"""
    cur = os.path.dirname(os.path.abspath(__file__))
    for _ in range(8):
        if is_repo_root(cur):
            return cur
        parent = os.path.dirname(cur)
        if parent == cur:
            break
    return os.getcwd()


def go_files(root: str):
    """产出发自 root 的非测试 .go 文件路径（跳过 vendor / 生成物 / 前端）。"""
    skip_parts = {".git", "vendor", "node_modules", "frontend-react", "dist"}
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in skip_parts and not d.startswith(".")]
        for fn in filenames:
            if not fn.endswith(".go") or fn.endswith("_test.go"):
                continue
            if fn.endswith((".pb.go", "_generated.go")):
                continue
            yield os.path.join(dirpath, fn)


def walk_functions(root: str):
    """逐函数产出 (相对路径, 起始行号, 声明首行, 函数总行数)。

    为什么单拎这一层：长函数判定（long_functions）与"扫描面是否真的扫到了东西"
    （selftest 的真实仓库体检）用的是同一份解析结果。若在两处各写一遍遍历，
    解析器和语言形态失配时（func 换缩进、花括号风格变化）计数会一起变成 0，
    于是"门禁全绿"和"检查器坏了"看起来一模一样——这正是本仓响应体契约检查器首版
    栽过的形态（解析出零字段却报"无差异"）。
    """
    for path in go_files(root):
        try:
            with open(path, encoding="utf-8") as fh:
                lines = fh.read().split("\n")
        except (OSError, UnicodeDecodeError):
            continue
        rel = os.path.relpath(path, root)
        i = 0
        while i < len(lines):
            line = lines[i]
            if not FUNC_RE.match(line):
                i += 1
                continue
            # 多行参数签名：往下找到带 '{' 的那行为止
            j = i
            while j < len(lines) and "{" not in lines[j]:
                j += 1
            if j >= len(lines):
                break
            depth = 0
            k = j
            while k < len(lines):
                depth += lines[k].count("{") - lines[k].count("}")
                if depth <= 0:
                    break
                k += 1
            yield (rel, i + 1, line.strip()[:100], k - i + 1)
            i = k + 1


def scan_stats(root: str):
    """产出 (扫描到的 .go 文件数, 解析出的函数总数)，供自证体检"扫描面非空"用。

    判据选的是**文件数（枚举所得）× 函数数（解析所得）**这一对，而不是**超长函数数**：
    FIX-G（2026-09-28）把本仓 >150 行函数清零之后，"真实仓库里一个超长函数都没找到"
    从"检查器坏了"的信号变成了**正当终态**，老自证因此反而判红。文件数与函数数不受债务
    有无影响，才配当"解析器有没有在干活"的判据；且文件数必须由 go_files **枚举**得到
    （若也解析出来数，解析一坏两项一起归零，就分不清"目录没扫到"还是"正则失配"）。
    """
    n_files = len(list(go_files(root)))
    n_funcs = sum(1 for _ in walk_functions(root))
    return n_files, n_funcs


def long_functions(root: str, threshold: int):
    """返回 [(行数, 相对路径, 起始行号, 签名首行)]，按行数降序。"""
    out = [(length, rel, start, sig)
           for (rel, start, sig, length) in walk_functions(root)
           if length > threshold and not is_exempt(rel, sig)]
    out.sort(reverse=True)
    return out


def read_baseline(root: str):
    path = os.path.join(root, BASELINE_FILE)
    if not os.path.isfile(path):
        return None
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            raw = raw.strip()
            if raw and not raw.startswith("#"):
                try:
                    return int(raw)
                except ValueError:
                    return None
    return None


def write_baseline(root: str, value: int, threshold: int):
    path = os.path.join(root, BASELINE_FILE)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(
            "# 超长函数（> {th} 行）个数棘轮基线，由 tools/check_function_length.py 维护。\n"
            "# 口径：只准降不准升。拆完一批后用 --update-baseline 收紧。\n"
            "{val}\n".format(th=threshold, val=value)
        )


def selftest() -> int:
    """自证检查器本身有牙齿：违规必被抓、合规必放行、真实扫描面解析得出函数。

    为什么要这一节：解析器一旦和语言形态失配（例如 `func` 换了缩进、花括号风格变化），
    它会**静默返回空清单**，于是门禁永远绿。历史上同类脚本栽过一次（响应体契约检查器
    首版解析出零字段却报"无差异"）。所以四件事都要证明：
      ① 造一个 200 行函数 → 必须报出来；
      ② 造一个 30 行函数 → 必须不报；
      ③ 豁免必须"点名才免"（同文件未点名的 160 行函数照样红）、_test.go 必须排除；
      ④ 对真实仓库解析出的**文件数与函数数**非空（不是"超长函数非空"——
         FIX-G 把债务清零后超长数合法为 0，判据绑在它上面会让绿终态自证变红）。
    """
    fails = []
    with tempfile.TemporaryDirectory() as td:
        big = ["package p", "", "func Big() {"]
        big += ["\tx := 1 // pad %d" % n for n in range(198)]
        big += ["}", "", "func Small() {", "\ty := 2", "}"]
        with open(os.path.join(td, "big.go"), "w", encoding="utf-8") as fh:
            fh.write("\n".join(big) + "\n")
        hits = long_functions(td, 150)
        names = [sig for (_, _, _, sig) in hits]
        if not any(sig.startswith("func Big(") for sig in names):
            fails.append("反证失败：200 行函数没被抓到 -> %r" % (names,))
        if any(sig.startswith("func Small(") for sig in names):
            fails.append("误报：30 行函数被判超长")
        if len(hits) != 1:
            fails.append("计数异常：合成目录应恰有 1 个超长函数，实为 %d" % len(hits))
        # 豁免清单必须"点名才免"：同文件里换个函数名照样要红
        with open(os.path.join(td, "main.go"), "w", encoding="utf-8") as fh:
            body = ["package main", "", "func main() {"] + ["\tx := 1"] * 160 + ["}",
                    "", "func someBusinessFunc() {"] + ["\ty := 2"] * 160 + ["}", ""]
            fh.write("\n".join(body) + "\n")
        EXEMPT["main.go"] = ({"main"}, "自证用临时豁免")
        try:
            names = [sig for (_, _, _, sig) in long_functions(td, 150)]
            if not any(sig.startswith("func someBusinessFunc(") for sig in names):
                fails.append("豁免失控：同文件里未点名的 160 行业务函数没被抓到 -> %r" % (names,))
            if any(sig.startswith("func main(") for sig in names):
                fails.append("豁免未生效：点名的 main 仍被计入")
        finally:
            EXEMPT.pop("main.go", None)
        # 测试文件必须排除在外（否则拆分会被测试用例的长表驱动函数绑死）
        baseline_n = len(long_functions(td, 150))  # 加 _test.go 之前的个数，作对照基数
        with open(os.path.join(td, "big_test.go"), "w", encoding="utf-8") as fh:
            fh.write("\n".join(["package p", "", "func TestBig() {"] + ["\tz := 1"] * 200 + ["}"]) + "\n")
        after_n = len(long_functions(td, 150))
        if after_n != baseline_n:
            fails.append("_test.go 未被排除（加一个 200 行测试函数后计数应不变：%d -> %d）" % (baseline_n, after_n))
    root = find_repo_root()
    real = long_functions(root, DEFAULT_THRESHOLD)
    n_files, n_funcs = scan_stats(root)
    # ③ 真实仓库体检：判"解析器有没有在干活"，不判"债务在不在场"。
    #    阈值取 200 个函数——本仓实测四位数（internal/cmd/seed 的 Go 文件数百个），
    #    而"解析失配 → 函数数为 0"会被这条当场抓住；FIX-G 收口后超长函数=0 是正当终态，
    #    故此处**不得**改回"超长函数必须 > 0"（那等于把门禁的自证绑在被消除的缺陷上）。
    if n_files < 50:
        fails.append("扫描面异常：只扫到 %d 个 .go 文件，检查器可能没在扫 (internal|cmd|seed)" % n_files)
    if n_funcs < 200:
        fails.append("解析面异常：只解析出 %d 个函数（文件却有 %d 个），func 声明正则与语言形态可能已失配" % (n_funcs, n_files))
    if fails:
        print("  FAIL  selftest：超长函数检查器自证未过")
        for f in fails:
            print("        - " + f)
        return 1
    print("  PASS  selftest：200 行函数被抓到 / 30 行放行 / _test.go 排除 / "
          "真实扫描面 %d 文件·%d 函数（超长 %d 条，基线另判）" % (n_files, n_funcs, len(real)))
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="超长函数棘轮门禁（FIX-G）")
    ap.add_argument("--threshold", type=int, default=DEFAULT_THRESHOLD)
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--top", type=int, default=0)
    ap.add_argument("--update-baseline", action="store_true")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args()

    if args.selftest:
        return selftest()

    root = find_repo_root()
    hits = long_functions(root, args.threshold)
    n = len(hits)

    if args.list or args.top:
        shown = hits[: args.top] if args.top else hits
        print("  > %d 行的函数：共 %d 个" % (args.threshold, n))
        for length, rel, ln, sig in shown:
            print("    %4d 行  %s:%d  %s" % (length, rel, ln, sig))
        if not args.list and args.top:
            print("    （--top %d，完整清单用 --list）" % args.top)

    if args.update_baseline:
        write_baseline(root, n, args.threshold)
        print("  基线已更新为 %d（%s）" % (n, os.path.join(root, BASELINE_FILE)))
        return 0

    base = read_baseline(root)
    if base is None:
        print("  FAIL  缺基线文件 %s（首次落地请跑 --update-baseline）" % BASELINE_FILE)
        return 1
    if n > base:
        print(
            "  FAIL  超长函数（> %d 行）个数 %d > 基线 %d —— 棘轮只准降不准升。"
            % (args.threshold, n, base)
        )
        print(
            "        新写的长函数请一开始就分段；若这是"
            "把已有函数拆小了之后的计数，请用 --update-baseline 收紧基线。"
        )
        for length, rel, ln, sig in hits[:5]:
            print("          %4d 行  %s:%d  %s" % (length, rel, ln, sig))
        return 1
    print("  PASS  超长函数（> %d 行）%d 个 ≤ 基线 %d%s" % (args.threshold, n, base, "（本批已收紧）" if n < base else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())

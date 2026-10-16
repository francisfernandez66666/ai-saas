#!/usr/bin/env python3
# P2-9 前端契约泛型覆盖率棘轮门禁。
#
# 背景：frontend-react 请求层（src/lib/api.ts）的 AUTH/apiFetch/PUBLIC/OPEN 调用点，
# 只有少数带了 `AUTH<ApiEnvelope<'/path'>>` 泛型，绝大多数是 `AUTH(url)` 裸调——
# 响应信封退化为 any，前端拿到的 data 无类型保护（契约漂移静默通过）。
#
# 口径（棘轮，只升不降）：
#   - 统计 src 下（排除 lib/api.ts 定义处）所有 AUTH/apiFetch/PUBLIC/OPEN( 调用点 = total；
#   - 其中带 `<泛型>` 的 = typed；
#   - typed 个数只准升不准降（基线 .api_generic_baseline）。删泛型即红，加泛型放行。
#   用"个数"而非"百分比"做棘轮：百分比会因新增未类型化调用点被稀释而假性下降，
#   用绝对 typed 个数更稳（新增调用点不扣分，删泛型才扣分）。
#
# 为什么不是"必须全部类型化"：给路径补 ApiEnvelope 泛型要有真实类型收益，前提是该
# 路径已登记进 src/types/api.d.ts 的 RouteData 映射；否则退化为 ApiResp，套了也白套。
# 故本门禁只强制"已类型化的不许退回"，把"继续提升"交给人审 + api.d.ts 扩充（持续工作）。
import os
import re
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "frontend-react", "src")
BASELINE = os.path.join(ROOT, ".api_generic_baseline")

TYPED_RE = re.compile(r"\b(?:AUTH|apiFetch|PUBLIC|OPEN)\s*<")
CALL_RE = re.compile(r"\b(?:AUTH|apiFetch|PUBLIC|OPEN)\s*\(")


def measure(src_dir):
    """返回 (total, typed, coverage%)。排除 lib/api.ts（请求层定义处，非调用点）。"""
    total = typed = 0
    for base, _dirs, files in os.walk(src_dir):
        for fn in files:
            if not (fn.endswith(".ts") or fn.endswith(".tsx")):
                continue
            path = os.path.join(base, fn)
            rel = os.path.relpath(path, src_dir)
            if rel.replace(os.sep, "/") == "lib/api.ts":
                continue
            try:
                s = open(path, encoding="utf-8").read()
            except Exception:
                continue
            total += len(CALL_RE.findall(s))
            typed += len(TYPED_RE.findall(s))
    cov = (100.0 * typed / total) if total else 0.0
    return total, typed, cov


def read_baseline():
    try:
        with open(BASELINE, encoding="utf-8") as f:
            return int(f.read().strip())
    except Exception:
        return None


def selftest():
    """双向自证：构造已知 typed/untyped 的样本，确认计数脚本能正确区分（防永远通过）。"""
    ok = True
    with tempfile.TemporaryDirectory() as d:
        # 命中样本：2 typed + 1 untyped
        hit = os.path.join(d, "hit.tsx")
        with open(hit, "w", encoding="utf-8") as f:
            f.write("const a = AUTH<ApiEnvelope<'/x'>>(u);\n"
                    "const b = apiFetch<ApiEnvelope<'/y'>>(u);\n"
                    "const c = AUTH(u);\n")
        # 放行样本：全 untyped
        rel = os.path.join(d, "rel.tsx")
        with open(rel, "w", encoding="utf-8") as f:
            f.write("const a = AUTH(u);\nconst b = PUBLIC(u);\n")
        t1, ty1, _ = measure(d)
        # 命中目录：total=3 typed=2
        if not (t1 == 3 and ty1 == 2):
            print(f"[selftest][FAIL] 命中样本计数错误: total={t1} typed={ty1} (want 3/2)")
            ok = False
        else:
            print(f"[selftest][OK] 命中样本 total={t1} typed={ty1}")
        # 验证 lib/api.ts 被排除：把 hit 内容塞进 lib/api.ts 应计 0
        libdir = os.path.join(d, "lib")
        os.makedirs(libdir, exist_ok=True)
        with open(os.path.join(libdir, "api.ts"), "w", encoding="utf-8") as f:
            f.write("const a = AUTH<ApiEnvelope<'/x'>>(u);\nconst b = AUTH(u);\n")
        t2, ty2, _ = measure(d)
        if not (t2 == 3 and ty2 == 2):
            print(f"[selftest][FAIL] lib/api.ts 未正确排除: total={t2} typed={ty2} (want 3/2)")
            ok = False
        else:
            print(f"[selftest][OK] lib/api.ts 定义处已排除")
    print("[selftest] " + ("PASS" if ok else "FAIL"))
    return 0 if ok else 1


def main():
    if "--selftest" in sys.argv:
        return selftest()
    total, typed, cov = measure(SRC)
    base = read_baseline()
    print(f"[P2-9] 契约泛型覆盖: typed={typed} total={total} coverage={cov:.1f}% 基线={base}")
    if base is None:
        print("[P2-9][FAIL] 缺少 .api_generic_baseline，请先落当前 typed 个数为基线")
        return 1
    if typed < base:
        print(f"[P2-9][FAIL] typed 泛型调用点从 {base} 降到 {typed}（删除泛型=契约保护回退，棘轮拦截）")
        return 1
    print(f"[P2-9][OK] typed={typed} ≥ 基线 {base}（只升不降）")
    return 0


if __name__ == "__main__":
    sys.exit(main())

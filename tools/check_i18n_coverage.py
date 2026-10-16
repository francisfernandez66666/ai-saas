#!/usr/bin/env python3
"""i18n 硬编码中文棘轮门禁（P2-14，2026-10-14）。

背景：全站文案中文化是存量工程（数十个页面的硬编码中文），一次性抽干不现实。
本门禁把"当前硬编码中文数量"冻成基线，**只许降不许升**——每迁移一个界面就下调基线，
新增硬编码中文（回归）立刻红灯。与 tools/check_api_generic_coverage.py 同族设计：
selftest 双向自证 + 基线棘轮。

计数口径（刻意从窄，避免误伤）：
  - 范围：frontend-react/src/**/*.tsx（JSX 所在的界面层）；
  - 计入：字符串/模板字面量（'...' / "..." / `...`）中含 CJK 表意文字（U+4E00-9FFF）的；
         以及 JSX 文本节点（标签之间）含 CJK 表意文字的；
  - 不计入：注释（行/块）、正则字面量、CJK 标点单独出现（如全角空格 U+3000 分隔符）、
           .ts 非界面层（api.ts 的 toast 文案表等属数据层，迁移口径另行排期）。
退出码：0 = 通过（计数 <= 基线）；1 = 超标或自证失败。
"""
import os
import re
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), '..'))
SRC = os.path.join(ROOT, 'frontend-react', 'src')
BASELINE_FILE = os.path.join(ROOT, '.i18n_cjk_baseline')

# CJK 表意文字（不含标点/全角空格——那些单独出现不算"文案"）
CJK = re.compile(r'[\u4e00-\u9fff]')


def scan_source(src: str):
    """单文件扫描。返回 (字符串字面量命中数, JSX 文本命中数)。

    迷你词法器：逐字符过状态机，先剔除注释（行/块，字符串内的 // 不算注释），
    再在剩余文本里数字符串字面量与 JSX 文本节点的 CJK 命中。
    为什么不用正则直接删注释：字符串里合法出现 '//'（URL）与 '/*'，
    裸正则删注释会把字符串内容啃掉，计数失真。
    """
    n = len(src)
    i = 0
    out = []  # (kind, text) kind: 'str' | 'code'
    buf = []
    while i < n:
        c = src[i]
        nxt = src[i + 1] if i + 1 < n else ''
        if c == '/' and nxt == '/':  # 行注释
            j = src.find('\n', i)
            i = n if j == -1 else j
            continue
        if c == '/' and nxt == '*':  # 块注释
            j = src.find('*/', i + 2)
            i = n if j == -1 else j + 2
            continue
        if c in ('"', "'", '`'):
            quote = c
            j = i + 1
            while j < n:
                if src[j] == '\\':
                    j += 2
                    continue
                if src[j] == quote:
                    break
                j += 1
            out.append(('str', src[i + 1:j]))
            i = j + 1
            continue
        buf.append(c)
        i += 1
    code = ''.join(buf)

    str_hits = sum(1 for _, t in out if CJK.search(t))
    # JSX 文本：> 与 < 之间、不含 { 表达式/换行的片段，含 CJK 表意文字即命中
    jsx_hits = len(re.findall(r'>([^<>{}\n]*[\u4e00-\u9fff][^<>{}\n]*)<', code))
    return str_hits, jsx_hits


def measure():
    """全量测量。返回 (总数, {文件: 数})"""
    total = 0
    per_file = {}
    for dirpath, _dirs, files in os.walk(SRC):
        if '__tests__' in dirpath.split(os.sep):
            continue  # 测试夹具里的中文是断言样本，不是界面文案
        for f in files:
            if not f.endswith('.tsx'):
                continue
            p = os.path.join(dirpath, f)
            with open(p, encoding='utf-8') as fh:
                s, j = scan_source(fh.read())
            if s + j:
                per_file[os.path.relpath(p, ROOT)] = s + j
                total += s + j
    return total, per_file


def read_baseline():
    try:
        with open(BASELINE_FILE, encoding='utf-8') as fh:
            return int(fh.read().strip())
    except (OSError, ValueError):
        return None


def selftest():
    """双向自证：已知应命中 + 已知应放行。任一不符即退出 1。"""
    cases = [
        # (源码, 期望字符串命中, 期望 JSX 命中, 说明)
        ("const a = '中文文案'", 1, 0, '单引号字面量'),
        ('const b = "你好"', 1, 0, '双引号字面量'),
        ('const c = `共 ${n} 条`', 1, 0, '模板字面量'),
        ('<div>标签文本</div>', 0, 1, 'JSX 文本节点'),
        ('<Button placeholder="占位符" />', 1, 0, 'JSX 属性字符串'),
        ('// 这是中文注释', 0, 0, '行注释不计入'),
        ('/* 块注释中文 */', 0, 0, '块注释不计入'),
        ("const u = 'https://example.com/a'", 0, 0, 'URL 字符串里的 // 不是注释'),
        ("const sep = '，'", 0, 0, '纯 CJK 标点不算文案'),
        ('const sp = <>　</>', 0, 0, '全角空格单独出现不算'),
        ('const n = 42', 0, 0, '无中文'),
    ]
    ok = True
    for src, exp_s, exp_j, why in cases:
        s, j = scan_source(src)
        if (s, j) != (exp_s, exp_j):
            print(f'[selftest FAIL] {why}: 期望 ({exp_s},{exp_j}) 实际 ({s},{j}) | {src}')
            ok = False
    print(f'[selftest] {"PASS" if ok else "FAIL"}（{len(cases)} 例双向）')
    return ok


def main():
    if not selftest():
        return 1
    total, per_file = measure()
    base = read_baseline()
    if base is None:
        print(f'[i18n-cjk] 基线文件缺失：{BASELINE_FILE}')
        return 1
    top = sorted(per_file.items(), key=lambda kv: -kv[1])[:10]
    print(f'[i18n-cjk] 当前硬编码中文 = {total}，基线 = {base}')
    for p, c in top:
        print(f'  {c:4d}  {p}')
    if total > base:
        print(f'[i18n-cjk] FAIL：{total} > 基线 {base}（新增硬编码中文 {total - base} 处；'
              f'迁移界面后请同步下调 .i18n_cjk_baseline）')
        return 1
    print(f'[i18n-cjk] PASS（{total} <= {base}）')
    return 0


if __name__ == '__main__':
    sys.exit(main())

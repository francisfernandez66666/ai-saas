#!/usr/bin/env python3
"""i18n 词典重复键门禁（P2-14，2026-10-15）。

背景：词典是 `{'a.b': '值', ...} as const` 的对象字面量。JS/TS 对重复键**不报错**，
语义是"后者覆盖前者"；而 Object.keys() 天生去重，于是 dictionaries.test.ts 那四项
对齐断言（键集合 / 空值 / 占位符 / 语言齐全）一条都抓不到重复。

本次迁移实测踩中这个盲区：新增 common.search 时与既有键重名，把全站"搜索"静默
改成了"查询"——所有引用该键的界面文案一夜之间变了味，且测试全绿。tsc 在 strict 下
会报 TS1117，但门禁不能只依赖"顺手跑一次 tsc"，故补本脚本回到**源码层**扫描。

为什么不在 vitest 里用 fs 读源码：本仓静态门禁一贯是 python/bash（20 项，各带
命中-放行双向自证），与 test_all.sh 阶段零同族，口径一致、不依赖 ESM/fs 环境。

退出码：0 = 无重复；1 = 存在重复键或自证失败。
"""
import os
import re
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), '..'))
DICT_DIR = os.path.join(ROOT, 'frontend-react', 'src', 'i18n', 'dictionaries')
FILES = ['zh-CN.ts', 'en-US.ts']

# 词典条目的缩进固定为两空格：'  key': '值'。行首 ^ 锚定避免误伤注释/嵌套对象里的同名字段。
KEY_RE = re.compile(r"^  '([^']+)':", re.M)


def find_dups(src: str):
    """返回源码里重复出现的键（按首次出现顺序去重）。"""
    keys = KEY_RE.findall(src)
    seen, dups = set(), []
    for k in keys:
        if k in seen and k not in dups:
            dups.append(k)
        seen.add(k)
    return dups


def selftest():
    """双向自证：已知应命中（重复） + 已知应放行（不重复）。"""
    cases = [
        # (源码, 期望重复键列表, 说明)
        ("export const zhCN = {\n  'a.b': '一',\n  'a.b': '二',\n}", ['a.b'], '完全重复必被点名'),
        ("export const zhCN = {\n  'a.b': '一',\n  'a.c': '二',\n}", [], '不重复必放行'),
        # 注释里的同名字样不算（行首 ^  + 两空格缩进锚定，注释行形如 "// 'a.b' ..."）
        ("export const zhCN = {\n  // 'a.b' 见上文\n  'a.b': '一',\n}", [], '注释里的同名不计入'),
        # 嵌套对象里的同名字段（缩进不是两空格）不计入，避免误伤
        ("export const zhCN = {\n  'a.b': '一',\n  nested: {\n    'a.b': 'x',\n  },\n}", [], '嵌套同名字段不计入'),
        ("", [], '空源码放行'),
    ]
    ok = True
    for src, exp, why in cases:
        got = find_dups(src)
        if got != exp:
            print(f'[selftest FAIL] {why}: 期望 {exp} 实际 {got}')
            ok = False
    print(f'[selftest] {"PASS" if ok else "FAIL"}（{len(cases)} 例双向）')
    return ok


def main():
    if not selftest():
        return 1
    bad = False
    for f in FILES:
        p = os.path.join(DICT_DIR, f)
        if not os.path.exists(p):
            print(f'[i18n-dup] 词典缺失：{p}')
            return 1
        with open(p, encoding='utf-8') as fh:
            dups = find_dups(fh.read())
        if dups:
            print(f'[i18n-dup] FAIL：{f} 存在重复键 {len(dups)} 个 -> {", ".join(dups)}')
            print('           重复键会静默覆盖前值（后者胜），Object.keys 去重导致单测抓不到。')
            bad = True
    if bad:
        return 1
    print(f'[i18n-dup] PASS（{len(FILES)} 个词典，无重复键）')
    return 0


if __name__ == '__main__':
    sys.exit(main())

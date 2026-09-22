#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""扫描 internal/ 与 cmd/ 中「裸 db.DB 用法」并按 2026-09-22 审计口径分类（G-12 棘轮门禁）。
#
# 背景（PLAN_FIX_2026-09-22_AUDIT.md §五 P2-6）：原 test_all.sh 的 G-12 内联段两分支均硬编码
# verdict 0，实测报 480 处仍 PASS——一道结构上不可能失败的「假绿门禁」。本脚本把 G-12 改造成
# 「显式白名单 + 基线棘轮」：把每一处裸 db.DB 用法归入合法类别（A–F + G12 豁免），凡不属任何
# 合法类别的即「白名单外计数」，该计数只允许下降（只降不升），新增违规即 FAIL。
#
# 扫描口径（与审计复核口径一致，刻意保守以避免误报）：
#   - 仅扫描 internal/ 与 cmd/ 下的 *.go，跳过 *_test.go（测试文件不计入）；
#   - 候选行：包含字面量 "db.DB." 的非纯注释行（行首为 // 的注释整行跳过；
#     行内 // g12:platform 豁免标记仍参与 G 类判定）；
#   - 不把 "x := db.DB.Where(...)" 这类链式赋值当 handle 捕获排除——它本身就是裸用，必须计数。
#
# 分类（白名单）口径：
#   A 行内显式含 tenant_id 条件（查询自带租户约束）；
#   B 主键直取：First|Take|Last(&x, id) 形式（第二参数为非字符串字面量的主键）；
#   C 取 *sql.DB 池句柄：db.DB.DB()；
#   D .Transaction( 事务包装（内部通常显式 SetTenantRLS）；
#   E 平台级表/跨租户聚合：白名单内的平台资产模型/表（IndustryPack/SystemConfig/model.Tenant/
#     TenantAuditLog/TenantUser/PaymentPlan/model.Plan/TenantQuota/GlobalConfig/Dict*/Sys*/Migration 等）；
#   F middleware 鉴权与租户解析前置路径：middleware/ 下的 tenant.go/auth.go/openapi_auth.go/org.go
#     （此刻租户上下文尚未建立，必须全表查，设计如此）；
#   G 行内 // g12:platform 豁免标记（沿用 D6 护栏既有豁免约定）。
# 不属于上述任何一类 → 白名单外（需人工逐处确认，是门禁真正盯着的数字）。
#
# 输出：先打印各分类计数，最后一行固定输出「白名单外计数 N」，便于 shell 用 tail -1 取值。
#
# 用法：
#   python3 tools/classify_bare_db.py                  # 打印分类 + 白名单外计数
#   python3 tools/classify_bare_db.py --selftest       # 双向自证（应命中 / 应放行）
"""

import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASELINE_FILE = os.path.join(ROOT, ".bare_db_baseline")

# 平台级（非租户作用域）模型/表名白名单：这些表的查询本就跨租户或属平台资产。
PLATFORM_WHITELIST = [
    "IndustryPack", "SystemConfig", "model.Tenant", "TenantAuditLog",
    "TenantUser", "PaymentPlan", "model.Plan", "TenantQuota",
    "GlobalConfig", "DictItem", "DictType", "model.Dict",
    "SysRole", "SysMenu", "model.Sys", "Migration",
]

# F 类：middleware 鉴权与租户解析前置路径文件
MIDDLEWARE_FILES = ("tenant.go", "auth.go", "openapi_auth.go", "org.go")

# 候选行：包含字面量 "db.DB."（非纯注释行）
CANDIDATE_RE = re.compile(r"db\.DB\.")

# B 类：First|Take|Last 且第二参数为非字符串字面量（即主键直取形）
PK_FETCH_RE = re.compile(r"\.(First|Take|Last)\([^)]*,\s*[^\"']")


def is_candidate(line):
    """行是否进入候选（含 db.DB. 且非纯注释行）。"""
    if "db.DB." not in line:
        return False
    if line.strip().startswith("//"):
        return False
    return True


def classify(relpath, line):
    """返回类别字母 A–G；不属于任何白名单类返回 None（即白名单外）。"""
    s = line.strip()
    # G 豁免标记优先（行内 // g12:platform）
    if "g12:platform" in s:
        return "G"
    # F middleware 前置路径
    norm = relpath.replace("\\", "/")
    if "/middleware/" in norm and norm.endswith(MIDDLEWARE_FILES):
        return "F"
    # C 池句柄
    if "db.DB.DB(" in s:
        return "C"
    # D 事务包装
    if ".Transaction(" in s:
        return "D"
    # B 主键直取
    if PK_FETCH_RE.search(s):
        return "B"
    # A 行内显式 tenant_id 条件
    if "tenant_id" in s:
        return "A"
    # E 平台级表/模型白名单
    if any(name in s for name in PLATFORM_WHITELIST):
        return "E"
    return None


def scan():
    """扫描全仓，返回 (分类计数Counter, 白名单外计数, 候选总行数)。"""
    from collections import Counter
    counts = Counter()
    outer = 0
    total = 0
    for base in ("internal", "cmd"):
        root = os.path.join(ROOT, base)
        if not os.path.isdir(root):
            continue
        for dirpath, dirnames, filenames in os.walk(root):
            for fn in filenames:
                if not fn.endswith(".go") or fn.endswith("_test.go"):
                    continue
                path = os.path.join(dirpath, fn)
                rel = os.path.relpath(path, ROOT)
                try:
                    lines = open(path, encoding="utf-8", errors="ignore").read().splitlines()
                except OSError:
                    continue
                for line in lines:
                    if not is_candidate(line):
                        continue
                    total += 1
                    cat = classify(rel, line)
                    if cat is None:
                        outer += 1
                    else:
                        counts[cat] += 1
    return counts, outer, total


def load_baseline():
    """读取 .bare_db_baseline（单行整数），缺失视为 0（最严）。"""
    if not os.path.exists(BASELINE_FILE):
        return 0
    for line in open(BASELINE_FILE, encoding="utf-8"):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        try:
            return int(line)
        except ValueError:
            continue
    return 0


def main():
    args = set(sys.argv[1:])
    if "--selftest" in args:
        return selftest()

    counts, outer, total = scan()
    print("==== 裸 db.DB 用法分类（G-12 棘轮）====")
    print("  候选总行数: %d" % total)
    for k in list("ABCDEF"):
        print("  类别 %s（白名单）: %d" % (k, counts.get(k, 0)))
    print("  类别 G（g12:platform 豁免）: %d" % counts.get("G", 0))
    print("  白名单外计数: %d" % outer)
    # 最后一行：便于 shell 用 tail -1 取值
    print("白名单外计数 %d" % outer)
    return 0


def selftest():
    """双向自证：一组应命中白名单外，一组应放行。
    防止「假绿门禁」重演——分类逻辑必须能抓住真实违规、也能正确放行合法用法。"""
    # 组一：应命中白名单外（计数为 1，不属 A–G 任一）
    must_outer = [
        '    db.DB.Model(&model.Customer{}).Where("id = ?", 1).Find(&c)',
    ]
    # 组二：应放行（不计入白名单外）
    must_pass = [
        # A：行内显式 tenant_id 条件
        '    db.DB.Model(&model.X{}).Where("tenant_id = ? AND status = ?", tid, 1).Find(&x)',
        # C：取 *sql.DB 池句柄
        '    pool := db.DB.DB()',
        # D：事务包装
        '    err := db.DB.Transaction(func(tx *gorm.DB) error { ... })',
        # B：主键直取 First(&x, id)
        '    db.DB.First(&u, userID)',
        # G：行内 g12:platform 豁免
        '    db.DB.Model(&model.Y{}).Find(&y) // g12:platform',
        # F：middleware 前置路径（tenant.go）
        '    db.DB.Where("token = ?", t).First(&sess)',
    ]
    fail = False
    for s in must_outer:
        if classify("internal/service/foo.go", s) is not None:
            print("  FAIL 自证(应命中白名单外却被判白名单): %s" % s.strip())
            fail = True
    # 注意：F 类样本依赖「middleware 文件路径」才成立，不能进以 service 路径
    # 跑的通用放行断言——单独在下方按真实路径断言（selftest 最初版本曾把
    # must_pass[-1] 混入本循环导致自证永远失败，已修正）。
    for s in must_pass[:-1]:
        if classify("internal/service/foo.go", s) is None:
            print("  FAIL 自证(应放行却被计入白名单外): %s" % s.strip())
            fail = True
    # middleware 行必须判 F（即便内容看似违规）
    if classify("internal/middleware/tenant.go", must_pass[-1]) != "F":
        print("  FAIL 自证(middleware 行未判 F)")
        fail = True
    if fail:
        print("  FAIL 自证未通过")
        return 1
    print("  PASS 自证：能命中真实违规（白名单外），也能正确放行合法用法（A/C/D/B/G/F）")
    return 0


if __name__ == "__main__":
    sys.exit(main())

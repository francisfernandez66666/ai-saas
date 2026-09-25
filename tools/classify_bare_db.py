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
#   A 语句内含 tenant_id 比较条件（= / <> / != / IN / IS；查询自带租户约束。链式换行按整条语句判定，见 statement_span）；
#   B 主键直取：First|Take|Last(&x, id) 形式（第二参数为非字符串字面量的主键）；
#   C 取 *sql.DB 池句柄：db.DB.DB()；
#   D .Transaction( 事务包装（内部通常显式 SetTenantRLS）；
#   E 平台级表/跨租户聚合：白名单内的平台资产模型/表（IndustryPack/SystemConfig/model.Tenant/
#     TenantAuditLog/TenantUser/PaymentPlan/model.Plan/TenantQuota/GlobalConfig/Dict*/Sys*/Migration 等）；
#     2026-09-24 G-15 补消息中心台账 MessageEventRecord/InboxEvent：行由发布端显式盖章 tenant_id，
#     消费端与清理器按 event_id/created_at 定位，跑在无 gin ctx 的消费协程与后台 ticker 里；
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
    # 消息中心两张台账（G-15②，2026-09-24）：行由**发布端**显式盖章 tenant_id 写入，
    # 消费端与清理器都按 event_id / created_at 定位，天然没有"请求租户作用域"可言——
    # 它们跑在消费 goroutine 与后台 ticker 里，根本没有 gin ctx 可带。
    # 反过来，若强行要求 db.RQ(c)，这两条链会因为 ctx 缺失而静默查不到行。
    "MessageEventRecord", "InboxEvent",
]

# F 类：middleware 鉴权与租户解析前置路径文件
MIDDLEWARE_FILES = ("tenant.go", "auth.go", "openapi_auth.go", "org.go")

# 候选行：包含字面量 "db.DB."（非纯注释行）
CANDIDATE_RE = re.compile(r"db\.DB\.")

# B 类：First|Take|Last 且第二参数为非字符串字面量（即主键直取形）
PK_FETCH_RE = re.compile(r"\.(First|Take|Last)\([^)]*,\s*[^\"']")

# A 类：tenant_id 作为**比较条件**出现（= / <> / != / IN / IS）。
# 刻意不认裸 token——`Select("tenant_id, customer_id")` 是投影列，不是租户过滤。
TENANT_COND_RE = re.compile(r"tenant_id\s*(=|<>|!=|\bIN\b|\bIS\b)", re.IGNORECASE)


def is_candidate(line):
    """行是否进入候选（含 db.DB. 且非纯注释行）。"""
    if "db.DB." not in line:
        return False
    if line.strip().startswith("//"):
        return False
    return True


def classify(relpath, line, stmt=None):
    """返回类别字母 A–G；不属于任何白名单类返回 None（即白名单外）。

    stmt：本行所属**整条语句**的拼接文本（缺省等于本行）。2026-09-23 批六新增——
    Go 的链式调用常按行拆开，租户条件往往落在 `db.DB.Model(...)` 的下一行
    （例：`Where("id = ? AND tenant_id = ?", ...)`）。只看单行会把这类合法用法误判为
    白名单外（本轮实测误伤 outcome_backfill / human_takeover / sales_path 共 5 处）。
    拼接只放宽 A（行内租户条件）/B（主键直取）/E（平台表）三种**内容判定**；
    G（行内豁免标记）与 F（middleware 文件）仍按行/文件语义判，避免把豁免扩散到整段。
    """
    s = line.strip()
    body = (stmt if stmt else line).strip()
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
    if PK_FETCH_RE.search(body):
        return "B"
    # A 语句内含显式 tenant_id **比较条件**（= / <> / != / IN / IS）。
    # 为什么不是裸 token：`Select("tenant_id, customer_id")` 也含 tenant_id 字样，
    # 但那是投影列不是过滤条件——跨租户巡检队列正是靠这个区别把自己暴露出来的
    # （批六第一版只判 `"tenant_id" in body`，把 outcome_backfill 的跨租户队列误放行）。
    if TENANT_COND_RE.search(body):
        return "A"
    # E 平台级表/模型白名单
    if any(name in body for name in PLATFORM_WHITELIST):
        return "E"
    return None


def statement_span(lines, idx, max_lines=8):
    """取 lines[idx]（0 基）所属语句的文本：沿**方法链续行**向后拼接，最多 max_lines 行。

    为什么判"上一行以 `.` 结尾"而不是"括号净平衡归零"（后者是本函数的第一版，已废弃）：
    gofmt 拆链式调用时把点号留在行尾（`db.DB.Model(&X{}).` 换行 `.Where(...)`），
    而每一段自身括号都是平衡的——按括号平衡根本拼不到续行，租户条件在下一行的语句
    照样误报（第一版即在此自证用例上 FAIL）。
    只向"下一行"方向拼，且只在行尾是点号时拼，因此绝不会把无关的 `if err != nil {` 卷进来。
    """
    parts = [lines[idx]]
    j = idx + 1
    while parts[-1].rstrip().endswith(".") and j < len(lines) and (j - idx) < max_lines:
        parts.append(lines[j])
        j += 1
    return "\n".join(parts)


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
                for i, line in enumerate(lines):
                    if not is_candidate(line):
                        continue
                    total += 1
                    cat = classify(rel, line, statement_span(lines, i))
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
        # A 类刻意不认裸 token：投影列里有 tenant_id 不等于按租户过滤
        '    db.DB.Select("tenant_id, customer_id").Where("outcome_checked_at IS NULL").Find(&rows)',
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
        # E：消息中心台账（G-15 新入白名单，无请求 ctx 的消费/清理链按 event_id 定位）
        '    q := db.DB.Model(&model.MessageEventRecord{}).Where("event_id = ?", id)',
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
    # 语句拼接（批六新增口径）双向自证：租户条件写在续行 → 必须判 A 放行；
    # 整条语句都没有租户条件 → 即使拼接也必须留在白名单外（防止拼接变成新的绕过口）。
    chain_tenant = [
        '\tif err := db.DB.Model(&model.Conversation{}).',
        '\t\tWhere("id = ? AND tenant_id = ?", conv.ID, conv.TenantID).',
        '\t\tUpdates(map[string]interface{}{"mode": "ai"}).Error; err != nil {',
    ]
    if classify("internal/chatflow/human_takeover.go", chain_tenant[0],
                statement_span(chain_tenant, 0)) != "A":
        print("  FAIL 自证(租户条件在续行的链式语句未判 A)")
        fail = True
    chain_bare = [
        '\tq := db.DB.Model(&model.Customer{}).',
        '\t\tWhere("status = ?", 1).',
        '\t\tFind(&rows)',
    ]
    if classify("internal/service/foo.go", chain_bare[0],
                statement_span(chain_bare, 0)) is not None:
        print("  FAIL 自证(整条语句无租户条件却被判放行)")
        fail = True
    if fail:
        print("  FAIL 自证未通过")
        return 1
    print("  PASS 自证：能命中真实违规（白名单外），也能正确放行合法用法（A/C/D/B/G/F + 续行租户条件）")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
# D6 修复(2026-09-16，AUDIT_DEFECT_VERIFY)：G-12"裸 db.DB"红线由 advisory(464 处恒 WARN 放行)
# 收窄为【精准阻断】——只拦真正危险的模式：
#   db.DB.Create/Save(&x) 且 x 是"含 TenantID 字段的 model 结构体字面量"且字面量未显式设 TenantID。
# 根因：盖章回调依赖 tx.Statement.Context 里的租户（db/tenant_ctx.go），db.DB 无请求 ctx →
# 盖成 tenant_id=0（归属丢失/平台视图泄露）。该事故形态历史上真实发生两次（C7、P1-5 留资线索）。
# 其余 400+ 处裸 db.DB 用法（后台任务显式 Where tenant_id、平台表）属合法形态，不再误报。
# 平台层（tenant_id=0）有意为之的行，在 Create/Save 行尾或字面量首行加 `// g12:platform` 豁免。
# 退出码：0=干净；1=存在违规（test_all.sh 据此 FAIL）。
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODEL_DIR = os.path.join(ROOT, "internal/model")
SCAN_DIRS = [os.path.join(ROOT, "internal"), os.path.join(ROOT, "cmd"), os.path.join(ROOT, "pkg")]
LOOKBACK = 40  # 字面量声明距 Create/Save 的最大行距


def tenant_models():
    """扫描 model 包，返回"含 TenantID 字段"的结构体名集合"""
    names = set()
    for fn in os.listdir(MODEL_DIR):
        if not fn.endswith(".go") or fn.endswith("_test.go"):
            continue
        src = open(os.path.join(MODEL_DIR, fn), encoding="utf-8").read()
        for m in re.finditer(r"type\s+(\w+)\s+struct\s*\{(.*?)\n\}", src, re.S):
            if re.search(r"\bTenantID\s+uint", m.group(2)):
                names.add(m.group(1))
    return names


def check_file(path, tenant_set):
    hits = []
    src = open(path, encoding="utf-8").read()
    lines = src.split("\n")
    create_re = re.compile(r"db\.DB\.(Create|Save)\(\s*&(?:([a-zA-Z_]\w*)|model\.(\w+)\{)")
    for i, line in enumerate(lines):
        m = create_re.search(line)
        if not m:
            continue
        var, inline_type = m.group(2), m.group(3)
        # 行内豁免
        if "g12:platform" in line:
            continue
        if inline_type:
            # db.DB.Create(&model.X{...})：窗口=本行起向后找闭合 }
            tname = inline_type
            block = "\n".join(lines[i:i + 30])
            start = 0
        else:
            # 变量：回溯找 `var := model.X{` 字面量声明
            tname, start, found = None, 0, False
            for j in range(max(0, i - LOOKBACK), i):
                dm = re.search(r"\b" + re.escape(var) + r"\s*(?::=|=)\s*(?:&)?model\.(\w+)\{", lines[j])
                if dm:
                    tname, start, found = dm.group(1), j, True
                    break
                # 非字面量声明（函数参数/查询结果 First(&var) 等）→ 赋值链复杂，保守跳过
                if re.search(r"\b" + re.escape(var) + r"\b", lines[j]) and not found and j == max(0, i - LOOKBACK):
                    pass
            if not found:
                continue
        if tname not in tenant_set:
            continue
        block = "\n".join(lines[start:i + 1])
        if "g12:platform" in block:
            continue
        if not re.search(r"\bTenantID\s*:", block):
            hits.append((path, i + 1, tname))
    return hits


def main():
    tenant_set = tenant_models()
    violations = []
    for d in SCAN_DIRS:
        for dirpath, _, files in os.walk(d):
            for fn in files:
                if not fn.endswith(".go") or fn.endswith("_test.go"):
                    continue
                p = os.path.join(dirpath, fn)
                if "/testutil/" in p:
                    continue
                violations += check_file(p, tenant_set)
    if violations:
        print(f"  FAIL  D6 盖章护栏：{len(violations)} 处 db.DB.Create/Save 租户表未显式 TenantID：")
        for p, ln, t in violations:
            print(f"    {os.path.relpath(p, ROOT)}:{ln}  model.{t}")
        print("  修复：结构体字面量显式 TenantID: tid，或平台层(tenant_id=0)行加 // g12:platform 豁免")
        sys.exit(1)
    print(f"  PASS  D6 盖章护栏（租户表 model 类型 {len(tenant_set)} 个，Create/Save 无漏章）")
    sys.exit(0)


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""响应体级契约检查（2026-09-24 收口「结构性弱点 5：契约只到路由层，没到响应体层」）

已有的契约链管到了"路径 + 方法 + 响应类型名"（api.schema.json ↔ api.d.ts ↔ 前端调用孤儿），
但**类型名对得上不等于字段对得上**：后端改一个 json tag 或新增一列，前端 interface 忘了跟，
tsc 照样绿（interface 是前端自己的断言，运行时没人校验）、apidump 照样绿（它只带类型名）。
真实事故形态就是"页面某一列永远空白"，或"字段拼错读到 undefined 却静默降级"。

两条互补的检查：

  A 后端有、前端没声明：api.schema.json 的 data_ts 类型名 ↔ Go 里同名 struct 的 json 字段集合。
    命中即"接口确实会吐这个键，前端类型里却查无此项"——前端读不到是迟早的事。
  B 前端有、后端从不吐：TS interface 的属性名，在**全仓后端出现过的响应键**（gin.H/map 字面量键
    + 所有 json tag）里一次都找不到。命中即"前端在读一个后端从没写过的字段"。

口径刻意保守（宁漏不误伤，否则门禁三天就被关掉）：
  · 只比字段名集合，不比类型与可选性（TS 的可选/窄化表达不了 Go 零值语义，硬比全是假红）；
  · 后端多数响应是 handler 内联 gin.H，没有 struct 可比——A 向只覆盖"确有同名 struct"的类型，
    B 向覆盖全部已声明类型，两者互补；
  · Go struct 含无法展开的内嵌字段时整个类型跳过（不猜）；
  · 两侧同名但语义不同的类型进 coincidence 表白名单，写明为什么是巧合——**白名单是账，不是遮羞布**。

退出码 0 才算过；本脚本无基线文件，两个方向都当场归零（首跑即抓到 Customer.acquisition_code
一例真实漂移，补进 types.ts 后转绿）。

用法：
  python3 tools/check_body_contract.py              # 检查
  python3 tools/check_body_contract.py --list       # 打印覆盖统计与逐条明细
  python3 tools/check_body_contract.py --selftest   # 自证：人造差异必须被抓到
"""
import argparse
import json
import os
import re
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
TS_TYPES = os.path.join(ROOT, "frontend-react", "src", "types.ts")
SCHEMA = os.path.join(ROOT, "api.schema.json")
GO_DIRS = ("internal", "pkg", "cmd", "seed")

# coincidence: Go 与 TS 同名但不是一个东西，A 向不比（值里写清为什么）。
COINCIDENCE = {
    "ChatMessage": "TS 侧描述的是 messages 表的一行（含 conversation_id/route_result 等），"
                   "Go 侧同名 struct 是 internal/ai 的模型消息体（role/content）——只是撞名",
}

# ---------------------------------------------------------------- Go 侧 ----

GO_STRUCT_RE = re.compile(r"^type\s+([A-Za-z_]\w*)\s+struct\s*\{(.*?)^\}", re.S | re.M)
# 字段行 = 名字 + 类型 + 可选反引号标签 + 可选行尾注释。
# 行尾注释这段是必需的：本仓 model 几乎每行都带 `json:"..."` // 说明，早先的正则把注释当非法尾缀
# 放弃整行，结果是"解析成功但零字段"，A 向便安静地把所有类型判成"前端多写了字段"——检查器自伤。
GO_FIELD_RE = re.compile(r"^\s*([A-Za-z_]\w*)\s+([^`\n]+?)\s*(?:`([^`]*)`)?\s*(?://.*)?$")
GO_TAG_JSON_RE = re.compile(r'json:"([^"]*)"')
# 后端"曾经吐过的键"全集：map 字面量的字符串键 + 所有 json tag 名
GO_MAPKEY_RE = re.compile(r'"([a-z][a-z0-9_]*)"\s*:')
GO_JSMTAG_RE = re.compile(r'json:"([a-zA-Z_][\w]*)(?:,[^"]*)?"')


def go_files():
    for base in GO_DIRS:
        for dirpath, _, files in os.walk(os.path.join(ROOT, base)):
            for fn in files:
                if fn.endswith(".go") and not fn.endswith("_test.go"):
                    path = os.path.join(dirpath, fn)
                    try:
                        yield path, open(path, encoding="utf-8").read()
                    except (OSError, UnicodeDecodeError):
                        continue


def go_structs():
    """{类型名: json 字段集合}。同名而定义不一致的类型整体剔除（歧义不猜）。"""
    out, dup = {}, set()
    for _, src in go_files():
        for m in GO_STRUCT_RE.finditer(src):
            name, body = m.group(1), m.group(2)
            fields, ok = parse_go_fields(body)
            if not ok:
                continue
            if name in out and out[name] != fields:
                dup.add(name)
                continue
            out.setdefault(name, fields)
    for n in dup:
        out.pop(n, None)
    return out


def go_key_universe():
    """后端全仓出现过的响应键集合（A/B 两向共用）。"""
    uni = set()
    for _, src in go_files():
        uni.update(GO_MAPKEY_RE.findall(src))
        uni.update(name for name in GO_JSMTAG_RE.findall(src))
    return uni


def parse_go_fields(body):
    """struct 体 → (json 字段名集合, 是否可安全比对)。

    不可安全比对 = 含内嵌（匿名）字段或内联 struct，这类展开超出本工具能力，整类型跳过。
    """
    fields = set()
    for raw in body.splitlines():
        line = raw.strip()
        if not line or line.startswith("//"):
            continue
        if re.fullmatch(r"[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?\s*(?:`[^`]*`)?(?:\s*//.*)?", line):
            return fields, False  # 内嵌字段：不猜它的 json 形态
        m = GO_FIELD_RE.match(raw)
        if not m:
            continue
        tag = m.group(3) or ""
        jm = GO_TAG_JSON_RE.search(tag)
        if jm is None:
            if "gorm:" in tag or "orm:" in tag or "form:" in tag:
                continue  # 纯 DB/表单标签，不是响应键
            fields.add(m.group(1))  # 无 tag：encoding/json 按字段原名输出
            continue
        name = jm.group(1).split(",")[0]
        if name == "-":
            continue
        fields.add(name or m.group(1))
    return fields, True


# ---------------------------------------------------------------- TS 侧 ----

def ts_interfaces():
    """types.ts → {名: (属性集合, 是否带索引签名)}。"""
    src = open(TS_TYPES, encoding="utf-8").read()
    blocks = {}
    for m in re.finditer(r"(?:export\s+)?interface\s+([A-Za-z_]\w*)([^{]*)\{", src):
        blocks[m.group(1)] = (slice_body(src, m.end() - 1), "extends" in m.group(2))
    for m in re.finditer(r"(?:export\s+)?type\s+([A-Za-z_]\w*)\s*=\s*\{", src):
        blocks[m.group(1)] = (slice_body(src, m.end() - 1), False)
    out = {}
    for name, (body, has_extends) in blocks.items():
        props, open_shape = parse_ts_body(body)
        if has_extends:
            props |= inherited_props(src, name)
        out[name] = (props, open_shape)
    return out


def slice_body(src, brace_pos):
    """从 `{` 处按括号配对切出块体。"""
    depth, i = 0, brace_pos
    while i < len(src):
        ch = src[i]
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                return src[brace_pos + 1:i]
        i += 1
    return src[brace_pos + 1:]


TS_PROP_RE = re.compile(r"^\s*(?:readonly\s+)?([A-Za-z_]\w*|\"[^\"]+\")\s*\??\s*:")


def parse_ts_body(body):
    """顶层属性名（嵌套对象的键不计入，否则每个带嵌套的类型都假红）+ 是否开放结构。"""
    props, open_shape, depth = set(), False, 0
    for line in body.splitlines():
        stripped = line.strip()
        if stripped.startswith("//"):
            continue
        if re.match(r"^\s*\[\w+\s*:\s*string\]\s*:", line):
            open_shape = True
        if depth == 0:
            m = TS_PROP_RE.match(line)
            if m:
                props.add(m.group(1).strip('"'))
        depth += line.count("{") + line.count("(") - line.count("}") - line.count(")")
    return props, open_shape


def inherited_props(src, name):
    """`interface X extends A, B` 的父属性并入（父不在本文件则忽略）。"""
    m = re.search(r"interface\s+" + re.escape(name) + r"[^{]*extends\s*([^{]+)\{", src)
    if not m:
        return set()
    out = set()
    for parent in m.group(1).split(","):
        p = parent.strip().split("<")[0].strip()
        mm = re.search(r"interface\s+" + re.escape(p) + r"\s*\{", src)
        if mm:
            ps, _ = parse_ts_body(slice_body(src, mm.end() - 1))
            out |= ps
    return out


# ---------------------------------------------------------------- 对账 ----

def declared_types():
    """api.schema.json 的 data_ts 里出现的类型标识符（响应类型名的权威清单）。"""
    schema = json.load(open(SCHEMA, encoding="utf-8"))
    names = set()
    for r in schema.get("routes", []):
        for tok in re.findall(r"[A-Za-z_]\w*", r.get("data_ts") or ""):
            if tok not in ("unknown", "map", "string", "int", "bool", "float64"):
                names.add(tok)
    return names


def compare():
    """返回 (A 向差异, B 向差异, 覆盖统计)。差异元素为 (类型名, [字段...])。"""
    go, tsi, uni = go_structs(), ts_interfaces(), go_key_universe()
    decl = declared_types()
    a_diffs, b_diffs, pairs, checked_b = [], [], 0, 0
    for name in sorted(decl):
        t = tsi.get(name)
        if t is None:
            continue
        props, open_shape = t
        checked_b += 1
        # B 向：前端在读一个后端从没写过的键
        phantom = sorted(p for p in props if p not in uni)
        if phantom:
            b_diffs.append((name, phantom))
        g = go.get(name)
        if g is None or name in COINCIDENCE:
            continue
        pairs += 1
        if not g:
            # struct 解析出零字段＝解析器失配（本工具历史上真踩过行尾注释），必须响亮地报错
            print(f"  FAIL  Go struct {name} 解析到 0 个字段：解析器失配，请修工具而不是放行")
            sys.exit(1)
        only_go = sorted(g - props)
        if only_go:
            a_diffs.append((name, only_go))
    stats = {"types_declared": len(decl), "pairs_a": pairs, "types_b": checked_b, "universe": len(uni)}
    return a_diffs, b_diffs, stats


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--list", action="store_true", help="打印覆盖统计与明细")
    ap.add_argument("--selftest", action="store_true", help="自证两向都能抓到人造差异")
    args = ap.parse_args()
    if args.selftest:
        return selftest()

    a_diffs, b_diffs, st = compare()
    if args.list:
        for n, fs in a_diffs:
            print(f"  A {n}: 后端吐但前端类型没有 -> {fs}")
        for n, fs in b_diffs:
            print(f"  B {n}: 前端在读但后端从不吐 -> {fs}")
        print(f"  —— 覆盖: 声明类型 {st['types_declared']}，A 向同名 struct 对 {st['pairs_a']}，"
              f"B 向已查类型 {st['types_b']}，后端响应键全集 {st['universe']}")
        return 0 if not a_diffs and not b_diffs else 1

    # 非空转自证：两条检查都必须真的在看东西，否则"零差异"只是解析器罢工
    if st["pairs_a"] < 3 or st["types_b"] < 20 or st["universe"] < 300:
        print(f"  FAIL  覆盖异常（{st}）：解析器很可能已失配，此时的零差异不可信")
        return 1
    if a_diffs or b_diffs:
        for n, fs in a_diffs:
            print(f"  FAIL[A] {n}: 后端吐但前端类型没声明 -> {n}.{fs}（改 types.ts 补齐，或后端确属误加）")
        for n, fs in b_diffs:
            print(f"  FAIL[B] {n}: 前端在读后端从不吐的字段 -> {fs}（拼错或后端已改名）")
        return 1
    print(f"  ok  响应体级契约一致（A 向 {st['pairs_a']} 对 struct / B 向 {st['types_b']} 个类型 / 键全集 {st['universe']}）")
    return 0


def selftest():
    """自证：解析器与两向判定都对人造差异敏感。

    检查器一旦正则失配（Go 换写法、TS 换格式），它会安静地返回零差异，而零差异在门禁里就是绿灯
    ——所以必须反过来证明"有差异时它真的响"。
    """
    gfields, ok = parse_go_fields('''
\tID uint `json:"id"`                                   // 主键（行尾注释是常态，必须容忍）
\tName string `gorm:"column:name" json:"name"`
\tSecret string `json:"-"`
\tPlain string
''')
    assert ok and gfields == {"id", "name", "Plain"}, f"Go 字段解析异常: {gfields}"
    _, emb_ok = parse_go_fields("\tgorm.Model\n\tName string `json:\"name\"`\n")
    assert not emb_ok, "含内嵌字段的 struct 被判为可比对（会漏字段）"

    props, open_shape = parse_ts_body('''
  id: number
  name?: string
  meta: { a: number; b: number }
  [key: string]: unknown
''')
    assert props == {"id", "name", "meta"} and open_shape, f"TS 属性解析异常: {props}"
    assert "a" not in props, "嵌套键泄漏到顶层"

    uni = go_key_universe()
    assert "tenant_id" in uni and "created_at" in uni, "后端键全集为空/过窄，B 向会全面假红"
    assert "definitely_not_a_backend_key_zzz" not in uni, "键全集把任意字符串都收进来了，B 向将永不响"
    print("  selftest ok：Go 字段/内嵌跳过/ts 属性/键全集宽窄 四类判定均生效")
    return 0


if __name__ == "__main__":
    sys.exit(main())

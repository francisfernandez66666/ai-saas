#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
行业包内容质量校验器（G-21，2026-09-24）

为什么需要它：包内容走的是 encoding/json 的宽松解码——**字段名写错不报错**，
未知键被静默丢掉、目标键取零值。于是出现过两类真实事故，且现场毫无声音：
  1) auto 基包 scripts.json 把话术写成 "template"/"trigger_keywords"，
     而 ScriptTemplate 认的是 prompt_template/hook_template/trigger_tags。
     结果三条模板全部以"空话术"入库，还能被策略召回（无 trigger_tags 时给 0.6 基础分），
     AI 拿到一条什么都没有的模板。
  2) general 包的 scripts.json 是一个关键词字典而不是数组，
     ParseScripts 直接失败 → 这个包永远注册不上、也永远绑不到租户身上。
本项目旧的冒烟断言是"目录存在 + JSON 文件数 ≥6"，上面两条在 463 项冒烟里全绿。

判据口径（与代码消费端逐字对齐，勿凭直觉加规则）：
  - scripts.json   → []ScriptTemplate：必须是数组，且每条至少有 prompt_template/hook_template 之一
  - product_kb.json→ ProductKB：必须有 features[]，且每条有 desc_template 或 short_desc
  - prompts.json   → GetBoundPackPrompts 只读 persona / system_instruction 两个键
  - mindset.json   → GetBoundPackMindset 只读 guidance(字符串) / mindset(数组)；
                     纯数字参数对象不会进 prompt，判 WARN 不判 FAIL
  - params.json    → 合法 JSON 即可（消费端 flattenJSONLines 兼容任意形态）
  - tags.json      → 必须是数组，每条有 code+name（materializeTags 会静默跳过空 code/name）
  - flows.json     → 必须是数组，每条有 code/name/nodes；节点 type 必须在引擎枚举内，
                     strategy 节点的 config.conditions 长度须与 next_nodes 一一对应
                     （findNextNodeByCondition 按下标配条件，错一位就分错支）
退出码：FAIL>0 → 1（冒烟/CI 阻断）；只有 WARN → 0。
"""
import json
import os
import sys

# 引擎侧真实枚举（internal/model/flow.go + internal/engine/flow/nodes.go 的 switch 分支）
FLOW_NODE_TYPES = {
    "start", "ai", "strategy", "human", "condition", "tag_update", "wait", "end",
}
# 策略路由值（internal/engine/strategy/types.go Route* 常量 + 入口实际写入值）
ROUTE_VALUES = {
    "ai", "human", "fish", "price", "simple_fast", "store_visit_fast",
    "offtopic_hardbound", "lead_captured_confirmed", "pending_human",
    "merged_suppressed", "channel_ai", "channel_inbound",
    "ai_triggered_by_advisor", "ai_triggered_by_openapi",
}
REQUIRED_FILES = ["scripts.json", "product_kb.json", "prompts.json",
                  "flows.json", "tags.json", "params.json", "mindset.json"]

fails, warns = [], []


def fail(where, msg):
    fails.append(f"{where}: {msg}")


def warn(where, msg):
    warns.append(f"{where}: {msg}")


def load(path):
    """读 JSON；返回 (值, 错误串)。文件缺失返回 (None, 'missing')。"""
    if not os.path.exists(path):
        return None, "missing"
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f), None
    except Exception as e:  # noqa: BLE001 —— 校验器要把任何解析失败变成人话
        return None, f"parse error: {e}"


def nonempty_str(v):
    return isinstance(v, str) and v.strip() != ""


def check_dir(src_dir):
    code = os.path.basename(src_dir.rstrip("/"))
    for fn in REQUIRED_FILES:
        v, err = load(os.path.join(src_dir, fn))
        if err == "missing":
            fail(code, f"缺少 {fn}（打包工具会补 [] 空壳，但空壳进不了消费链）")
            continue
        if err:
            fail(code, f"{fn} {err}")
            continue
        if fn == "scripts.json":
            check_scripts(code, v)
        elif fn == "product_kb.json":
            check_kb(code, v)
        elif fn == "prompts.json":
            check_prompts(code, v)
        elif fn == "mindset.json":
            check_mindset(code, v)
        elif fn == "tags.json":
            check_tags(code, v)
        elif fn == "flows.json":
            check_flows(code, v)


def check_scripts(code, v):
    where = f"{code}/scripts.json"
    if not isinstance(v, list):
        fail(where, f"必须是 ScriptTemplate 数组，实际是 {type(v).__name__}"
                    "——ParseScripts 会直接报错，这个包永远无法物化")
        return
    if not v:
        warn(where, "模板数为 0（该包不提供话术，全靠上层包兜底）")
    seen = set()
    for i, t in enumerate(v):
        if not isinstance(t, dict):
            fail(where, f"第 {i} 条不是对象")
            continue
        tid = t.get("id") or f"#{i}"
        if tid in seen:
            fail(where, f"模板 id 重复: {tid}（物化后主键撞车，后一条覆盖前一条）")
        seen.add(tid)
        if not nonempty_str(t.get("prompt_template")) and not nonempty_str(t.get("hook_template")):
            fail(where, f"模板 {tid} 的 prompt_template/hook_template 全为空"
                        "——空模板能被召回并吐出一条无话术的回复（G-21 事故现场，字段名请核对）")
        for k in ("trigger_tags", "required_tags", "hook_fields", "required_features"):
            if k in t and not isinstance(t[k], list):
                fail(where, f"模板 {tid} 的 {k} 必须是数组")
        at = t.get("anchor_type")
        if at is not None and (not isinstance(at, int) or not (0 <= at <= 6)):
            fail(where, f"模板 {tid} 的 anchor_type={at!r} 越界（合法 0-6，见 model.AnchorType*）")


def check_kb(code, v):
    where = f"{code}/product_kb.json"
    if not isinstance(v, dict):
        fail(where, f"必须是对象（ProductKB），实际是 {type(v).__name__}")
        return
    feats = v.get("features")
    if not isinstance(feats, list):
        fail(where, "features 缺失或非数组——ParseProductKB 之后物化环节拿不到卖点")
        return
    if not feats:
        warn(where, "features 为空（该包不提供卖点）")
    seen = set()
    for i, f in enumerate(feats):
        if not isinstance(f, dict):
            fail(where, f"第 {i} 条卖点不是对象")
            continue
        fid = f.get("id") or f"#{i}"
        if fid in seen:
            fail(where, f"卖点 id 重复: {fid}")
        seen.add(fid)
        if not nonempty_str(f.get("desc_template")) and not nonempty_str(f.get("short_desc")):
            fail(where, f"卖点 {fid} 的 desc_template/short_desc 全为空——模板填充会得到一段空白")


def check_prompts(code, v):
    where = f"{code}/prompts.json"
    if not isinstance(v, dict):
        fail(where, f"必须是对象，实际是 {type(v).__name__}")
        return
    # 消费端 service.GetBoundPackPrompts 只认这两个键；写成 system_prompt 之类的
    # 历史别名不会报错，只会让人设静默不进 prompt（auto 包此前的实际状态）
    has_persona = nonempty_str(v.get("persona")) or nonempty_str(v.get("system_instruction"))
    if not has_persona:
        # 区分"有意留空"与"字段名写错"：
        #   {} → 部门包/演示包常见（该层不覆写人设），apply 侧已按语义空壳跳过、不写覆盖键，判 WARN；
        #   有键但两个都不认识 → 内容确实写了却没进 prompt，正是 auto 包此前的现场，判 FAIL。
        if v:
            fail(where, f"只有 {sorted(v.keys())} 这类键，而消费端只读 persona/system_instruction"
                        "——人设写在了 prompt 永远读不到的键上")
        else:
            warn(where, "prompts 为空对象（该层不覆写人设，走上层包/系统层兜底）")
    dead = [k for k in v if k not in ("persona", "system_instruction")]
    if dead:
        warn(where, f"存在消费端不读的键 {dead}（仅存证，不进 prompt）")


def check_mindset(code, v):
    where = f"{code}/mindset.json"
    if isinstance(v, list):
        if not v:
            warn(where, "为空数组")
        return
    if isinstance(v, dict):
        if nonempty_str(v.get("guidance")) or isinstance(v.get("mindset"), list):
            return
        if not v:
            warn(where, "mindset 为空对象（该层不覆写心智指引）")
        else:
            warn(where, f"只有 {sorted(v.keys())} 这类参数键——GetBoundPackMindset 只读 "
                        "guidance/mindset，这些数字不会进 prompt（作为引擎参数存证可以，但别指望它影响话术）")
        return
    fail(where, f"必须是对象或字符串数组，实际是 {type(v).__name__}")


def check_tags(code, v):
    where = f"{code}/tags.json"
    if not isinstance(v, list):
        fail(where, f"必须是标签数组，实际是 {type(v).__name__}"
                    "——materializeTags 解析失败会让整次物化回滚")
        return
    for i, t in enumerate(v):
        if not isinstance(t, dict):
            fail(where, f"第 {i} 条不是对象")
            continue
        if not nonempty_str(t.get("code")) or not nonempty_str(t.get("name")):
            fail(where, f"第 {i} 条缺 code/name——materializeTags 静默跳过，标签数与包声明不符")


def check_flows(code, v):
    where = f"{code}/flows.json"
    if not isinstance(v, list):
        fail(where, f"必须是流程数组，实际是 {type(v).__name__}"
                    "——ApplyToTenant 在此 return error，整包物化（含模板/卖点）全部回滚")
        return
    for i, fl in enumerate(v):
        if not isinstance(fl, dict):
            fail(where, f"第 {i} 条不是对象")
            continue
        fcode = fl.get("code") or f"#{i}"
        if not nonempty_str(fl.get("code")) or not nonempty_str(fl.get("name")):
            fail(where, f"流程 {fcode} 缺 code/name——apply 时静默 continue，包声明的流程根本不存在")
            continue
        nodes = fl.get("nodes")
        if not isinstance(nodes, list) or not nodes:
            fail(where, f"流程 {fcode} 的 nodes 缺失或为空")
            continue
        ids = set()
        by_id = {}
        for n in nodes:
            if not isinstance(n, dict) or not nonempty_str(n.get("id")):
                fail(where, f"流程 {fcode} 存在无 id 节点")
                continue
            ids.add(n["id"])
            by_id[n["id"]] = n
            if n.get("type") not in FLOW_NODE_TYPES:
                fail(where, f"流程 {fcode} 节点 {n['id']} 的 type={n.get('type')!r} 不在引擎枚举 "
                            f"{sorted(FLOW_NODE_TYPES)} 内——ExecuteNode 走 default 分支报未知类型并中止")
        for n in nodes:
            nid = n.get("id")
            for nxt in (n.get("next_nodes") or []):
                if nxt not in ids:
                    fail(where, f"流程 {fcode} 节点 {nid} 指向不存在的节点 {nxt}")
            conds = (n.get("config") or {}).get("conditions") if isinstance(n.get("config"), dict) else None
            if conds is not None:
                nn = n.get("next_nodes") or []
                if len(conds) != len(nn):
                    fail(where, f"流程 {fcode} 节点 {nid} 的 conditions({len(conds)}) 与 "
                                f"next_nodes({len(nn)}) 数量不等——按下标配对，错一位就分错支")
                for c in conds:
                    if c not in ROUTE_VALUES:
                        fail(where, f"流程 {fcode} 节点 {nid} 的条件 {c!r} 不是策略路由值"
                                    "（永远匹配不上，等于该分支不可达）")
        sid = fl.get("start_node_id")
        if nonempty_str(sid) and sid not in ids:
            fail(where, f"流程 {fcode} 的 start_node_id={sid} 不在 nodes 里")


def selftest():
    """非空自证：构造一个"全错包"，必须被自己的规则抓满，否则检查器是空转的。"""
    import tempfile
    with tempfile.TemporaryDirectory() as d:
        src = os.path.join(d, "badpack")
        os.makedirs(src)
        open(os.path.join(src, "scripts.json"), "w").write(
            '[{"id":"a","template":"老字段"}]')
        open(os.path.join(src, "product_kb.json"), "w").write('{"features":[{"id":"f"}]}')
        open(os.path.join(src, "prompts.json"), "w").write('{"system_prompt":"x"}')
        open(os.path.join(src, "mindset.json"), "w").write('{"nurture_rounds_target":3}')
        open(os.path.join(src, "tags.json"), "w").write('{"attitude":{"a":["b"]}}')
        open(os.path.join(src, "params.json"), "w").write('{}')
        open(os.path.join(src, "flows.json"), "w").write('{"stages":[{"name":"认知期"}]}')
        before_f, before_w = len(fails), len(warns)
        check_dir(src)
        nf, nw = len(fails) - before_f, len(warns) - before_w
        # 三类必抓：scripts 空话术 / prompts 死键 / flows 非数组；外加 tags 非数组、kb 空描述
        if nf < 5:
            print(f"SELFTEST FAIL: 坏包只抓出 {nf} 条 FAIL（期望 ≥5）——检查器空转", file=sys.stderr)
            return 1
        print(f"SELFTEST OK: 坏包抓出 FAIL={nf} WARN={nw}")
        return 0


def main(argv):
    if "--selftest" in argv:
        return selftest()
    roots = [a for a in argv if not a.startswith("-")] or ["packs-src"]
    root = roots[0]
    if not os.path.isdir(root):
        print(f"目录不存在: {root}", file=sys.stderr)
        return 2
    dirs = sorted(os.path.join(root, n) for n in os.listdir(root)
                  if os.path.isdir(os.path.join(root, n)))
    if not dirs:
        print(f"{root} 下没有包源目录——检查器无事可做即为空转", file=sys.stderr)
        return 1
    for d in dirs:
        check_dir(d)
    only = os.environ.get("PACK_CONTENT_ONLY")
    for w in warns:
        print(f"WARN  {w}")
    for f in fails:
        print(f"FAIL  {f}")
    print(f"包内容校验：目录 {len(dirs)} 个，FAIL={len(fails)} WARN={len(warns)}"
          + (f"（只看 {only}）" if only else ""))
    if fails:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))

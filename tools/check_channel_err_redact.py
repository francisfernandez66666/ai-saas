#!/usr/bin/env python3
"""通道 HTTP 错误外抛必须过脱敏（FIX-2 防回潮，2026-09-26）。

要封的事故形态：Go 的 `*url.Error` 会把**完整 URL（含 query）**拼进 `Error()`。
通道取 token 的 URL 上挂着 `corpsecret` / `secret`，所以任何一句"把 Do(req) 的 err 原样 return 出去"
都会把企业级密钥带到日志、`channel_outbound.error` 列、以及管理界面回显里——
通道密钥泄露＝拿到就能冒名给客户发消息，是资金级安全事故。

修法口径（已落地）：在**产生处**包一层 `redactErr(err)`（见 `internal/channel/redact.go`），
落库与出接口再各兜一道。本脚本封"后来人新增适配器时又漏一处"：
凡 `x, err := <client>.Do(req)` 之后紧跟的那个 `if err != nil { … }` 分支，
不得把裸 `err` return 出去。

判据为什么只盯"Do 之后第一个 if err != nil 分支"（而不是"函数里任何 return err"）：
只有这一处的 err 一定是**传输层错误**（`*url.Error`，含完整 URL）。
首版按函数体扫，在同一批文件上误伤三处 `io.ReadAll` / `json.Unmarshal` 的错误
（它们既不含 URL 也确实只包了中文前缀），判据恒真会把人引向绕过它——所以收窄到分支级，
并把那三处形态写成自证样本⑤。"""

import os
import re
import subprocess
import sys
import tempfile

# Do 调用点：`resp, err := chanHTTP.Do(req)` / `body, err := c.hc.Do(req)`
DO_ASSIGN_RE = re.compile(r",\s*err\s*:?=\s*[A-Za-z_][\w.]*\.Do\(")
IF_ERR_RE = re.compile(r"^\s*if\s+err\s*!=\s*nil\s*\{")
RETURN_RE = re.compile(r"\breturn\b.*\berr\b")
# 合规形态：redactErr(err)（channel 包内）或 pii.RedactSecretURL(err.Error())（api 包内）
SAFE_RE = re.compile(r"redactErr\(|RedactSecretURL\(")
SCAN_DIR = os.path.join("internal", "channel")


def _is_comment(line: str) -> bool:
    s = line.lstrip()
    return s.startswith("//") or s.startswith("*")


def check_source(path: str, src: str) -> list:
    """扫描单个源文件，返回 [(行号, 源码片段)] 违规清单。"""
    hits = []
    lines = src.splitlines()
    for i, line in enumerate(lines):
        if _is_comment(line) or not DO_ASSIGN_RE.search(line):
            continue
        # 只看紧跟其后的 if err != nil 分支体（最多 12 行，够容纳注释 + 一条 return）
        j = i + 1
        while j < len(lines) and j <= i + 3 and lines[j].strip() == "":
            j += 1
        if j >= len(lines) or not IF_ERR_RE.match(lines[j]):
            continue
        depth = 1
        k = j + 1
        while k < len(lines) and depth > 0:
            depth += lines[k].count("{") - lines[k].count("}")
            body = lines[k]
            if not _is_comment(body) and RETURN_RE.search(body) and not SAFE_RE.search(body):
                hits.append((k + 1, body.strip()))
            k += 1
    return hits



def scan_repo(root: str) -> list:
    """扫描 internal/channel 下非测试 Go 文件，返回违规清单。"""
    bad, sites = [], 0
    d = os.path.join(root, SCAN_DIR)
    if not os.path.isdir(d):
        return [(-1, f"目录不存在：{d}")], 0
    for name in sorted(os.listdir(d)):
        if not name.endswith(".go") or name.endswith("_test.go"):
            continue
        p = os.path.join(d, name)
        with open(p, encoding="utf-8") as f:
            src = f.read()
        sites += len(DO_ASSIGN_RE.findall(src))
        for ln, frag in check_source(p, src):
            bad.append((f"{p}:{ln}", frag))
    return bad, sites


def main() -> int:
    if "--selftest" in sys.argv:
        return selftest()
    root = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                          capture_output=True, text=True).stdout.strip() or "."
    bad, sites = scan_repo(root)
    if bad:
        print(f"FAIL 通道 HTTP 错误外抛未脱敏（{len(bad)} 处）——*url.Error 含完整 URL，corpsecret/secret 会跟着进日志与数据列")
        for loc, frag in bad:
            print(f"  {loc}: {frag}   ← 改为 redactErr(err)")
        return 1
    # 扫到 0 个 Do 调用点＝这条锁在空转（文件被移动/改名都会这样），宁可红也不要假绿
    if sites == 0:
        print(f"FAIL {SCAN_DIR}/ 里一个 HTTP 调用点都没扫到——判据空转，锁已失效")
        return 1
    print(f"OK   通道 HTTP 错误外抛均已过 redactErr（{sites} 个调用点全查，FIX-2 防回潮）")
    return 0


GOOD = '''package channel

func good(ctx context.Context, req *http.Request) (string, error) {
	resp, err := chanHTTP.Do(req)
	if err != nil {
		return "", redactErr(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}
'''

BAD = '''package channel

func bad(ctx context.Context, req *http.Request) (string, error) {
	resp, err := chanHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return "ok", nil
}
'''

NEUTRAL = '''package channel

func neutral(ctx context.Context, req *http.Request) int {
	resp, err := chanHTTP.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return 1
}

// 注释里出现 return "", err 不得被判（形态说明注释是仓库惯例）
func commentOnly() string {
	// return "", err
	return ""
}
'''

# 样本⑤：Do 那步已正确脱敏，后面 io.ReadAll / json.Unmarshal 的 err 与 URL 无关，必须放行
# （首版判据按"函数体内任何 return err"扫，正是在这三处上误伤）
AFTER_DO_ERRORS = '''package channel

func postJSON(req *http.Request, out interface{}) (int, error) {
	resp, err := chanHTTP.Do(req)
	if err != nil {
		return -1, redactErr(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return -1, fmt.Errorf("读取渠道响应失败: %w", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return -1, fmt.Errorf("渠道响应解析失败: %w", err)
	}
	return 0, nil
}
'''


def selftest() -> int:
    ok = True
    cases = [
        ("样本①已知违规（Do 后裸 return err）", check_source("bad.go", BAD), True),
        ("样本②已包 redactErr", check_source("good.go", GOOD), False),
        ("样本③不外抛 err / 纯注释", check_source("neutral.go", NEUTRAL), False),
        ("样本⑤Do 后读体/解析错误（与 URL 无关）", check_source("after.go", AFTER_DO_ERRORS), False),
    ]
    for label, hits, expect_hit in cases:
        want = "≥1" if expect_hit else "0"
        flag = "ok" if (bool(hits) == expect_hit) else "MISMATCH"
        print(f"  {label}：命中 {len(hits)}（期望 {want}）[{flag}]")
        for ln, frag in hits:
            print(f"      -> :{ln} {frag}")
        if bool(hits) != expect_hit:
            ok = False
    if not check_source("bad.go", BAD):
        print("SELFTEST FAIL: 判据恒假——已知违规形态抓不到，这条锁等于没有")
        ok = False
    # 目录级自证：临时目录里放一份 BAD，scan 必须能报出来（证明扫描面真的覆盖了该目录）
    with tempfile.TemporaryDirectory() as root:
        os.makedirs(os.path.join(root, SCAN_DIR), exist_ok=True)
        with open(os.path.join(root, SCAN_DIR, "bad.go"), "w", encoding="utf-8") as f:
            f.write(BAD)
        hits, n = scan_repo(root)
        if not hits:
            print("SELFTEST FAIL: 目录级扫描没读到文件（路径口径错，锁会静默空跑）")
            ok = False
        else:
            print(f"  样本④目录级扫描命中（调用点 {n} 个、违规 {len(hits)} 处，证明扫描面非空）")
    # 空转保护自证：一个只有合规文件的临时目录必须**不**报错，而 0 调用点目录必须报错
    with tempfile.TemporaryDirectory() as root:
        os.makedirs(os.path.join(root, SCAN_DIR), exist_ok=True)
        with open(os.path.join(root, SCAN_DIR, "empty.go"), "w", encoding="utf-8") as f:
            f.write("package channel\n")
        hits, n = scan_repo(root)
        flag = "ok" if (n == 0 and not hits) else "MISMATCH"
        print(f"  样本⑥零调用点目录：调用点 {n}（期望 0，主流程据此判空转）[{flag}]")
        if n != 0:
            ok = False
    print("SELFTEST PASS: 违规必抓 / 合规必放 / 扫描面非空 / 空转可判" if ok else "SELFTEST 未通过")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())

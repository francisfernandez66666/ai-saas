// Command goldeneval 是 AI 黄金问答集的自动跑分与发布阈值门禁入口（PLAN_FIX_2026-09-21 D1）。
//
// 用法：
//
//	go run ./cmd/goldeneval                        # 内嵌黄金集，默认阈值 95%，报告写 GOLDEN_REPORT.md
//	go run ./cmd/goldeneval -min-pass 1.0          # 全量必须通过（严格模式）
//	go run ./cmd/goldeneval -set ./my/cases -out /tmp/r.md
//	go run ./cmd/goldeneval -list                  # 只列用例，不跑分
//
// 退出码：
//
//	0 通过阈值；1 低于阈值（阻断发布）；2 黄金集自身不合法（守卫失效，必须先修集）
//
// 关于 LLM 评分层：本 CLI 只跑**确定性层**（路由/关键词/离线话术评分），
// 不联网、不读库、不烧 token，因此可以放在每次提交的 CI 门禁里。
// 需要 LLM 语义评分时，由调用方在进程内 `golden.Judge = ...` 注入（见 internal/golden 包注释）；
// CLI 会如实打印该层是否执行，未注入时标注 skipped，绝不伪装成通过。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"ai-scrm/internal/golden"
)

func main() {
	minPass := flag.Float64("min-pass", 0.95, "通过率下限（低于即退出码 1，阻断发布）")
	out := flag.String("out", "GOLDEN_REPORT.md", "Markdown 报告输出路径（空字符串=不写文件）")
	set := flag.String("set", "", "自定义黄金集目录（留空=用内嵌 cases/）")
	listOnly := flag.Bool("list", false, "只列出用例清单，不执行打分")
	flag.Parse()

	cases, err := loadCases(*set)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载黄金集失败: %v\n", err)
		os.Exit(2)
	}

	// 黄金集自身不合法 = 守卫静默失效，必须先修集，绝不能"跑过了就算过"
	if problems := golden.Validate(cases); len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "黄金集不合法（%d 项），请先修正用例：\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  · %s\n", p)
		}
		os.Exit(2)
	}

	if *listOnly {
		for _, c := range cases {
			fmt.Printf("%-8s %-8s %s\n", c.ID, c.Family, firstLine(c.Question))
		}
		fmt.Printf("\n共 %d 条\n", len(cases))
		return
	}

	rep := golden.Run(cases)
	fmt.Println(strings.TrimRight(rep.Markdown(), "\n"))

	if *out != "" {
		if err := os.WriteFile(*out, []byte(rep.Markdown()), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "写报告失败 %s: %v\n", *out, err)
		} else {
			fmt.Printf("\n报告已写入 %s\n", *out)
		}
	}

	rate := rep.PassRate()
	if rate < *minPass {
		fmt.Fprintf(os.Stderr, "\n门禁未通过：通过率 %.2f%% 低于阈值 %.2f%%（%d/%d 通过，%d 条失败）\n",
			rate*100, *minPass*100, rep.Passed, rep.Total, len(rep.Failures))
		os.Exit(1)
	}
	fmt.Printf("\n门禁通过：通过率 %.2f%% ≥ 阈值 %.2f%%（%d/%d）\n", rate*100, *minPass*100, rep.Passed, rep.Total)
}

// loadCases 按 -set 选择自定义目录或内嵌黄金集。
func loadCases(dir string) ([]golden.Case, error) {
	if dir == "" {
		return golden.Load()
	}
	return golden.LoadFS(os.DirFS(dir), ".")
}

// firstLine 取首行用于清单展示。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if rs := []rune(s); len(rs) > 40 {
		return string(rs[:40]) + "…"
	}
	return s
}

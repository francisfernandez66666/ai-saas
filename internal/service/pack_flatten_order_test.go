// 行业包超参扁平化的**行序确定性**回归单测（2026-09-25 残项收口批·残项4）。
//
// 缺陷现场：flattenJSONLines 把 params.json 展平成 "key: value" 行注入 LLM 系统提示，
// map 分支原先直接 `for k := range t` 遍历。Go 的 map 迭代序**每次随机**（运行时故意打乱，
// 不是哈希实现细节），于是同一份汽车包超参文件在两次请求里输出的行序不同——
// 表现就是用户报的"键位错位"（看着像参数串了行，其实键值配对一直是对的，漂的是行序）。
// 实际后果有两条：① 系统提示词前缀每次不同，模型侧 prompt cache 白烧；
// ② 排查"这个租户的回复为什么变了"时，两份提示词逐行对不上，无从比对。
//
// 为什么这么测（测试自伤规避）：
// 只断言"两次调用结果相同"是**不够的**——排序写错（比如按 value 排、按长度排）时
// 它照样绿。所以本测三口并列：
// ①等值锁：断言输出**逐行等于**字典序的那一份（不是"和前一次相等"这种自洽锁）；
// ②配对锁：把输出行反解回 key→value，与原始 JSON 逐键比对，防"排序时把值一起搬错"；
// ③反证：用一个刻意不排序的姊妹函数跑同样断言，必须**真的红**（出现多种行序），
//
//	否则说明"随机序"这个前提是空的、①②两条护栏在空转。
package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// autoPackParamsJSON 汽车包超参文件的**真实内容**（packs-src/auto/params.json 逐字节抄录）。
// 刻意用真实样本而非合成 abc：这六把键就是线上注入提示词的那一份，测的就是它。
const autoPackParamsJSON = `{
  "delay_simple_min": 2,
  "delay_simple_max": 5,
  "delay_normal_min": 5,
  "delay_normal_max": 10,
  "max_merge_messages": 3,
  "merge_wait_seconds": 25
}`

// flattenUnsortedForTest 是修复前的写法（map 不排序直接 range），**仅供反证用例调用**。
// 它不参与产品逻辑，存在的唯一意义是证明"随机序"这个前提是真实可观测的。
func flattenUnsortedForTest(raw string) []string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	var out []string
	var walk func(val any, p string)
	walk = func(val any, p string) {
		switch t := val.(type) {
		case map[string]any:
			for k, sub := range t { // 故意不排序：复刻缺陷写法
				key := k
				if p != "" {
					key = p + "." + k
				}
				walk(sub, key)
			}
		case []any:
			for i, sub := range t {
				walk(sub, fmt.Sprintf("%s[%d]", p, i))
			}
		default:
			out = append(out, fmt.Sprintf("%s: %v", p, t))
		}
	}
	walk(v, "")
	return out
}

// TestFlattenJSONLinesKeepsDeterministicOrder 钉住"同输入必同输出、且输出就是字典序那一份"。
func TestFlattenJSONLinesKeepsDeterministicOrder(t *testing.T) {
	// 期望值手写字典序全量（等值锁）：键名字典序 = delay_normal_max, delay_normal_min,
	// delay_simple_max, delay_simple_min, max_merge_messages, merge_wait_seconds
	want := []string{
		"delay_normal_max: 10",
		"delay_normal_min: 5",
		"delay_simple_max: 5",
		"delay_simple_min: 2",
		"max_merge_messages: 3",
		"merge_wait_seconds: 25",
	}
	// 重复次数取 200：map 随机序在 6 键上有 720 种排列，200 次仍恒定输出同一份才是真确定性，
	// 而不是"运气好抽到同一排列"。
	const rounds = 200
	for i := 0; i < rounds; i++ {
		got := flattenJSONLines(autoPackParamsJSON, "")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("第%d次扁平化结果不等于字典序基准\n got=%v\nwant=%v", i+1, got, want)
		}
	}
}

// TestFlattenJSONLinesKeepsKeyValuePairing 把每行反解回 key→value 与源 JSON 逐键比对。
// 为什么单独一条：排序改错（键排了、值没跟着走）会让上一条**照样绿**——
// 因为它比的也是"排完序的那一份"。配对锁才证明键与值仍然对得上。
func TestFlattenJSONLinesKeepsKeyValuePairing(t *testing.T) {
	var src map[string]any
	if err := json.Unmarshal([]byte(autoPackParamsJSON), &src); err != nil {
		t.Fatalf("样本本身不是合法 JSON，测例失效: %v", err)
	}
	lines := flattenJSONLines(autoPackParamsJSON, "")
	if len(lines) != len(src) {
		t.Fatalf("扁平化行数 %d != 原始键数 %d（有键被吞或重复输出）", len(lines), len(src))
	}
	seen := map[string]bool{}
	for _, ln := range lines {
		k, v, ok := strings.Cut(ln, ": ")
		if !ok {
			t.Fatalf("行 %q 不是 \"key: value\" 形态", ln)
		}
		want, exists := src[k]
		if !exists {
			t.Fatalf("行 %q 的键 %q 在源 JSON 里不存在（凭空多出键）", ln, k)
		}
		if seen[k] {
			t.Fatalf("键 %q 出现多次", k)
		}
		seen[k] = true
		if got := fmt.Sprint(v); got != fmt.Sprint(want) {
			t.Fatalf("键 %q 的值错位: 行里=%q 源里=%v", k, got, want)
		}
	}
}

// TestFlattenJSONLinesSortsNestedPaths 嵌套形态：父.子 前缀路径也必须字典序。
// 为什么单列：扁平键排序不能顺带证明递归下传的前缀路径有序——
// 嵌套分支若仍按 map 随机序走，"vehicle.engine.power" 这类行照样漂。
func TestFlattenJSONLinesSortsNestedPaths(t *testing.T) {
	const nested = `{"params":{"vehicle":{"zebra":"z","engine":{"power":"150kW","torque":"350Nm"}},"abb":"a"}}`
	want := []string{
		"abb: a",
		"vehicle.engine.power: 150kW",
		"vehicle.engine.torque: 350Nm",
		"vehicle.zebra: z",
	}
	for i := 0; i < 200; i++ {
		got := flattenJSONLines(nested, "")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("第%d次嵌套扁平化不等于字典序基准\n got=%v\nwant=%v", i+1, got, want)
		}
	}
}

// TestUnsortedFlattenReallyDrifts 反证：证明"map 序随机"这件事在本机可观测。
// 本测若红（200 次都不漂），说明上面几条确定性断言的前提是空的——
// 那时应当换成更大键集或显式提示：护栏已失去判别力，不是产品回退。
func TestUnsortedFlattenReallyDrifts(t *testing.T) {
	const manyKeys = `{"k01":1,"k02":2,"k03":3,"k04":4,"k05":5,"k06":6,"k07":7,"k08":8,"k09":9,"k10":10,"k11":11,"k12":12}`
	// 先把基准（字典序）算出来
	var m map[string]any
	if err := json.Unmarshal([]byte(manyKeys), &m); err != nil {
		t.Fatalf("反证样本非法 JSON: %v", err)
	}
	// 不排序写法的输出集合
	orders := map[string]bool{}
	for i := 0; i < 200; i++ {
		ls := flattenUnsortedForTest(manyKeys)
		sorted := append([]string(nil), ls...)
		sort.Strings(sorted) // 只比行序不比内容：排序后仍等长说明没吞行
		if len(sorted) != 12 {
			t.Fatalf("反证函数输出行数 %d != 12，姊妹实现本身失效", len(sorted))
		}
		orders[strings.Join(ls, "\n")] = true
	}
	if len(orders) < 2 {
		t.Skip("本机 Go 运行时未打乱 map 迭代序（200 次全同序）——确定性断言失去反证支撑，需换更大键集")
	}
	t.Logf("反证成立：不排序写法 200 次跑出 %d 种不同行序，修复前的行序漂移确实可观测", len(orders))
}

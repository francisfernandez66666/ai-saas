// 错误码单点真源的结构性守卫（2026-09-25 残项3）。
//
// 这个包没有业务逻辑，All() 也只是把常量列一遍——真正值得测的是**全仓的写法**：
// "后端会发哪些码"这件事只要还能在别处写第二遍，前端那份清单就又退化成抄件。
// 本测用 go/ast 扫全仓非测试源码，钉四件事：
//
//	① 响应体里 error_code 的值不得是裸字符串字面量（必须引用本包常量，或经
//	   codeNameFromCode 这类推导函数）——封掉"字面量散在各处"那条老路；
//	② 本包常量声明与 All() 清单双向对齐（加了常量忘了进清单 = 后端会发一个
//	   生成物和前端都不知道；清单里手写一个没常量的串 = 一格永远发不出的死码）；
//	③ All() 里每个码都必须在非测试源码里被真的引用（死码会让前端陪着一格命中不了的文案）；
//	④ 反证：检查器必须真能抓到店面上那个 `"error_code": "xxx"`。
//	   缺④，前三条会在"扫描面为空/键名判据失配"时集体空转——本仓在契约对账器上
//	   已经踩过一次"解析出零字段却报零差异"，这次把自证写成用例。
//
// 为什么不用 grep 做①：response.go 的文档注释里就写着
// `{"code":500,"message":safeMsg,"error_code":"internal_error"}`，负向 grep 会把它
// 当成违规点（注释不是发射点，见 [[static-negative-locks]] 那类教训）。
// AST 只看真实代码结构，天然不误伤说明文字。
package errcodes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// errcodesImportPath 导入路径：文件是否"已接入单点真源"的判据之一
const errcodesImportPath = "ai-scrm/internal/errcodes"

// emitSite 一处 error_code 发射点
type emitSite struct {
	file string
	expr string // 值的文本化形态：`"xxx"` / errcodes.Xxx / fn(...) / varName
}

// exprString 把表达式渲染成一行短文本用于报错定位（不追求语法完整）
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return exprString(v.Fun) + "(...)"
	default:
		return "<expr>"
	}
}

// scanErrorCodes 解析一个文件，返回其中所有 error_code 发射点。
//
// 只认 map 字面量里的键值对（键为字符串字面量 "error_code"）——这正是全仓写
// error_code 的唯一形态；RespErr 那类统一封装不带字面量码（由 codeNameFromCode
// 按 HTTP 码推导），天然不在扫描面内。
func scanErrorCodes(fset *token.FileSet, file string, src []byte) ([]emitSite, error) {
	node, err := parser.ParseFile(fset, file, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var sites []emitSite
	ast.Inspect(node, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.BasicLit)
		if !ok {
			return true
		}
		if k, uerr := strconv.Unquote(key.Value); uerr != nil || k != "error_code" {
			return true
		}
		sites = append(sites, emitSite{file: file, expr: exprString(kv.Value)})
		return true
	})
	return sites, nil
}

// isAllowedValue 判定一处的值是不是"从单点真源来"的写法。
//
// 允许：errcodes.Xxx（直接引用常量）、函数调用（codeNameFromCode(code) 一类推导）、
// 以及**已 import 本包**的文件里的变量（openapi_auth.go 的 errCode：值由 errcodes 常量
// 赋来，中间隔了分支，AST 不做数据流分析，就用"该文件必须接入真源"这条约束兜住）。
// 拒绝：字符串字面量——那是"第二份码集"的源头。
func isAllowedValue(expr string, importsErrcodes bool) bool {
	if strings.HasPrefix(expr, `"`) {
		return false
	}
	if strings.HasPrefix(expr, "errcodes.") || strings.HasSuffix(expr, "(...)") {
		return true
	}
	return importsErrcodes
}

// repoRoot 定位仓库根（本包在 internal/errcodes 下）
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("定位仓库根失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓库根定位不对（%s 下没有 go.mod）: %v", root, err)
	}
	return root
}

// collectGoFiles 收集待扫描的 Go 源文件（跳过测试文件与构建产物目录）。
// 数量下限是**扫描面自证**：目录被改名/路径写错时结果会是"零违规"，那种绿比红更有害。
func collectGoFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	skipDirs := map[string]bool{".git": true, "node_modules": true, "dist": true, "data": true, "keys": true, "vendor": true}
	var files []string
	for _, sub := range []string{"internal", "cmd", "pkg", "seed", "tools"} {
		dir := filepath.Join(root, sub)
		if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // 子目录不存在（tools 下可能没有 .go）不算失败
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
			return nil
		}); err != nil {
			t.Fatalf("遍历 %s 失败: %v", dir, err)
		}
	}
	if len(files) < 100 {
		t.Fatalf("只扫到 %d 个 Go 源文件，扫描面本身就不对——零违规会在「什么都没扫到」上假绿", len(files))
	}
	return files
}

// TestNoBareErrorCodeLiterals ①：全仓 error_code 发射点必须引自本包。
func TestNoBareErrorCodeLiterals(t *testing.T) {
	fset := token.NewFileSet()
	total := 0
	for _, path := range collectGoFiles(t) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", path, err)
		}
		sites, err := scanErrorCodes(fset, path, src)
		if err != nil {
			// 任一处解析失败必须点名：整仓扫不动时本条与其他两条会集体空转
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		imports := strings.Contains(string(src), errcodesImportPath)
		for _, s := range sites {
			total++
			if !isAllowedValue(s.expr, imports) {
				t.Errorf("%s 的 error_code 值是裸写法 %q —— 码集只能在本包声明一次，"+
					"否则前端那份清单又变成抄件（改一处会漂移，正是残项3 的现场）", relOf(path), s.expr)
			}
		}
	}
	if total == 0 {
		t.Fatal("全仓一处 error_code 都没扫到：键名判据或扫描面失效，本测正在空转")
	}
	if total > 200 {
		t.Fatalf("扫到 %d 处 error_code 发射点，超出预期量级——多半是判据把别的键也吃进来了", total)
	}
}

// declaredConstValues 用 AST 读本包源文件里的常量声明，返回 值→常量名 映射。
//
// 为什么不直接手写一份对照表（测试里再抄一遍常量）：那正是残项3 要消灭的形态——
// 抄件与真源之间没有机制约束。扫源码拿声明，加一个常量它就自动多一项。
func declaredConstValues(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	out := map[string]string{}
	for _, name := range []string{"errcodes.go"} {
		node, err := parser.ParseFile(fset, filepath.Join(root, "internal", "errcodes", name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		for _, decl := range node.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, nm := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok {
						continue
					}
					if v, err := strconv.Unquote(lit.Value); err == nil {
						out[v] = nm.Name
					}
				}
			}
		}
	}
	if len(out) < 10 {
		t.Fatalf("只从本包常量声明里读到 %d 个码，解析判据已失效", len(out))
	}
	return out
}

// TestRegistryAndConstsAlign ②：All() 与本包常量声明双向等集。
func TestRegistryAndConstsAlign(t *testing.T) {
	declared := declaredConstValues(t)
	listed := All()
	if len(listed) != len(declared) {
		t.Fatalf("All() 有 %d 项、常量声明有 %d 项——两个集合必须同源，加/删码要一起动", len(listed), len(declared))
	}
	inList := map[string]bool{}
	for _, c := range listed {
		inList[c] = true
	}
	for v, nm := range declared {
		if !inList[v] {
			t.Errorf("常量 %s = %q 未进 All() 清单：后端会发一个生成物里没有、前端查不到的码", nm, v)
		}
	}
	for _, c := range listed {
		if _, ok := declared[c]; !ok {
			t.Errorf("All() 里的 %q 没有对应常量声明：清单会生成一个永远发不出的码，"+
				"前端就得跟着维护一格死文案", c)
		}
	}
}

// countConstReferences 解析一个文件，统计其中对 errcodes 包常量的引用次数（按常量名）。
//
// 用 AST 的 SelectorExpr 而不是逐行找 "errcodes." 文本：行扫描会把注释里的举例、
// 字符串里的路径也算成引用，那样死码锁就在噪声上假绿。
func countConstReferences(fset *token.FileSet, file string, src []byte) (map[string]int, error) {
	node, err := parser.ParseFile(fset, file, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	used := map[string]int{}
	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "errcodes" {
			used[sel.Sel.Name]++
		}
		return true
	})
	return used, nil
}

// TestEveryRegisteredCodeIsActuallyUsed ③：清单里不得有死码（发射点真引用它）。
//
// 七个基础码也在其中——它们在 api/code.go 的 codeName 映射里被引用（映射本身就是发射点，
// 任何走统一响应封装的失败都经它），所以同样计入"被用到"。
func TestEveryRegisteredCodeIsActuallyUsed(t *testing.T) {
	fset := token.NewFileSet()
	used := map[string]int{}
	for _, path := range collectGoFiles(t) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", path, err)
		}
		refs, err := countConstReferences(fset, path, src)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		for name, n := range refs {
			used[name] += n
		}
	}
	if len(used) == 0 {
		t.Fatal("全仓零处 errcodes 常量引用：引用面判据失效，本条正在空转")
	}
	declared := declaredConstValues(t)
	for _, code := range All() {
		nm := declared[code]
		if used[nm] == 0 {
			t.Errorf("All() 登记了 %q（常量 %s）但非测试源码零引用——死码，前端那格文案永远命中不了", code, nm)
		}
	}
}

// TestScannerSelfEvidence ④ 反证：坏写法必须被抓到、正规写法必须被放行。
// 只测正向用例的护栏等于没有护栏——它在判据失效时同样报"全绿"。
func TestScannerSelfEvidence(t *testing.T) {
	fset := token.NewFileSet()
	bad := []byte(`package p
import "github.com/gin-gonic/gin"
func f(c *gin.Context) {
	c.JSON(400, gin.H{"code": 400, "error_code": "brand_new_code", "message": "x"})
}
`)
	sites, err := scanErrorCodes(fset, "bad.go", bad)
	if err != nil {
		t.Fatalf("解析合成样本失败: %v", err)
	}
	if len(sites) != 1 {
		t.Fatalf("合成样本应有 1 处发射点，实得 %d —— 键名判据已失效", len(sites))
	}
	if sites[0].expr != `"brand_new_code"` {
		t.Fatalf("合成样本的值被解析成 %q，判据取错了表达式", sites[0].expr)
	}
	if isAllowedValue(sites[0].expr, false) {
		t.Fatal("裸字面量被判为合法：单点真源的封口是空的")
	}
	if isAllowedValue(sites[0].expr, true) {
		t.Fatal("仅因「文件已接入真源」就放行裸字面量：漂移会从第二个 handler 开始")
	}
	good := []byte(`package p
import (
	"github.com/gin-gonic/gin"
	"ai-scrm/internal/errcodes"
)
func f(c *gin.Context) {
	c.JSON(400, gin.H{"code": 400, "error_code": errcodes.DealRejected})
}
`)
	sites2, err := scanErrorCodes(fset, "good.go", good)
	if err != nil || len(sites2) != 1 {
		t.Fatalf("正向样本解析异常: sites=%d err=%v", len(sites2), err)
	}
	if !isAllowedValue(sites2[0].expr, true) {
		t.Fatal("引用 errcodes 常量的正规写法被判违规：门禁过强会把人推回去写字面量")
	}
	// 推导函数与注释文本都不该被误判
	derived := []byte(`package p
import "github.com/gin-gonic/gin"
func f(c *gin.Context) { c.JSON(400, gin.H{"error_code": codeNameFromCode(400)}) }
`)
	sites3, err := scanErrorCodes(fset, "derived.go", derived)
	if err != nil || len(sites3) != 1 || !isAllowedValue(sites3[0].expr, false) {
		t.Fatalf("推导函数写法被误判: sites=%+v err=%v", sites3, err)
	}
}

// TestAllIsSortedAndDeduped 生成物参与 git diff，顺序漂移会让每次重生成产生整片假 diff，
// 把"真加了一个码"埋在噪声里。
func TestAllIsSortedAndDeduped(t *testing.T) {
	got := All()
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("All() 未严格升序或含重复项：%v", got)
		}
	}
}

// relOf 报错信息里用相对路径，绝对路径会把整棵树的长度带进日志
func relOf(path string) string {
	i := strings.LastIndex(path, "/internal/")
	if i < 0 {
		i = strings.LastIndex(path, "/cmd/")
	}
	if i < 0 {
		return filepath.Base(path)
	}
	return "." + path[i:]
}

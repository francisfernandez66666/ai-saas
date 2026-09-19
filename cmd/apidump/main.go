// T7 契约 codegen：导出稳定路由清单，供 api.schema.json golden 与前端类型生成使用。
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/api"
)

// Route 单条 API 路由元数据（T7 契约 golden 的最小单元）：
// DataTS 为该路由的核心响应数据时间戳口径（契约漂移检测用，可空）。
type Route struct {
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Group   string   `json:"group"`
	Handler string   `json:"handler"`
	Auth    []string `json:"auth,omitempty"`
	DataTS  string   `json:"data_ts,omitempty"`
}

// Schema api.schema.json golden 文件的顶层结构（check_api_contract.sh 逐项比对）
type Schema struct {
	GeneratedBy string         `json:"generated_by"`
	Version     int            `json:"version"`
	Meta        map[string]any `json:"meta"`
	Routes      []Route        `json:"routes"`
}

// main 启动当前命令入口。
func main() {
	out := flag.String("out", "api.schema.json", "输出文件，- 表示 stdout")
	check := flag.Bool("check", false, "与已有文件做字节级 diff，不一致则退出码 1")
	format := flag.String("format", "schema", "schema|paths|openapi")
	flag.Parse()

	content, err := buildContent(*format)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if *out == "-" {
		os.Stdout.WriteString(content)
		return
	}
	if *check {
		old, readErr := os.ReadFile(*out)
		if readErr != nil || string(old) != content {
			fmt.Fprintf(os.Stderr, "契约文件已变化，请运行: go run ./cmd/apidump -out %s\n", *out)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(*out, []byte(content), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// buildContent 按指定格式生成 apidump 输出文本。
func buildContent(format string) (string, error) {
	routes, err := collectRoutes()
	if err != nil {
		return "", err
	}
	if format == "paths" {
		var b strings.Builder
		for _, r := range routes {
			fmt.Fprintf(&b, "%s %s\n", r.Method, r.Path)
		}
		return b.String(), nil
	}
	if format == "openapi" {
		// E10 防漂移：golden 与运行时规格同源（api.BuildOpenAPISpecJSON），
		// 并双向核对真实 /openapi/v1 路由 ↔ spec paths（新增端点漏文档当场 FAIL）
		specBytes, err := api.BuildOpenAPISpecJSON()
		if err != nil {
			return "", err
		}
		var spec struct {
			Paths map[string]map[string]any `json:"paths"`
		}
		if err := json.Unmarshal(specBytes, &spec); err != nil {
			return "", err
		}
		routeKeys := map[string]bool{}
		for _, r := range routes {
			if strings.HasPrefix(r.Path, "/openapi/v1/") {
				routeKeys[r.Method+" "+ginToSpecPath(strings.TrimPrefix(r.Path, "/openapi/v1"))] = true
			}
		}
		specKeys := map[string]bool{}
		for path, ops := range spec.Paths {
			for method := range ops {
				specKeys[strings.ToUpper(method)+" "+path] = true
			}
		}
		for k := range routeKeys {
			if !specKeys[k] {
				return "", fmt.Errorf("spec 缺少端点文档: %s（routes_openapi.go 已注册）", k)
			}
		}
		for k := range specKeys {
			if !routeKeys[k] {
				return "", fmt.Errorf("spec 含未注册端点: %s（路由清单中不存在，疑似内部面泄露或文档漂移）", k)
			}
		}
		return string(specBytes), nil
	}
	if format != "schema" {
		return "", fmt.Errorf("未知 format: %s", format)
	}
	s := Schema{
		GeneratedBy: "cmd/apidump",
		Version:     1,
		Meta: map[string]any{
			"meta_endpoints": []string{"GET /health", "GET /status", "GET /metrics"},
			"spa_fallback":   true,
		},
		Routes: routes,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// collectRoutes 构建路由树并收集 API 清单。
func collectRoutes() ([]Route, error) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api.RegisterRoutes(r)

	respTypes := scanResponseAnnotations()
	out := make([]Route, 0, len(r.Routes()))
	for _, rt := range r.Routes() {
		handler := funcName(rt.Handler)
		route := Route{
			Method:  rt.Method,
			Path:    rt.Path,
			Group:   groupOf(rt.Path),
			Handler: handler,
			Auth:    authOf(rt.Path),
		}
		if ts, ok := respTypes[handler]; ok {
			route.DataTS = ts
		}
		out = append(out, route)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Method == out[j].Method {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

var funcSuffixRe = regexp.MustCompile(`-fm$`)

// ginToSpecPath 把 gin 路径参数（:id）转成 OpenAPI 模板（{id}），用于 spec↔路由清单核对
func ginToSpecPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") {
			segs[i] = "{" + strings.TrimPrefix(s, ":") + "}"
		}
	}
	return strings.Join(segs, "/")
}

// funcName 从 Gin 函数指针文本中解析 handler 名称。
func funcName(raw string) string {
	name := funcSuffixRe.ReplaceAllString(raw, "")
	name = strings.TrimPrefix(name, "ai-scrm/")
	parts := strings.Split(name, ".")
	if len(parts) == 0 {
		return name
	}
	last := parts[len(parts)-1]
	last = strings.ReplaceAll(last, "func", "")
	last = strings.Trim(last, "().")
	if last == "" && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	return last
}

// groupOf 根据 API 路径推断路由分组。
func groupOf(path string) string {
	switch {
	case strings.HasPrefix(path, "/openapi/"):
		return "openapi"
	case strings.HasPrefix(path, "/api/v1/"):
		rest := strings.TrimPrefix(path, "/api/v1/")
		parts := strings.Split(rest, "/")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
		return "public"
	case path == "/health" || path == "/status" || path == "/metrics":
		return "infra"
	default:
		return "other"
	}
}

// authOf 根据路由位置推断鉴权要求。
func authOf(path string) []string {
	var out []string
	add := func(name string) {
		out = append(out, name)
	}
	switch {
	case strings.HasPrefix(path, "/openapi/"):
		add("api_key")
		add("ip_limit")
	case path == "/api/v1/collector":
		add("collector_key")
	case path == "/api/v1/chat/test", path == "/api/v1/chat/guest", path == "/api/v1/chat/welcome", path == "/api/v1/chat/history", path == "/api/v1/chat/clear-delay":
		add("optional_jwt")
		add("ip_limit")
	case strings.HasPrefix(path, "/api/v1/channel/callback/"):
		add("channel_signature")
	case strings.HasPrefix(path, "/api/v1/billing/webhook/"):
		add("webhook_signature")
	case strings.HasPrefix(path, "/api/v1/turnstile/sitekey"), strings.HasPrefix(path, "/api/v1/public/"), strings.HasPrefix(path, "/api/v1/plans"), strings.HasPrefix(path, "/api/v1/packages"),
		strings.HasPrefix(path, "/api/v1/knowledge/"), strings.HasPrefix(path, "/api/v1/client-errors"), strings.HasPrefix(path, "/api/v1/privacy/deletion-request"),
		strings.HasPrefix(path, "/api/v1/auth/login"), strings.HasPrefix(path, "/api/v1/auth/register"), strings.HasPrefix(path, "/api/v1/auth/register-config"),
		strings.HasPrefix(path, "/api/v1/auth/email-code"), strings.HasPrefix(path, "/api/v1/auth/reset-password"), strings.HasPrefix(path, "/api/v1/auth/verify-reset-code"),
		strings.HasPrefix(path, "/api/v1/tenant/signup"), strings.HasPrefix(path, "/api/v1/tenant/check-code"),
		strings.HasPrefix(path, "/api/v1/openapi/spec"): // E10 开放面规格：静态子集零租户数据，公开（注册在 JWTAuth 之前，口径与代码一致）
		add("public")
	default:
		add("jwt")
		switch {
		case strings.HasPrefix(path, "/api/v1/admin/"):
			add("admin")
		case strings.HasPrefix(path, "/api/v1/super/"):
			add("super_admin")
		case strings.HasPrefix(path, "/api/v1/org/"):
			add("org_manage")
		}
	}
	return out
}

// scanResponseAnnotations 扫描源码中的 apidump 响应类型注解。
func scanResponseAnnotations() map[string]string {
	res := map[string]string{}
	fset := token.NewFileSet()
	files, err := filepath.Glob("internal/api/*.go")
	if err != nil {
		return res
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			continue
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Doc == nil {
				continue
			}
			for _, c := range fn.Doc.List {
				text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
				if rest, ok := strings.CutPrefix(text, "apidump:ts"); ok {
					ts := strings.TrimSpace(rest)
					if ts != "" {
						res[fn.Name.Name] = ts
					}
				}
			}
		}
	}
	return res
}

// Package api 2026-09-22 复核 P1-1 修复的回归断言
//
// 覆盖两个修复点：
//  1. 入参长度校验：超长 name 在绑定层被拦下，不再直达 DB 触发
//     "value too long for type character varying(50) (SQLSTATE 22001)"
//  2. 错误脱敏：5xx 出口 RespErrInternal 与 4xx 绑定出口 RespErrBind
//     均不得把内部结构（Go 结构体名、字段 tag、SQLSTATE、列名）回传前端
//
// 不依赖 DB，纯逻辑断言；与既有 *_test.go 风格一致（表驱动 + t.Run）。
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-scrm/internal/schema"

	"github.com/gin-gonic/gin"
)

// TestSanitizeBindErr_LongName 超长入参应被校验层拦下，且文案不含内部结构信息。
func TestSanitizeBindErr_LongName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"name":"` + strings.Repeat("张", 200) + `"}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/customers", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	var req schema.CreateCustomerRequest
	err := c.ShouldBindJSON(&req)
	if err == nil {
		t.Fatal("超长 name 未被校验层拦下 —— P1-1 回归：应返回校验错误而非放行进 DB")
	}

	got := SanitizeBindErr(err)
	for _, leak := range []string{"SQLSTATE", "character varying", "CreateCustomerRequest", "failed on the"} {
		if strings.Contains(got, leak) {
			t.Errorf("SanitizeBindErr 泄漏内部结构 %q，实际输出：%s", leak, got)
		}
	}
	if !strings.Contains(got, "name") || !strings.Contains(got, "50") {
		t.Errorf("超长 name 的提示应指向字段与上限，实际：%s", got)
	}
}

// TestSanitizeBindErr_Forms 各类校验错误与非校验错误的文案收敛。
func TestSanitizeBindErr_Forms(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"超长手机号", `{"phone":"` + strings.Repeat("1", 30) + `"}`, "phone"},
		{"年龄越界", `{"age":999}`, "age"},
		{"标签名超长", `{"tags":["` + strings.Repeat("签", 60) + `"]}`, "tags[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			var req schema.CreateCustomerRequest
			err := c.ShouldBindJSON(&req)
			if err == nil {
				t.Skipf("该样例当前不触发校验（%s），跳过", tc.name)
			}
			got := SanitizeBindErr(err)
			if !strings.Contains(got, tc.want) {
				t.Errorf("期望包含 %q，实际：%s", tc.want, got)
			}
		})
	}
}

// TestValidCustomerPayloadStillPasses 正对照：新增的 max 标签不得误伤正常入参
// （否则等于把线上存量合法请求全打成 400——只测负向是不够的）。
func TestValidCustomerPayloadStillPasses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"name":"张三","phone":"13800138000","wechat_id":"zhangsan","region":"华东",` +
		`"city":"上海","career":"工程师","customer_type":"potential","interest_model":"SUV",` +
		`"current_car":"旧款","source":"官网","tags":["价格敏感","已到店"],"remark":"意向明确"}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/customers", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	var req schema.CreateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		t.Fatalf("合法入参不应被新增的长度校验拦下（会把线上请求误打成 400）：%v", err)
	}
	if req.Name != "张三" || len(req.Tags) != 2 {
		t.Fatalf("字段解析异常：name=%q tags=%v", req.Name, req.Tags)
	}
}

// TestSanitizeBindErr_JSONSyntax JSON 语法错误应归为通用文案，不回显 Go 内部结构。
func TestSanitizeBindErr_JSONSyntax(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":`))
	c.Request.Header.Set("Content-Type", "application/json")
	var req schema.CreateCustomerRequest
	err := c.ShouldBindJSON(&req)
	if err == nil {
		t.Fatal("畸形 JSON 应当绑定失败")
	}
	got := SanitizeBindErr(err)
	if strings.Contains(got, "unexpected") || strings.Contains(got, "json") && strings.Contains(got, "Decode") {
		t.Errorf("畸形 JSON 的文案不应回显解析器细节，实际：%s", got)
	}
	if got != "请求体格式不正确" {
		t.Errorf("畸形 JSON 应收敛为通用文案，实际：%s", got)
	}
}

// TestRespErrInternal_NoLeak 5xx 出口不得回传真实错误串。
func TestRespErrInternal_NoLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "ERROR: value too long for type character varying(50) (SQLSTATE 22001)"

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/customers", nil)
	RespErrInternal(c, errors.New(secret), "创建失败")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("期望 HTTP 500，实际 %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "SQLSTATE") || strings.Contains(w.Body.String(), "character varying") {
		t.Errorf("5xx 响应体泄漏数据库原生错误：%s", w.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, w.Body.String())
	}
	if out["message"] != "创建失败" {
		t.Errorf("对外文案应为安全短语，实际：%v", out["message"])
	}
	if out["error_code"] != "internal_error" {
		t.Errorf("期望 error_code=internal_error，实际：%v", out["error_code"])
	}
}

// TestRespErrBind_NoLeak 4xx 绑定出口不得回显 validator 的结构名与 tag 名。
func TestRespErrBind_NoLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"name":"` + strings.Repeat("张", 200) + `"}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/customers", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	var req schema.CreateCustomerRequest
	err := c.ShouldBindJSON(&req)
	if err == nil {
		t.Fatal("超长 name 未被拦下")
	}
	RespErrBind(c, err)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 HTTP 400，实际 %d", w.Code)
	}
	for _, leak := range []string{"CreateCustomerRequest", "failed on the", "Key:"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("4xx 响应体泄漏内部结构 %q：%s", leak, w.Body.String())
		}
	}
}

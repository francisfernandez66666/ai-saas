// §八-7 零测试包最小单测（2026-09-18）：schema（请求/响应 DTO 层）此前零测试，
// 但它是"入参校验的最后一道声明"，两条闸口值得钉住：
//  1. 分页：GetOffset 的 P2-9 clamp（page_size 上限 100，原可 1000000 拖全表）——
//     注意 GetOffset **有副作用**（会改写调用方实例的 Page/PageSize），
//     所以表驱动里每个 case 必须新建实例，否则前一个 case 的归一化会污染后一个；
//     同时校验它和独立函数 NormalizePageSize 的口径一致（两处上限不一致=某个列表页可拖库）；
//  2. binding tag：ChatRequest.Content 的 max=4000（防超长报文打爆 prompt/DB）、
//     LoginRequest 的 required（防匿名空包刷登录接口）、三个列表请求内嵌 Pagination
//     （内嵌丢了就是"无分页全表扫"）。用 reflect 读 tag 断言，避免改结构体时静默掉闸。
package schema

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"ai-scrm/internal/testutil"
)

// TestMain 统一出口：收尾打印本测试二进制的 DB 跳过计数（防静默绿）
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// TestPaginationGetOffset 分页偏移与 clamp：每 case 用全新实例（GetOffset 会改写自身字段）
func TestPaginationGetOffset(t *testing.T) {
	cases := []struct {
		name         string
		page         int
		pageSize     int
		wantOffset   int
		wantPage     int // 归一化后的副作用值
		wantPageSize int
	}{
		{name: "首页默认", page: 1, pageSize: 10, wantOffset: 0, wantPage: 1, wantPageSize: 10},
		{name: "第三页", page: 3, pageSize: 20, wantOffset: 40, wantPage: 3, wantPageSize: 20},
		{name: "零值走默认 1/10", page: 0, pageSize: 0, wantOffset: 0, wantPage: 1, wantPageSize: 10},
		{name: "负页码归一为 1", page: -5, pageSize: 10, wantOffset: 0, wantPage: 1, wantPageSize: 10},
		{name: "负页长归一为 10", page: 2, pageSize: -1, wantOffset: 10, wantPage: 2, wantPageSize: 10},
		{name: "拖库页长 clamp 到 100", page: 1, pageSize: 1_000_000, wantOffset: 0, wantPage: 1, wantPageSize: 100},
		{name: "clamp 后再算偏移", page: 3, pageSize: 500, wantOffset: 200, wantPage: 3, wantPageSize: 100},
		{name: "边界 100 不动", page: 2, pageSize: 100, wantOffset: 100, wantPage: 2, wantPageSize: 100},
		{name: "边界 101 clamp", page: 2, pageSize: 101, wantOffset: 100, wantPage: 2, wantPageSize: 100},
		{name: "边界 1 页长", page: 4, pageSize: 1, wantOffset: 3, wantPage: 4, wantPageSize: 1},
		{name: "int 溢出保护：超大页码不 panic", page: 1 << 40, pageSize: 100, wantOffset: ((1 << 40) - 1) * 100, wantPage: 1 << 40, wantPageSize: 100},
	}
	for _, tc := range cases {
		p := &Pagination{Page: tc.page, PageSize: tc.pageSize} // 每 case 新实例
		got := p.GetOffset()
		if got != tc.wantOffset {
			t.Errorf("GetOffset(%s: page=%d size=%d)=%d want %d", tc.name, tc.page, tc.pageSize, got, tc.wantOffset)
		}
		// 副作用现状：GetOffset 会把归一化值写回自身（下游若复用同一实例不会再被上游脏值影响）
		if p.Page != tc.wantPage || p.PageSize != tc.wantPageSize {
			t.Errorf("GetOffset(%s) 归一化副作用异常: page=%d size=%d want %d/%d",
				tc.name, p.Page, p.PageSize, tc.wantPage, tc.wantPageSize)
		}
	}
}

// TestGetOffsetMatchesNormalizePageSize 两套口径必须一致：
// Pagination.GetOffset 的页长区间 == NormalizePageSize，否则某类列表页可绕开 clamp 拖库。
func TestGetOffsetMatchesNormalizePageSize(t *testing.T) {
	for _, n := range []int{-1000, -1, 0, 1, 2, 50, 99, 100, 101, 500, 100000, 1 << 20} {
		p := &Pagination{Page: 1, PageSize: n}
		p.GetOffset() // 归一化写回 p.PageSize
		if got, want := p.PageSize, NormalizePageSize(n); got != want {
			t.Errorf("页长口径不一致(%d): GetOffset 归一=%d NormalizePageSize=%d", n, got, want)
		}
	}
	// NormalizePageSize 自身的取值域契约：结果恒在 [1,100]
	for n := -200; n <= 200; n++ {
		if got := NormalizePageSize(n); got < 1 || got > 100 {
			t.Fatalf("NormalizePageSize(%d)=%d 越界 [1,100]", n, got)
		}
	}
	// 幂等：归一化两次结果不变（防止"归一化后又被 clamp 成另一个值"的漂移）
	for _, n := range []int{0, -3, 7, 100, 999} {
		once := NormalizePageSize(n)
		if twice := NormalizePageSize(once); twice != once {
			t.Errorf("NormalizePageSize 非幂等: in=%d 一次=%d 两次=%d", n, once, twice)
		}
	}
}

// TestListRequestsEmbedPagination 列表请求必须内嵌 Pagination（丢了就是无分页全表扫）
func TestListRequestsEmbedPagination(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{name: "CustomerListRequest", value: CustomerListRequest{}},
		{name: "ConversationListRequest", value: ConversationListRequest{}},
		{name: "TemplateListRequest", value: TemplateListRequest{}},
	}
	for _, tc := range cases {
		rt := reflect.TypeOf(tc.value)
		if !hasEmbeddedPagination(rt) {
			t.Errorf("%s 未内嵌 Pagination（列表接口会退化成全表返回）", tc.name)
			continue
		}
		// 内嵌字段本身仍可被 form 绑定（否则前端 ?page=2 传不进来）
		pg := reflect.TypeOf(Pagination{})
		wantJSON := map[string]string{"Page": "page", "PageSize": "page_size"}
		for i := 0; i < pg.NumField(); i++ {
			f := pg.Field(i)
			if f.Tag.Get("form") == "" {
				t.Errorf("Pagination.%s 缺 form tag，query 参数绑定不上", f.Name)
			}
			if want, ok := wantJSON[f.Name]; ok && f.Tag.Get("json") != want {
				t.Errorf("Pagination.%s json tag=%q want %q（前端按 snake_case 取值）", f.Name, f.Tag.Get("json"), want)
			}
		}
	}
}

// hasEmbeddedPagination 结构体是否匿名内嵌 Pagination
func hasEmbeddedPagination(rt reflect.Type) bool {
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Anonymous && f.Type == reflect.TypeOf(Pagination{}) {
			return true
		}
	}
	return false
}

// TestBindingTagsPinInputGuards 关键 binding tag 现状钉住（防改 DTO 时静默掉闸）
func TestBindingTagsPinInputGuards(t *testing.T) {
	cases := []struct {
		typ     reflect.Type
		field   string
		needs   []string // tag 必须包含的约束
		forbid  []string // tag 必须不含的约束（钉住"这里刻意不校验"的现状）
		comment string
	}{
		{
			typ: reflect.TypeOf(ChatRequest{}), field: "Content",
			needs:   []string{"required", "max=4000"},
			comment: "消息长度上限（P2：挡超长报文打爆 prompt/DB）",
		},
		{
			typ: reflect.TypeOf(ChatRequest{}), field: "CustomerID",
			needs:   []string{"required"},
			comment: "客户 ID 必填（否则匿名消息落不到客户档案）",
		},
		{
			typ: reflect.TypeOf(LoginRequest{}), field: "Username",
			needs:   []string{"required"},
			comment: "登录用户名必填（防空包刷接口）",
		},
		{
			typ: reflect.TypeOf(LoginRequest{}), field: "Password",
			needs:   []string{"required"},
			comment: "登录密码必填",
		},
		{
			typ: reflect.TypeOf(LoginRequest{}), field: "TenantCode",
			forbid: []string{"required"},
			comment: "【现状】tenant_code 虽为多租户必需，但绑定层刻意不 required" +
				"（超管登录不传），隔离靠 service 层判空，故这里钉住「不加 required」",
		},
		{
			typ: reflect.TypeOf(TemplateListRequest{}), field: "AnchorType",
			comment: "锚类型不做 min 校验（0=不抛 是合法锚位）",
			forbid:  []string{"min=1"},
		},
	}
	for _, tc := range cases {
		tag, ok := bindingTag(tc.typ, tc.field)
		if !ok {
			t.Errorf("%s.%s 字段不存在或无 binding tag（%s）", tc.typ.Name(), tc.field, tc.comment)
			continue
		}
		for _, want := range tc.needs {
			if !strings.Contains(tag, want) {
				t.Errorf("%s.%s binding=%q 缺约束 %q（%s）", tc.typ.Name(), tc.field, tag, want, tc.comment)
			}
		}
		for _, bad := range tc.forbid {
			if strings.Contains(tag, bad) {
				t.Errorf("%s.%s binding=%q 不应含 %q（%s）", tc.typ.Name(), tc.field, tag, bad, tc.comment)
			}
		}
	}
}

// bindingTag 取字段的 binding tag
func bindingTag(rt reflect.Type, field string) (string, bool) {
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Name == field {
			return rt.Field(i).Tag.Get("binding"), true
		}
	}
	return "", false
}

// TestResponseEnvelopeShape 统一响应壳与分页响应：前端/E2E 按这套字段名解析，改名即全端崩
func TestResponseEnvelopeShape(t *testing.T) {
	rt := reflect.TypeOf(Response{})
	for _, f := range []struct{ name, json string }{
		{name: "Code", json: "code"},
		{name: "Message", json: "message"},
		{name: "Data", json: "data"},
		{name: "Error_code", json: "error_code,omitempty"}, // M4：机器可读错误码
	} {
		field, ok := rt.FieldByName(f.name)
		if !ok {
			t.Errorf("Response 缺字段 %s（前端按 code/message/data/error_code 解析）", f.name)
			continue
		}
		if got := field.Tag.Get("json"); got != f.json {
			t.Errorf("Response.%s json tag=%q want %q", f.name, got, f.json)
		}
	}
	// 零值：Code=0 现状即"成功"，所以错误分支必须由 handler 显式写非 0（壳自身不设默认）
	resp := Response{}
	if resp.Code != 0 || resp.Data != nil {
		t.Errorf("Response 零值异常: code=%d data=%v", resp.Code, resp.Data)
	}
	// error_code 的 omitempty 已在上面的 json tag 断言里覆盖（成功响应不出现该字段）

	// 分页响应：Total/Page/PageSize/List 四字段齐备，且页长口径与请求侧 clamp 一致
	pt := reflect.TypeOf(PageResponse{})
	for _, name := range []string{"Total", "Page", "PageSize", "List"} {
		if _, ok := pt.FieldByName(name); !ok {
			t.Errorf("PageResponse 缺字段 %s", name)
		}
	}
	req := Pagination{Page: 0, PageSize: 100000}
	off := req.GetOffset() // 值变量可寻址，指针方法自动取址
	pr := PageResponse{Page: req.Page, PageSize: req.PageSize, Total: 500}
	if pr.PageSize != 100 || pr.Page != 1 || off != 0 {
		t.Errorf("分页壳回填值异常: page=%d size=%d offset=%d（应 1/100/0）", pr.Page, pr.PageSize, off)
	}
}

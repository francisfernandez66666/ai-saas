// 活码落地链接（获客批 · 批次3，2026-09-23）：二维码里到底印哪个域名。
//
// 这件事看着小，出事很难查：海报印出去之后链接改不动，客户扫进去是别家的对话页，
// 归因闸（码所属租户 ≠ 请求租户即拒写）会把这一批流量全部记不上——
// 销售只会看到"扫的人不少、进来的客户为零"，而数据层看起来一切正常。
// 所以基址必须**在生成时就确定、可解释、且租户能自己指定**，而不是取"谁在什么机器上点了生成"。
//
// 优先级（从高到低）：
//  1. 租户热配 acquisition_link_base（运营显式指定，如 https://go.acme.com）；
//  2. 租户白标自定义域名 custom_domain（已有品牌域，不必再配一遍）；
//  3. 请求 Host（本地开发与"就用当前站点"的场景；生产多租户共域时这是最后兜底，不可靠）。
package acquisition

import (
	"strings"

	"ai-scrm/internal/runtimecfg"
)

// LinkBaseKeyTenantConfig 租户级链接基址热配键（出厂空串=未设）。
const LinkBaseKeyTenantConfig = "acquisition_link_base"

// normalizeBase 规范化基址：去空白、去尾部斜杠、缺协议则补 https。
// 补 https 而不是原样返回：运营在后台填 "go.acme.com" 是常态，
// 拼进二维码会生成一个没有协议的字符串，微信里点开是纯文本而不是链接。
func normalizeBase(raw string) string {
	b := strings.TrimRight(strings.TrimSpace(raw), "/")
	if b == "" {
		return ""
	}
	if !strings.HasPrefix(b, "http://") && !strings.HasPrefix(b, "https://") {
		b = "https://" + b
	}
	return b
}

// TenantLinkBase 读租户配置的活码链接基址（未配置返回空串）。
// nil 服务守卫照 outreach 口径：单测/启动早期配置中心未装配时回退默认值，不 panic。
func TenantLinkBase(tenantID uint) string {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return ""
	}
	return normalizeBase(svc.GetStringForTenant(tenantID, LinkBaseKeyTenantConfig, ""))
}

// ResolveLinkBase 按三级优先级解析出链接基址（可能为空串=三级都没落到绝对地址）。
// 单独暴露给管理端：出二维码之外还要能如实告诉运营"这个链接的域名是从哪来的"，
// 空串更要显式说明——物料印的是相对路径等于印了一张废码。
func ResolveLinkBase(tenantID uint, customDomain, requestBase string) string {
	if b := TenantLinkBase(tenantID); b != "" {
		return b
	}
	if b := normalizeBase(customDomain); b != "" {
		return b
	}
	return normalizeBase(requestBase)
}

// BuildLandingLink 拼出扫码后的完整落地链接。
// requestBase 由调用方从请求上下文取（scheme + Host），customDomain 是租户白标域名（可空）。
//
// 三级全空时退回相对路径（见 BuildLink）：接口仍能出图，但管理端会把
// "当前未配置落地域名"这句 note 一并下发——不显示出来的话，代价是一次重印。
func BuildLandingLink(tenantID uint, customDomain, requestBase, code string) string {
	return BuildLink(ResolveLinkBase(tenantID, customDomain, requestBase), code)
}

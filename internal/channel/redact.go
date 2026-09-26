// 本文件提供通道层错误串的凭据脱敏收口（2026-09-26 全量审计 FIX-2）。
package channel

import (
	"errors"

	"ai-scrm/internal/pii"
)

// ============================================================
// 为什么在通道层单独包一层，而不是每个调用点各自 strings.Replace
//
// 泄露源头是 Go 的 *url.Error：http.Client 把传输错误包成 url.Error 时，Error() 会
// **重新拼出完整 URL 含 query**，而换取 access_token / 发消息的 URL query 里就带着
// corpsecret / access_token。于是：
//
//	doToken 网络失败 → SendResult{Err} → outbound.truncateErr（只截长度）
//	→ channel_outbound.error 列（json:"error"）→ /admin/channels/dead-letters 整行回显
//	→ 管理端界面与 ai-scrm.log 同时出现客户应用密钥明文。
//
// 只在 outbound 落库处脱敏不够：入站/JS-SDK/存档同步的错误串各有自己的出口（接口、日志、
// 观测位），漏一条就是新的泄露面。所以收口放在**错误产生处**（每个 chanHTTP.Do 的返回），
// 落库与回显再各兜一层（防御式，两处都脱敏才不怕后来人新增调用点）。
//
// 注意保留错误类型：通道业务错误（*ErrTokenFailed）走的是"响应体 errcode"分支，不是网络
// 错误，不经本函数；而 isTokenInvalidErr 依赖 errors.As 拿到该类型做 -1 强制刷新重试，
// 所以只有"确实脱敏过"才换成新错误串，否则原样返回，绝不把类型信息洗掉。
// ============================================================

// redactErr 把错误串里的凭据参数值替成 ***；无凭据命中时原样返回（保留错误类型）。
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	raw := err.Error()
	clean := pii.RedactSecretURL(raw)
	if clean == raw {
		return err
	}
	return errors.New(clean)
}

// redactText 用于日志/落库前的字符串兜底脱敏（已脱敏的文本重复调用幂等）。
func redactText(s string) string {
	return pii.RedactSecretURL(s)
}

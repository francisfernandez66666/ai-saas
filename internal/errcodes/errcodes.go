// Package errcodes 是机器可读错误码（响应体 error_code 字段）的唯一真源。
//
// 为什么要单独一个包（2026-09-25 残项3 收口）：
// 同一个"后端会发哪些码"这件事，此前散在四处——
//
//	① internal/api/code.go 的 codeName 映射；
//	② middleware/auth.go、org.go、openapi_auth.go 与各域 handler 里的字符串字面量；
//	③ 前端 lib/api.ts 的文案表 ERROR_CODE_MESSAGES；
//	④ 前端单测里**手抄**的一份 BACKEND_CODES 清单。
//
// ④ 是真正的坑：它测的是"抄来的清单和前端文案表是否一致"，不是"后端真发的码和前端
// 文案表是否一致"。后端加一个新码而前端忘登记时，这一样照样全绿——漂移的方向恰好就是
// 这张表存在的意义（新码静默走未登记分支：只回传 message、分级恒为 warning）。
//
// 现在的口径：码集只在本包声明一次。后端每一处发射点都引用这里的常量（改字面量必须过本包，
// 契约门禁 tools/check_api_contract.sh 第 4.5 段用负向 grep 锁死"不得再出现裸字面量"）；
// 前端拿到的是由 `go run ./cmd/apidump -format errorcodes` 生成的清单，
// 单测把生成物与文案表做**双向**对账（后端有而前端没登记 → 红；前端登记了而后端从不发 → 也红）。
//
// 依赖方向：本包是叶子包，不 import 项目内任何包。
// 之所以不放进 internal/api：middleware 也要引用这些常量，而 api 包已经 import middleware，
// 反向依赖会成环（同 internal/strategytypes 断 ai→strategy 耦合的做法）。
package errcodes

// 七位基础码：与 internal/api/code.go 的 RespCode 语义码一一对应，
// 由 codeNameFromCode 按 HTTP 码/语义码推导得出，任何走统一响应封装的失败都会带上其中之一。
const (
	ParamError    = "param_error"    // 参数校验失败（400 / 40001）
	Unauthorized  = "unauthorized"   // 未认证、token 无效（401 / 40101）
	Forbidden     = "forbidden"      // 已认证但无权限（403 / 40301）
	NotFound      = "not_found"      // 资源不存在（404 / 40401）
	RateLimited   = "rate_limited"   // 触发限流/防薅（429 / 42901）
	BizError      = "biz_error"      // 业务规则拒绝（409/42001，余额不足、幂等冲突等）
	InternalError = "internal_error" // 服务内部错误（500/50001，含第三方通信失败）
)

// 登录态与身份类码：由中间件在鉴权链上直接 abort 发出，不经统一响应封装，
// 所以字面量必须显式登记在这里，否则生成物会漏掉它们（前端把"重新登录"弹成黄色轻提示就是事故）。
const (
	TokenRevoked       = "token_revoked"        // 旧会话已被吊销（改密/换绑后），必须重新登录
	MustChangePassword = "must_change_password" // 出厂弱密码未改，除改密外一律拦下
	InvalidAPIKey      = "invalid_api_key"      // OpenAPI Key 缺失/无效/未绑租户
)

// 域码：某一业务域拒了这次动作。这些码的 message 本身就是精确人读文案
// （"阶段不能往回退…"），前端**只登记分级、不写通用话**，否则会把后端的具体原因盖掉
// ——这也是双向对账里"格子里没有 text"的那种合法形态，不是漏登记。
const (
	DealRejected            = "deal_rejected"             // 商机/报价状态机拒绝（reason 给具体档）
	OutreachRejected        = "outreach_rejected"         // 主动触达排期被裁决拒（开关/文案/频控/静默窗）
	AcquisitionRejected     = "acquisition_rejected"      // 获客活码建码被拒（渠道/名称/归因）
	PackTierDenied          = "pack_tier_denied"          // 行业包档位门槛未开放（G-22c）
	ChannelConfigIncomplete = "channel_config_incomplete" // 通道凭据缺项，侧边栏 JS-SDK 无法签名
)

// All 返回后端可能发出的全部机器可读错误码（已排序，不含成功码 "ok"）。
//
// 为什么没有 "ok"：code.go 的 codeName 映射里确实有 CodeOK→"ok"，但成功响应
// 根本不带 error_code 字段（codeNameFromCode(0) 返回空串，见 code.go 注释），
// 前端也就永远查不到它。把它列进来等于在文案表里留一格死码。
//
// 排序是刻意的：本函数的输出会落进契约生成物（frontend-react/src/types/error_codes.generated.ts），
// 顺序不稳会让每次重生成都产生一整片假 diff，把"真加了一个码"混在噪声里看不见。
func All() []string {
	codes := []string{
		ParamError, Unauthorized, Forbidden, NotFound, RateLimited, BizError, InternalError,
		TokenRevoked, MustChangePassword, InvalidAPIKey,
		DealRejected, OutreachRejected, AcquisitionRejected, PackTierDenied, ChannelConfigIncomplete,
	}
	for i := 1; i < len(codes); i++ { // 插入排序：清单是个位数到十位数规模，不必引 sort 包
		for j := i; j > 0 && codes[j-1] > codes[j]; j-- {
			codes[j-1], codes[j] = codes[j], codes[j-1]
		}
	}
	return codes
}

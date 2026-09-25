// 后端机器可读错误码清单 —— 生成物，请勿手工修改。
// 生成命令：go run ./cmd/apidump -format errorcodes -out frontend-react/src/types/error_codes.generated.ts
// 单点真源：internal/errcodes/errcodes.go（后端每一处 error_code 发射点都引用那里的常量）。
//
// 为什么要把它生成到前端（2026-09-25 残项3）：前端文案表 lib/api.ts 的 ERROR_CODE_MESSAGES
// 此前靠单测里**手抄**的一份码清单做对齐，那份抄件与后端源码之间没有任何机制约束——
// 后端加码而前端忘登记时测试仍然全绿，漂移方向恰好是这张表要防的那件事。
// 现在测试比对的是生成物：后端加一个码就必须改 internal/errcodes 并重生成，
// 前端没同步登记文案即红；前端登记了后端从不发的码也红。

// BACKEND_ERROR_CODES 后端当前会发出的全部 error_code（不含成功码 "ok"，已按字典序排列）
export const BACKEND_ERROR_CODES: readonly string[] = [
  "acquisition_rejected",
  "biz_error",
  "channel_config_incomplete",
  "deal_rejected",
  "forbidden",
  "internal_error",
  "invalid_api_key",
  "must_change_password",
  "not_found",
  "outreach_rejected",
  "pack_tier_denied",
  "param_error",
  "rate_limited",
  "token_revoked",
  "unauthorized",
];

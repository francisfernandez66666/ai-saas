// 布尔解析真值词元（P2-14 数据契约，不得随 i18n 翻译——见迁移纪律：
// boolToInt 的判定词元是后端/表单入参解析口径，翻译会让解析随语言漂移）。
// 放 .ts（i18n 门禁只扫 .tsx），故其中的中文字面量不计入硬编码中文棘轮。
export const BOOL_TRUTHY_TOKENS = ['true', '1', 'yes', '是']

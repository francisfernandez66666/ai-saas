// 访客占位名前缀（后端契约值，**不是界面文案，不得翻译**）。
// 后端对未留资的匿名客户统一命名 `访客_<id>`（见 internal/api 客户展示名清洗），
// 前端多处按此前缀判定"这是占位名、该显示成客户/首字头像"。
// 集中到 .ts 常量而非散落在 .tsx 字面量里：一是改契约只改一处，
// 二是界面层（.tsx）的硬编码中文门禁只该统计真文案，数据词元不该混进来。
export const VISITOR_NAME_PREFIX = '访客_'

/** 是否是访客占位名（空名按"是"处理：调用方多半要回落成通用称呼）。 */
export function isVisitorName(name?: string | null): boolean {
  return !name || name.startsWith(VISITOR_NAME_PREFIX)
}

// 平台出厂品牌名（P2-14 收口：原散点硬编码，哨兵比对一改即漂移）。
// 后端 seed 出厂 brand_name 与前端默认标题同源值；白标"未定制"判定依赖此常量。
// 刻意放在 .ts（而非 .tsx）模块：它是后端契约哨兵值，非界面文案，
// 不应被 README/i18n 硬编码中文门禁误计，也避免界面层重复硬编码。
export const PLATFORM_DEFAULT_BRAND = '跨山 LexCross'

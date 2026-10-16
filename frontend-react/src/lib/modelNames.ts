// 模型降级链兜底显示名（P2-14：从 admin/ConfigPanel.tsx 抽到数据层）。
// 这些是中英混排的模型注册名，属数据/展示标签，故放 .ts（界面层 .tsx 不计入硬编码中文门禁）。
export const MODEL_FALLBACK_NAMES: Record<string, string> = {
  siliconflow_deepseek_v4_flash: 'DeepSeek V4 Flash (硅基流动)',
  siliconflow_glm4_9b: 'GLM-4-9B (硅基流动免费)',
  zhipu_glm4_flash: 'GLM-4-Flash (智谱)',
  template_fallback: '模板兜底（不调用AI）',
}

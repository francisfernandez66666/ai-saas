// F3 租户资料解析层测试：JSON/MD/TXT 到 /admin/kb/upload payload 的转换。
import { describe, expect, it } from 'vitest'
import { parseKBFile } from '../tenantKb'

describe('parseKBFile', () => {
  it('解析 JSON 数组片段', () => {
    const out = parseKBFile('policy.json', JSON.stringify([
      { title: '退款政策', content: '7天无理由', category: '售后政策' },
      { name: '质保政策', text: '整车三年' },
    ]))
    expect(out).toHaveLength(2)
    expect(out[0]).toMatchObject({ title: '退款政策', category: '售后政策' })
    expect(out[1]).toMatchObject({ title: '质保政策', content: '整车三年', category: '企业知识' })
  })

  it('解析带 front matter 的 Markdown', () => {
    const out = parseKBFile('guide.md', '---\ntitle: 试驾指引\ncategory: 销售话术\n---\n客户到店先核对证件。')
    expect(out[0]).toMatchObject({ title: '试驾指引', category: '销售话术', content: '客户到店先核对证件。' })
  })

  it('TXT 超长内容自动按上限切块', () => {
    const out = parseKBFile('long.txt', 'a'.repeat(40001))
    expect(out).toHaveLength(3)
    expect(out[0].title).toBe('long (1/3)')
    expect(out[2].content).toHaveLength(1)
  })
})

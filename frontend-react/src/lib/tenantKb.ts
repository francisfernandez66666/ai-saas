// F3 租户资料上传解析层：把 JSON/Markdown/TXT 文件转换成 /admin/kb/upload 可消费的片段。
// 仅处理纯文本形态，不触发网络请求，便于组件测试与错误定位。

export type KBUpsertPayload = { title: string; content: string; category?: string }

const DEFAULT_CATEGORY = '企业知识'
const MAX_UPLOAD_RUNES = 20000

/** 把未知字段清洗成可展示的字符串。 */
function clean(v: unknown, fallback = ''): string {
  const s = String(v ?? '').trim()
  return s || fallback
}

/** 移除文件名扩展名，作为知识库标题兜底。 */
function stripExtension(name: string): string {
  return clean(name, '未命名资料').replace(/\.(json|md|markdown|txt)$/i, '')
}

/** 按 Unicode 字符数统计文本长度。 */
function textLength(s: string): number {
  return Array.from(s).length
}

/** 把超长知识正文按固定长度切片，保留可检索粒度。 */
function splitLongContent(content: string): string[] {
  if (textLength(content) <= MAX_UPLOAD_RUNES) return [content]
  const chars = Array.from(content)
  const chunks: string[] = []
  for (let i = 0; i < chars.length; i += MAX_UPLOAD_RUNES) {
    chunks.push(chars.slice(i, i + MAX_UPLOAD_RUNES).join(''))
  }
  return chunks
}

/** 规范化单条知识库片段，补齐标题/内容/标签等字段。 */
function normalize(item: any, fallbackTitle: string): KBUpsertPayload | null {
  if (!item || typeof item !== 'object') return null
  const content = item.content ?? item.text ?? item.body ?? item.value
  if (content === undefined || content === null || String(content).trim() === '') return null
  return {
    title: clean(item.title ?? item.name, fallbackTitle),
    content: String(content),
    category: clean(item.category ?? item.type ?? item.group, DEFAULT_CATEGORY),
  }
}

/** 解析 Markdown front matter，返回元数据和正文。 */
function parseFrontMatter(text: string, fallbackTitle: string): { meta: Record<string, string>; body: string } {
  const m = /^---\s*\n([\s\S]*?)\n---\s*\n?/.exec(text)
  if (!m) return { meta: {}, body: text }
  const meta: Record<string, string> = {}
  for (const line of m[1].split(/\r?\n/)) {
    const kv = /^([A-Za-z_\- ]+):\s*(.*)$/.exec(line.trim())
    if (kv) meta[kv[1].trim().toLowerCase()] = kv[2].trim().replace(/^["']|["']$/g, '')
  }
  return { meta, body: text.slice(m[0].length) }
}

/** 为长文切片生成带序号的稳定标题。 */
function chunkTitle(base: string, index: number, total: number): string {
  return total > 1 ? `${base} (${index + 1}/${total})` : base
}

/** 解析上传的 JSON/Markdown/TXT 文件并生成知识库片段。 */
export function parseKBFile(name: string, text: string): KBUpsertPayload[] {
  const lower = name.toLowerCase()
  const fallbackTitle = stripExtension(name)
  if (lower.endsWith('.json')) {
    try {
      const data = JSON.parse(text)
      if (Array.isArray(data)) {
        return data.map((x, i) => normalize(x, `${fallbackTitle} #${i + 1}`)).filter(Boolean) as KBUpsertPayload[]
      }
      if (data && typeof data === 'object') {
        const arr = data.list ?? data.data ?? (Array.isArray(data.items) ? data.items : null)
        if (Array.isArray(arr)) {
          return arr.map((x, i) => normalize(x, `${fallbackTitle} #${i + 1}`)).filter(Boolean) as KBUpsertPayload[]
        }
        const one = normalize(data, clean(data.title ?? data.name, fallbackTitle))
        if (one) return [one]
      }
    } catch {
      // JSON 解析失败时退回纯文本，避免上传按钮卡死。
    }
  }

  const { meta, body } = parseFrontMatter(text, fallbackTitle)
  const content = clean(body, clean(text, '空文件'))
  const title = clean(meta.title, fallbackTitle)
  const category = clean(meta.category, DEFAULT_CATEGORY)
  return splitLongContent(content).map((c, i, all) => ({
    title: chunkTitle(title, i, all.length),
    content: c,
    category,
  }))
}

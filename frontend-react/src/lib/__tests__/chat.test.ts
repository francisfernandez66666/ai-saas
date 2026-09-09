// 前端聊天纯逻辑单测（2026-09-08 本轮修复覆盖）
// 场景：聊天记录不固化(轮询替换全列表) / 回复不实时(轮询闭包) / 引导词循环(system 过滤) / 临时消息防重
import { describe, expect, it } from 'vitest'
import { collectFreshMessages, filterReplyMessages, promoteTempMessage, promoteTempAndRegister, dropSystemNotice, ChatMsg } from '../chat'
import { resolveJsonMode } from '../jsonMode'

describe('collectFreshMessages 轮询去重追加', () => {
  it('首轮拉取：全量历史都算新增（knownIds 为空）', () => {
    const known = new Set<string>()
    const items: ChatMsg[] = [
      { id: 1, sender_type: 'system', content: '欢迎' },
      { id: 2, sender_type: 'ai', content: '你好' },
    ]
    expect(collectFreshMessages(items, known)).toHaveLength(2)
    expect(known.has('1')).toBe(true)
    expect(known.has('2')).toBe(true)
  })

  it('后续轮询：只追加未见过的新消息，绝不替换或重复', () => {
    const known = new Set(['1', '2'])
    const items: ChatMsg[] = [
      { id: 1, sender_type: 'ai', content: '旧消息（已在列表）' },
      { id: 2, sender_type: 'customer', content: '旧消息（已在列表）' },
      { id: 3, sender_type: 'ai', content: 'AI 新回复' },
    ]
    const fresh = collectFreshMessages(items, known)
    expect(fresh).toHaveLength(1)
    expect(fresh[0].id).toBe(3)
    expect(known.size).toBe(3)
  })

  it('id 为空的记录跳过（不展示无 ID 的脏数据）', () => {
    const known = new Set<string>()
    const fresh = collectFreshMessages([{ id: null, sender_type: 'ai', content: 'x' }], known)
    expect(fresh).toHaveLength(0)
  })
})

describe('filterReplyMessages 发送响应防重 + 引导词过滤', () => {
  it('过滤 system 型瞬时确认语（人工模式"请稍候"不重复渲染）', () => {
    const known = new Set<string>()
    const replies: ChatMsg[] = [
      { id: 10, sender_type: 'ai', content: '真实回复' },
      { id: 11, sender_type: 'system', content: '销售顾问正在赶来的路上，请稍候~' },
    ]
    expect(filterReplyMessages(replies, known)).toHaveLength(1)
    expect(filterReplyMessages(replies, known)[0].id).toBe(10)
  })

  it('丢弃已由轮询先展示的同 ID 回复（DB 写入先于 HTTP 返回的时序）', () => {
    const known = new Set(['99'])
    const replies: ChatMsg[] = [{ id: 99, sender_type: 'ai', content: '重复回复' }]
    expect(filterReplyMessages(replies, known)).toHaveLength(0)
  })

  it('无 ID 的兜底回复（ai_reply 分支构造）放行', () => {
    const known = new Set<string>()
    const replies: ChatMsg[] = [{ sender_type: 'ai', content: '兜底回复' }]
    expect(filterReplyMessages(replies, known)).toHaveLength(1)
  })
})

describe('promoteTempMessage 临时消息替换 DB ID', () => {
  const tempId = 'temp_123'
  it('轮询未抢跑：临时气泡替换为真实 DB ID', () => {
    const msgs: ChatMsg[] = [{ id: tempId, sender_type: 'customer', content: '在吗' }]
    const next = promoteTempMessage(msgs, tempId, 42)
    expect(next).toHaveLength(1)
    expect(next[0].id).toBe(42)
  })

  it('轮询已先加入同 ID（DB 写入先于 HTTP 返回）：移除临时气泡防重复', () => {
    const msgs: ChatMsg[] = [
      { id: tempId, sender_type: 'customer', content: '在吗' },
      { id: 42, sender_type: 'customer', content: '在吗' },
    ]
    const next = promoteTempMessage(msgs, tempId, 42)
    expect(next).toHaveLength(1)
    expect(next[0].id).toBe(42)
  })

  it('未匹配到临时消息时原样返回', () => {
    const msgs: ChatMsg[] = [{ id: 7, sender_type: 'customer', content: 'x' }]
    expect(promoteTempMessage(msgs, 'temp_none', 8)).toHaveLength(1)
  })
})

describe('promoteTempAndRegister 替换临时消息并登记 dbId（2026-09-09 双气泡修复）', () => {
  it('HTTP 响应先到：临时气泡换成 dbId，且 dbId 进 knownIds，poll 后续拉到历史不再二次追加', () => {
    const known = new Set(['temp_900'])
    const msgs: ChatMsg[] = [{ id: 'temp_900', sender_type: 'customer', content: '在吗' }]
    // 1) HTTP 响应处理：替换 + 登记 dbId
    const afterResp = promoteTempAndRegister(msgs, 'temp_900', 42, known)
    expect(afterResp).toHaveLength(1)
    expect(afterResp[0].id).toBe(42)
    expect(known.has('42')).toBe(true)
    // 2) 随后 poll/WS 拉历史返回同一条消息（dbId=42）
    const fresh = collectFreshMessages([{ id: 42, sender_type: 'customer', content: '在吗' }], known)
    expect(fresh).toHaveLength(0)
  })

  it('轮询先到达：同 dbId 已在列表时移除临时气泡，且 knownIds 登记不重复', () => {
    const known = new Set(['42'])
    const msgs: ChatMsg[] = [
      { id: 'temp_901', sender_type: 'customer', content: '在吗' },
      { id: 42, sender_type: 'customer', content: '在吗' },
    ]
    const after = promoteTempAndRegister(msgs, 'temp_901', 42, known)
    expect(after).toHaveLength(1)
    expect(after[0].id).toBe(42)
    expect(known.has('42')).toBe(true)
  })
})

describe('dropSystemNotice 清理瞬时系统占位（2026-09-09）', () => {
  it('移除指定文案的 system 占位，保留真实回复', () => {
    const msgs: ChatMsg[] = [
      { sender_type: 'system', content: '顾问可能正在忙碌中，请稍候' },
      { id: 51, sender_type: 'ai', content: '真实回复' },
      { sender_type: 'system', content: '消息发送失败，请重试~' },
    ]
    const next = dropSystemNotice(msgs, '顾问可能正在忙碌中，请稍候')
    expect(next).toHaveLength(2)
    expect(next.find((m) => m.content === '顾问可能正在忙碌中，请稍候')).toBeUndefined()
    expect(next.find((m) => m.id === 51)).toBeTruthy()
  })

  it('列表无该占位时原样返回', () => {
    const msgs: ChatMsg[] = [{ id: 1, sender_type: 'ai', content: 'x' }]
    expect(dropSystemNotice(msgs, '顾问可能正在忙碌中，请稍候')).toHaveLength(1)
  })
})

describe('resolveJsonMode 策略引擎 JSON 形态判定（修复 React error #31）', () => {
  it('anchor_weights 对象数组 → objectArray（原崩溃主因）', () => {
    const weight = `[{"intent_score_weight":-2.0,"trust_weight":-1.5,"base_bias":0.5},{"intent_score_weight":0.5,"base_bias":1.0}]`
    expect(resolveJsonMode(weight)).toBe('objectArray')
  })

  it('数字数组 → numberArray，字符串数组 → stringArray', () => {
    expect(resolveJsonMode('[1,2,3]')).toBe('numberArray')
    expect(resolveJsonMode('["a","b"]')).toBe('stringArray')
  })

  it('非法 JSON / 非数组 → plain 文本兜底', () => {
    expect(resolveJsonMode('not-json')).toBe('plain')
    expect(resolveJsonMode('{"k":1}')).toBe('plain')
    expect(resolveJsonMode('[]')).toBe('stringArray')
  })
})
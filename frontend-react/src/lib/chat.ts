/**
 * chat.ts：聊天消息合并/去重纯函数（供 Client.tsx 轮询与发送响应复用）
 *
 * 本轮修复背景（2026-09-08）：
 * 1. 原 poll() 直接 setMsgs(next) 把整个列表替换成"仅新增" → 刷新/轮询后历史崩坏；
 *    抽出 collectFreshMessages 按 ID 去重追加，杜绝闭包旧值替换全列表。
 * 2. 原 send() 把"请稍候"等 system 型瞬时确认语当气泡渲染 → 引导词循环出现；
 *    抽出 filterReplyMessages 过滤 system 消息 + 已由轮询先到的重复 ID。
 * 3. 临时消息替换 DB ID 时若轮询已先加入同 ID 消息会留下重复，promoteTempMessage 处理。
 * 纯函数便于 vitest 直接覆盖，不依赖组件渲染。
 */

/** 消息最小结构（与后端 Message 对齐，仅取单测/合并所需字段） */
export type ChatMsg = {
  id?: number | string | null
  sender_type?: string
  content?: string
  conversation_id?: number
  created_at?: string
}

/**
 * collectFreshMessages 按 ID 去重，返回"没见过"的新消息，并就地写入 knownIds。
 * 核心语义：轮询/WS 兜底拉取全量历史时，只追加新消息、绝不替换已有列表。
 * @param items   本次拉取到的消息数组（可能含历史全量）
 * @param knownIds 已展示消息 ID 集合（Set<string>，按 String(id) 比较）
 * @returns 仅新增的消息数组
 */
export function collectFreshMessages<T extends ChatMsg>(items: T[], knownIds: Set<string>): T[] {
  const fresh: T[] = []
  for (const m of items) {
    if (m.id == null) continue
    const key = String(m.id)
    if (knownIds.has(key)) continue
    fresh.push(m)
    knownIds.add(key)
  }
  return fresh
}

/**
 * filterReplyMessages 过滤发送响应中的回复消息：
 * 1. 丢弃 system 型瞬时确认语（如人工模式下"销售顾问正在赶来的路上"），避免引导词重复；
 * 2. 丢弃已由轮询/WS 先展示的同 ID 消息（DB 写入先于 HTTP 返回的时序窗口）。
 * @param replies  响应里的回复消息数组
 * @param knownIds 已展示消息 ID 集合
 * @returns 真正需要追加渲染的消息数组
 */
export function filterReplyMessages<T extends ChatMsg>(replies: T[], knownIds: Set<string>): T[] {
  return replies.filter(
    (m) => m.sender_type !== 'system' && (m.id == null || !knownIds.has(String(m.id)))
  )
}

/**
 * promoteTempMessage 把乐观更新的临时消息替换为真实数据库 ID：
 * 若该 DB ID 已被轮询先展示，则直接移除临时气泡（防同一条消息渲染两遍）；
 * 否则把临时气泡的 id 替换为 dbId。
 * @param msgs   当前已展示消息列表
 * @param tempId 临时消息 ID（如 temp_<时间戳>）
 * @param dbId   后端返回的真实消息主键
 * @returns 替换后的新列表（不可变更新）
 */
export function promoteTempMessage<T extends ChatMsg>(
  msgs: T[],
  tempId: string,
  dbId: number | string
): T[] {
  if (msgs.some((x) => x.id === dbId)) {
    return msgs.filter((x) => x.id !== tempId)
  }
  return msgs.map((x) => (x.id === tempId ? { ...x, id: dbId } : x))
}

/**
 * promoteTempAndRegister 替换临时消息为真实 DB ID，并把 dbId 一并登记进 knownIds。
 * 修复背景（2026-09-09）：Client.tsx 里 promoteTempMessage 后若不同步登记 dbId，
 * HTTP 响应先到时临时气泡换成真实ID，随后 poll/WS 拉历史看到 dbId 不在 knownIds
 * → 同一条客户消息被二次追加渲染（双气泡）。把"替换+登记"固化成纯函数便于回归。
 * @param msgs    当前已展示消息列表
 * @param tempId  临时消息 ID（如 temp_<时间戳>）
 * @param dbId    后端返回的真实消息主键
 * @param knownIds 已展示消息 ID 集合（就地新增 dbId）
 * @returns 替换后的新列表（不可变更新）
 */
export function promoteTempAndRegister<T extends ChatMsg>(
  msgs: T[],
  tempId: string,
  dbId: number | string,
  knownIds: Set<string>
): T[] {
  knownIds.add(String(dbId))
  return promoteTempMessage(msgs, tempId, dbId)
}

/**
 * dropSystemNotice 移除指定文案的瞬时系统占位（如"顾问可能正在忙碌中，请稍候"）。
 * 修复背景（2026-09-09）：60s 计时器在长延迟时插入忙碌占位，回复到达后占位不清理，
 * 视觉上与真实回复并存像是"两条消息"。有真实回复/有效响应时调用，保持消息流干净。
 * @param msgs   当前消息列表
 * @param notice 要移除的占位文案（精确匹配）
 * @returns 过滤后的新列表
 */
export function dropSystemNotice<T extends ChatMsg>(msgs: T[], notice: string): T[] {
  return msgs.filter((x) => x.sender_type !== 'system' || x.content !== notice)
}
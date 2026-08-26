<!--
  C端访客对话页（免登录）
  接口：POST /chat/test    {content, conversation_id?} —— 首访服务端自动创建访客
        GET  /chat/history?conversation_id=&limit=50 —— 会话历史
  合并窗口契约：
    25s 窗口内的连发消息会被合并处理；响应可能不含 ai_reply（merged 标记），
    此时前端延时拉取 history 获取真实回复，不本地伪造气泡。
-->
<template>
  <div style="display:flex;flex-direction:column;height:calc(100vh - 52px)">
    <!-- 消息流区域 -->
    <div ref="box" style="flex:1;overflow-y:auto;padding:10px">
      <div v-for="m in msgs" :key="m.id || m.ts" class="bubble-wrap" :class="{me:m.sender_type==='customer'}">
        <div class="bubble" :class="m.sender_type==='customer'?'user':(m.sender_type==='human'?'human':'ai')">{{ m.content }}</div>
      </div>
      <div v-if="!msgs.length" class="muted" style="text-align:center;margin-top:30px">您好，我是极石顾问，想了解点什么？</div>
    </div>
    <!-- 输入区 -->
    <div class="row" style="padding:10px;border-top:1px solid var(--line);background:#fff">
      <input v-model="draft" placeholder="输入消息..." @keyup.enter="send" style="margin:0" />
      <button class="btn" @click="send" :disabled="busy||!draft">发送</button>
    </div>
  </div>
</template>
<script setup>
import { ref, onMounted, nextTick } from 'vue'
import { api } from '../lib/api.js'

const msgs = ref([])          // 渲染消息数组（历史 + 本地乐观插入）
const draft = ref('')         // 输入框草稿
const busy = ref(false)       // 发送中防抖
const box = ref(null)         // 消息流容器（用于滚动到底）

// 会话ID持久化：刷新页面后继续同一会话
let convId = Number(localStorage.getItem('scrm_conv') || 0)
let seq = Date.now()          // 本地消息唯一键（无后端id的乐观消息用）

/** 追加本地消息并滚动到底 */
const push = (sender_type, content) => { msgs.value.push({ id: ++seq, sender_type, content }); scroll() }
const scroll = () => nextTick(() => box.value && (box.value.scrollTop = box.value.scrollHeight))

onMounted(async () => {
  if (convId) await loadHistory()
})

/** 拉取会话历史并整体替换渲染列表 */
async function loadHistory(){
  const j = await api('/chat/history?conversation_id=' + convId + '&limit=50')
  if (j.code === 0) {
    const list = j.data.list || j.data || []
    msgs.value = Array.isArray(list) ? list : []
    scroll()
  }
}

/** 发送消息：乐观上屏 → 服务端处理 → 展示回复或延后拉历史 */
async function send(){
  const content = draft.value.trim(); if (!content) return
  draft.value = ''; busy.value = true
  push('customer', content)
  const body = { content }
  if (convId) body.conversation_id = convId
  const j = await api('/chat/test', { method:'POST', body })
  busy.value = false
  if (j.code === 0) {
    const d = j.data || {}
    // 首轮响应会带回服务端分配的会话ID，持久化供后续轮询
    if (d.conversation_id) { convId = d.conversation_id; localStorage.setItem('scrm_conv', convId) }
    if (d.ai_reply) push('ai', d.ai_reply)
    else setTimeout(loadHistory, 3000)   // 合并批次场景：稍后从历史取真实回复
  }
  scroll()
}
</script>

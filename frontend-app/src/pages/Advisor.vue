<!--
  顾问工作台（登录态）
  接口：GET  /advisor/customers?page=&journey_stage=   客户列表（DataScope按身份过滤）
        GET  /advisor/customer/:id                     客户详情（含会话列表）
        GET  /chat/history?conversation_id=            会话消息
        POST /advisor/chat/takeover                    一键人工接管
        POST /advisor/chat/ai-reply                    触发AI辅助回复
        POST /advisor/chat/send                        人工发送消息（自动入素材池）
  轮询：选中客户后每8秒刷新会话消息（新AI/客户消息自动出现）
-->
<template>
  <div>
    <!-- 阶段筛选 -->
    <div class="row" style="padding:0 12px">
      <select v-model="stageFilter" style="max-width:180px" @change="load">
        <option value="">全部阶段</option>
        <option value="ai_connected">AI建联</option><option value="lead_captured">已留资</option>
        <option value="arrived">已到店</option><option value="quoted">已报价</option>
      </select>
      <span class="muted">共 {{ total }} 人</span>
    </div>

    <!-- 客户卡片列表 -->
    <div class="card" v-for="c in list" :key="c.id" @click="pick(c)"
         :style="{cursor:'pointer', outline: cur && cur.id===c.id ? '2px solid var(--pri)' : ''}">
      <div class="row" style="justify-content:space-between">
        <b>{{ c.name }}</b>
        <span class="tag">{{ c.journey_stage }}</span>
      </div>
      <p class="muted">
        意向 {{ pct(c.intent_score) }}%
        · {{ c.assigned_user_name ? '顾问 '+c.assigned_user_name : '未分配' }}
        · {{ c.conv_mode==='human' ? '人工中' : 'AI接待' }}
      </p>
    </div>
    <p v-if="!list.length" class="muted" style="text-align:center;padding:30px">暂无客户</p>

    <!-- 选中的客户详情 + 会话操作区 -->
    <template v-if="cur">
      <div class="card">
        <b>{{ cur.name }}</b> <span class="tag">{{ cur.journey_stage }}</span>
        <p class="muted">意向 {{ pct(cur.intent_score) }} · 预算 {{ cur.budget || '-' }}万 · {{ cur.career || '职业未知' }}</p>
        <div class="row" style="margin-top:8px">
          <button class="btn gray" @click="takeover">一键接管</button>
          <button class="btn gray" @click="aiReply">触发AI回复</button>
        </div>
      </div>

      <!-- 会话消息流（含[人工]标识区分顾问消息） -->
      <div class="card" style="max-height:40vh;overflow-y:auto">
        <div v-for="m in convMsgs" :key="m.id" class="bubble-wrap" :class="{me:m.sender_type==='customer'}">
          <div class="bubble" :class="m.sender_type==='customer'?'user':(m.sender_type==='human'?'human':'ai')">
            <span v-if="m.sender_type==='human'" class="muted" style="font-size:11px">[人工] </span>{{ m.content }}
          </div>
        </div>
        <p v-if="!convMsgs.length" class="muted">暂无会话记录</p>
      </div>

      <!-- 人工回复输入 -->
      <div class="row" style="padding:0 12px 20px">
        <input v-model="reply" placeholder="人工回复内容..." style="margin:0" @keyup.enter="send" />
        <button class="btn ok" @click="send" :disabled="!convId">发送</button>
      </div>
    </template>
  </div>
</template>
<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { api } from '../lib/api.js'

const list = ref([]), total = ref(0), stageFilter = ref('')
const cur = ref(null), convId = ref(0), convMsgs = ref([]), reply = ref('')
let timer = null

const pct = v => Math.round((v||0)*100) + '%'

/** 加载客户列表（可按线索阶段过滤） */
async function load(){
  const q = '/advisor/customers?page=1&page_size=50' + (stageFilter.value ? '&journey_stage='+stageFilter.value : '')
  const j = await api(q)
  if (j.code === 0){ list.value = j.data.list || []; total.value = j.data.total }
}

/** 选中客户：拉详情定位最新会话，再加载消息 */
async function pick(c){
  cur.value = c
  const j = await api('/advisor/customer/' + c.id)
  if (j.code === 0){
    const d = j.data || {}
    const convs = d.conversations || []
    convId.value = convs.length ? convs[0].id : 0
    await loadMsgs()
  }
}

/** 加载当前会话消息 */
async function loadMsgs(){
  if (!convId.value){ convMsgs.value = []; return }
  const j = await api('/chat/history?conversation_id=' + convId.value + '&limit=50')
  if (j.code === 0){ const l = j.data.list || j.data || []; convMsgs.value = Array.isArray(l) ? l : [] }
}

/** 一键接管：暂停AI、标记人工接待 */
async function takeover(){
  if(!cur.value) return
  const j = await api('/advisor/chat/takeover', { method:'POST', body:{ customer_id: cur.value.id } })
  alert(j.message || j.code)
}

/** 手动触发一次AI辅助回复 */
async function aiReply(){
  if(!convId.value) return alert('该客户暂无会话')
  const j = await api('/advisor/chat/ai-reply', { method:'POST', body:{ conversation_id: convId.value } })
  alert(j.message || j.code); load(); pick(cur.value)
}

/** 发送人工消息（后端同步入素材池） */
async function send(){
  if(!convId.value || !reply.value) return
  await api('/advisor/chat/send', { method:'POST', body:{ conversation_id: convId.value, content: reply.value } })
  reply.value = ''; loadMsgs(); load()   // 刷新消息与列表统计
}

onMounted(load)
// 会话消息轮询：8秒一拍，组件卸载时清理
const t = setInterval(()=>{ if(cur.value) loadMsgs() }, 8000)
onUnmounted(()=>clearInterval(t))
</script>

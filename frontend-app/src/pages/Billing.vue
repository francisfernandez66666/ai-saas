<!--
  收银台（登录态）
  接口：GET  /billing/my-package        三桶余额 + 计费引擎状态
        GET  /packages                  在售套餐列表
        POST /billing/subscribe             下单 {package_id}
        POST /billing/orders/mock-pay       模拟到账 {order_id}（仅mock渠道）
        POST /billing/manual-confirm        「我已付费」{order_id}（manual/static_qr渠道）
  三桶语义：③免费体验桶(注册/邀请,14天) → ①月度订阅额度(paid) → ②永久余额(充值包)
-->
<template>
  <!-- 三桶余额概览 -->
  <div class="grid" style="grid-template-columns:repeat(3,1fr);padding:0 12px">
    <div class="stat"><b>{{ fmt(free) }}</b><span class="muted">免费体验桶{{ freeExp }}</span></div>
    <div class="stat"><b>{{ fmt(monthlyUsed) }}/{{ fmt(monthlyQuota) }}</b><span class="muted">月度订阅额度</span></div>
    <div class="stat"><b>{{ fmt(balance) }}</b><span class="muted">永久余额</span></div>
  </div>
  <p class="muted" style="padding:0 16px">扣减顺序：免费体验桶 → 月度额度 → 永久余额 · 引擎{{ engineOn?'已开启':'灰度未开（只记不扣）' }}</p>

  <!-- 在售套餐 -->
  <div class="card">
    <h3>充值套餐</h3>
    <div v-for="p in pkgs" :key="p.id" class="row" style="justify-content:space-between;border-bottom:1px solid var(--line);padding:10px 0">
      <div>
        <b>{{ p.name }}</b> <span class="tag">{{ typeName(p.p_type) }}</span>
        <p class="muted">{{ p.description }}</p>
      </div>
      <div style="text-align:right">
        <b>¥{{ (p.price_cents/100).toFixed(0) }}</b><br/>
        <button class="btn" @click="buy(p)">订阅/充值</button>
      </div>
    </div>
    <p v-if="!pkgs.length" class="muted">加载中...</p>
  </div>

  <!-- 当前待支付订单操作卡 -->
  <div class="card" v-if="order">
    <h3>待支付订单 #{{ order.id }}</h3>
    <p>渠道：{{ order.channel }} · 金额 ¥{{ (order.amount_cents/100).toFixed(2) }}</p>
    <p class="muted" style="word-break:break-all">{{ order.qr_content }}</p>
    <div class="row" style="margin-top:8px">
      <!-- mock 渠道：测试环境一键模拟到账 -->
      <template v-if="order.channel==='mock'">
        <button class="btn warn" @click="pay(order.id)">模拟到账</button>
      </template>
      <!-- manual 渠道：转账后人工确认，超管核实后自动发放权益 -->
      <template v-if="order.channel==='manual'">
        <span class="tag">转账后点下方按钮，超管核实后自动到账</span>
        <button class="btn ok" @click="confirmPaid(order.id)">我已付费</button>
      </template>
    </div>
  </div>
</template>
<script setup>
import { ref, onMounted } from 'vue'
import { api } from '../lib/api.js'

// ---- 三桶余额与引擎状态 ----
const free=ref(0), freeExp=ref(''), monthlyQuota=ref(0), monthlyUsed=ref(0),
      balance=ref(0), engineOn=ref(false), pkgs=ref([]), order=ref(null)

const fmt = n => (n||0).toLocaleString('zh-CN')
const typeName = t => ({free:'试用',paid:'包月',increment:'充值包'}[t] || t)

/** 拉取我的三桶余额与计费引擎状态 */
const loadBuckets = async () => {
  const j = await api('/billing/my-package')
  if (j.code === 0){
    const d = j.data
    free.value = d.free_token_balance
    freeExp.value = d.free_token_expires_at ? '·至'+String(d.free_token_expires_at).slice(5,10) : ''
    monthlyQuota.value = d.monthly_token_quota
    monthlyUsed.value = d.monthly_token_used
    balance.value = d.token_balance
    engineOn.value = !!d.token_billing_enabled
  }
}

/** 拉取在售套餐列表（过滤下架项） */
const loadPkgs = async () => {
  const j = await api('/packages')
  if (j.code === 0) pkgs.value = (j.data || []).filter(p=>p.enabled!==false)
}

/**
 * 下单：创建待支付订单
 * mock 模式返回可模拟到账的订单；static_qr 模式附带收款码内容
 */
async function buy(p){
  const j = await api('/billing/subscribe', { method:'POST', body:{ package_id: p.id } })
  if (j.code === 0){ order.value = j.data.order || j.data; alert('订单已创建') }
  else alert(j.message)
}

/** mock 渠道模拟到账（生产环境该接口被服务端403封死） */
async function pay(id){
  const j = await api('/billing/orders/mock-pay', { method:'POST', body:{ order_id: id } })
  if (j.code === 0){ order.value=null; await loadBuckets(); alert('到账成功，权益已发放') }
  else alert(j.message)
}

/** static_qr「我已付费」：写 critical 审计并催告超管核实 */
async function confirmPaid(id){
  const j = await api('/billing/manual-confirm', { method:'POST', body:{ order_id: id } })
  if (j.code === 0) alert('已提交「我已付费」，平台核实后自动到账')
  else alert(j.message)
}

onMounted(()=>{ loadBuckets(); loadPkgs() })
</script>

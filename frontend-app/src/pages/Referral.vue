<!--
  邀请推广页（登录态·租户管理员）
  接口：GET /admin/referral/info           邀请码/链接/统计/奖励参数快照
        GET /admin/referral/qrcode?size=   邀请二维码PNG（需鉴权 → fetch blob 转 objectURL）
  规则：好友注册→邀请人+30万token&+14天；好友购paid首笔→邀请人+50万永久。多邀多得，单邀单个。
-->
<template>
  <div class="card">
    <h3>🎁 我的邀请码</h3>
    <div class="row" style="margin-top:10px">
      <b style="font-size:26px;letter-spacing:4px;color:var(--pri)">{{ code || '...' }}</b>
      <button class="btn gray" @click="copy(code)">复制码</button>
    </div>
    <label style="display:block;margin-top:12px">邀请链接</label>
    <div class="row"><input readonly :value="url" /><button class="btn gray" @click="copy(url)">复制</button></div>
    <!-- 二维码经鉴权接口取回的 blob 渲染 -->
    <img v-if="qr" :src="qr" style="width:200px;margin-top:10px;border-radius:10px" alt="二维码" />
  </div>

  <!-- 统计三联 -->
  <div class="grid" style="grid-template-columns:repeat(3,1fr);padding:0 12px">
    <div class="stat"><b>{{ info.invited_count || 0 }}</b><span class="muted">已邀请</span></div>
    <div class="stat"><b>{{ info.paid_count || 0 }}</b><span class="muted">已付费好友</span></div>
    <div class="stat"><b>{{ fmt(info.bonus_per_signup) }}</b><span class="muted">每位注册奖token</span></div>
  </div>

  <div class="card muted">
    规则：好友经你的链接注册 → 你得 {{ fmt(info.bonus_per_signup) }} 免费token且有效期+{{ info.ext_days_per_signup }}天；
    好友购买包月套餐首笔到账 → 你再得 {{ fmt(info.bonus_on_paid) }} 永久token。多邀多得，单邀单个。
  </div>

  <!-- 邀请记录列表：受邀人ID/邮箱/邀请成功/支付成功/奖励发放 -->
  <div class="card">
    <h3>📋 邀请记录</h3>
    <table style="width:100%;font-size:13px;border-collapse:collapse" v-if="records.length">
      <thead>
        <tr class="muted" style="text-align:left">
          <th style="padding:6px">账户ID</th><th>企业</th><th>邮箱</th><th>邀请成功</th>
          <th>已支付</th><th>注册奖</th><th>付费奖</th><th>注册时间</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in records" :key="r.tenant_id" style="border-top:1px solid var(--line)">
          <td style="padding:6px">{{ r.tenant_id }}</td>
          <td>{{ r.company_name }}</td>
          <td>{{ r.email || '-' }}</td>
          <td>{{ r.invited_ok ? '✅' : '❌' }}</td>
          <td>{{ r.paid_ok ? '✅' : '—' }}</td>
          <td>{{ r.signup_reward ? '✅ 已发' : '—' }}</td>
          <td>{{ r.paid_reward ? '✅ 已发' : (r.paid_ok ? '待发放' : '—') }}</td>
          <td class="muted">{{ r.registered_at }}</td>
        </tr>
      </tbody>
    </table>
    <p v-else class="muted">暂无邀请记录——复制上方链接开始邀请吧</p>
  </div>
</template>
<script setup>
import { ref, onMounted } from 'vue'
import { api, getToken } from '../lib/api.js'

const code = ref(''), url = ref(''), qr = ref(''), info = ref({}), records = ref([])
const fmt = n => (n||0).toLocaleString('zh-CN')

/** 剪贴板复制（非安全上下文降级 execCommand） */
const copy = t => navigator.clipboard ? navigator.clipboard.writeText(t) : fallbackCopy(t)
function fallbackCopy(t){
  const i = document.createElement('input'); i.value = t
  document.body.appendChild(i); i.select(); document.execCommand('copy'); i.remove()
}

onMounted(async () => {
  // ① 邀请信息聚合
  const j = await api('/admin/referral/info')
  if (j.code === 0){
    info.value = j.data.referral
    code.value = j.data.referral.invite_code
    url.value = j.data.invite_url
    // ② 二维码 PNG 接口带 Bearer 鉴权：fetch 取 blob 转 objectURL 给 img
    const r = await fetch('/api/v1/admin/referral/qrcode?size=240', { headers:{ Authorization:'Bearer '+getToken() } })
    if (r.ok) qr.value = URL.createObjectURL(await r.blob())
  }
  // ③ 邀请记录（受邀人id/邮箱/邀请成功/支付成功/奖励发放）
  const rec = await api('/admin/referral/records')
  if (rec.code === 0) records.value = rec.data.list || []
})
</script>

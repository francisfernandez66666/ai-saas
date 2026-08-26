<!--
  租户入驻注册页
  接口：GET  /auth/register-config  下发邮箱验证开关（显隐验证码输入）
        POST /auth/email/code       发送邮箱验证码（60s冷却由后端控制）
        POST /tenant/signup         提交入驻（自动发30万token体验+邀请奖励）
  邀请支持：URL 携带 ?ref=邀请码 时存入 localStorage 并随注册提交（首绑唯一）
-->
<template>
  <div class="card" style="max-width:460px;margin:30px auto">
    <h2>免费开通</h2>
    <p class="muted">注册即送 {{ trial }} token 体验（14天有效）· 邀请好友双方得奖</p><br/>
    <template v-if="needEmail">
      <label>管理员邮箱 *</label>
      <div class="row"><input v-model="email" /><button class="btn gray" @click="sendCode" :disabled="cd>0">{{ cd>0? cd+'s':'发验证码' }}</button></div>
      <label>邮箱验证码 *</label><input v-model="emailCode" />
    </template>
    <label>企业名称 *</label><input v-model="company" />
    <label>访问标识 * （子域名，3-20位小写字母开头）</label><input v-model="code" />
    <label>管理员账号 *</label><input v-model="username" />
    <label>密码 *（≥6位）</label><input v-model="password" type="password" />
    <button class="btn" style="width:100%" @click="submit" :disabled="busy">{{ busy?'提交中...':'免费开通' }}</button>
    <p v-if="msg" :style="{color: ok?'var(--ok)':'var(--warn)'}">{{ msg }}</p>
    <p class="muted" style="margin-top:10px">已有账号？<a href="#/login">去登录</a></p>
  </div>
</template>
<script setup>
import { ref, onMounted } from 'vue'
import { api } from '../lib/api.js'

const needEmail = ref(false)   // 平台下发的邮箱验证开关（register-config）
const email = ref(''), emailCode = ref(''), company = ref(''), code = ref('')
const username = ref(''), password = ref(''), busy = ref(false), msg = ref(''), ok = ref(false), cd = ref(0)

onMounted(async () => {
  // ① 读取注册配置决定是否需要邮箱验证码
  const j = await api('/auth/register-config').catch(()=>null)
  if (j && j.data) needEmail.value = !!j.data.email_verify_enabled
})

/** 发送邮箱验证码（冷却倒计时纯前端展示；硬限制由后端控制）*/
async function sendCode(){
  if (!email.value) return
  await api('/auth/email-code', { method:'POST', body:{ email: email.value } })
  cd.value = 60; const t = setInterval(()=>{ cd.value--; if(cd.value<=0) clearInterval(t) },1000)
}

/** 提交入驻：组装字段 + 附带邀请码（localStorage 存的 ?ref= 值）*/
async function submit(){
  busy.value = true
  const body = {
    company_name: company.value,
    code: code.value,
    username: username.value,
    password: password.value,
    admin_email: email.value || undefined,
    email_code: emailCode.value || undefined,
  }
  const rf = localStorage.getItem('scrm_ref')
  if (rf) body.ref = rf
  const j = await api('/tenant/signup', { method:'POST', body })
  busy.value = false
  if (j.code === 0) {
    ok.value = true
    msg.value = j.message + '！即将跳转登录...'
    setTimeout(()=>{ location.hash = '#/login' }, 1500)
  } else { msg.value = j.message }
}
</script>

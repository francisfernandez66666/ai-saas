<!--
  登录页
  接口：POST /api/v1/auth/login {tenant_code?, username, password}
       → data.token + data.must_change_password
  流程：
    1. 登录成功写 token；must_change_password=true（首登强改密）跳设置页强制改密
    2. 支持 ?redirect= 回跳登录前目标页
-->
<template>
  <div class="card" style="max-width:420px;margin:40px auto">
    <h2>登录</h2><br/>
    <label>企业标识（选填，防串站）</label>
    <input v-model="tenantCode" placeholder="留空=默认租户" />
    <label>用户名</label><input v-model="username" />
    <label>密码</label><input v-model="password" type="password" @keyup.enter="submit" />
    <button class="btn" style="width:100%" :disabled="busy" @click="submit">{{ busy?'登录中...':'登录' }}</button>
    <p class="muted" style="margin-top:12px">
      还没有账号？<a href="#" @click.prevent="$router.push('/register')">免费开通</a>
      · <a href="#" @click.prevent="$router.push('/settings')">忘记密码请重置</a>
    </p>
    <p v-if="msg" :style="{color: ok?'var(--ok)':'var(--warn)'}">{{ msg }}</p>
  </div>
</template>
<script setup>
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { api, setToken } from '../lib/api.js'

const r = useRouter(), route = useRoute()
const tenantCode = ref(route.query.tenant_code || '')   // 支持外部带 ?tenant_code= 直达
const username = ref(''), password = ref(''), busy = ref(false), msg = ref(''), ok = ref(false)

/** 提交登录：成功写 token 并按强改密标记分流 */
async function submit(){
  busy.value = true
  const body = { username: username.value, password: password.value }
  if (tenantCode.value) body.tenant_code = tenantCode.value
  const j = await api('/auth/login', { method:'POST', body })
  busy.value = false
  if (j.code === 0) {
    setToken(j.data.token)
    ok.value = true; msg.value = '登录成功'
    // 强改密用户只放行到设置页；其余回跳目标页并刷新全局状态
    location.hash = j.data.must_change_password ? '#/settings?must=1' : (route.query.redirect || '#/')
    if (!j.data.must_change_password) location.reload()
  } else { msg.value = j.message || '登录失败' }
}
</script>

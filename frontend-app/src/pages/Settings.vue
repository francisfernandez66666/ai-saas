<!--
  账号设置页（登录态）
  四大块：
    1. 修改密码      POST /auth/change-password {old_password,new_password}
    2. 换绑邮箱      POST /auth/email/code  发码到新邮箱
                     POST /auth/email/change {new_email,code}
                     ⚠ 新邮箱曾参与奖励领取会被撞库拦截(409)
    3. 企业知识库    POST /admin/kb/upload 切片入库 / GET my / DELETE my/:id
                     上传内容经分段切片后供 AI 对话融合检索（租户层优先）
    4. 账号注销      POST /admin/account/cancel {password}
                     当日仍可登录，次日零点停用；数据保留；API Key 同步禁用
-->
<template>
  <!-- 首登强改密提示（登录响应 must_change_password=true 时经 ?must=1 进入） -->
  <div class="card" v-if="route.query.must">
    <p style="color:var(--warn)">⚠️ 首次登录请先修改密码后再使用其他功能</p>
  </div>

  <!-- 块1：修改密码 -->
  <div class="card">
    <h3>修改密码</h3>
    <label>旧密码</label><input v-model="oldPwd" type="password" />
    <label>新密码（≥8位含字母数字）</label><input v-model="newPwd" type="password" />
    <button class="btn" @click="changePwd">修改</button>
    <p v-if="pwdMsg" class="muted">{{ pwdMsg }}</p>
  </div>

  <!-- 块2：换绑邮箱 -->
  <div class="card">
    <h3>换绑邮箱</h3>
    <p class="muted">新邮箱曾参与奖励领取时不可用于换绑</p>
    <div class="row">
      <input v-model="newEmail" placeholder="新邮箱" />
      <button class="btn gray" @click="sendCode" :disabled="cd>0">{{ cd>0?cd+'s':'发验证码' }}</button>
    </div>
    <label>验证码</label><input v-model="emailCode" />
    <button class="btn" @click="bindEmail">换绑</button>
    <p v-if="emMsg" class="muted">{{ emMsg }}</p>
  </div>

  <!-- 块3：企业知识库 -->
  <div class="card">
    <h3>我的企业知识库</h3>
    <p class="muted">上传产品/企业资料，AI 对话自动融合检索（租户层优先）</p>
    <label>标题</label><input v-model="kbTitle" placeholder="如：售后政策" />
    <label>内容</label><textarea v-model="kbContent" rows="4"></textarea>
    <button class="btn" @click="uploadKb">上传切片入库</button>
    <ul style="margin-top:10px;padding-left:18px">
      <li v-for="f in kbList" :key="f.id" style="margin:6px 0">
        {{ f.title }}
        <a href="#" @click.prevent="delKb(f.id)" style="color:var(--warn);margin-left:8px">删除</a>
      </li>
    </ul>
  </div>

  <!-- 块4：账号注销 -->
  <div class="card" style="border:1px solid #fecaca">
    <h3 style="color:var(--warn)">⚠️ 账号注销</h3>
    <p class="muted">今日内仍可登录，明日零点起停用；数据保留不删除；名下 API Key 同步禁用。</p>
    <label>输入登录密码确认</label><input v-model="cancelPwd" type="password" />
    <button class="btn warn" @click="cancelAccount">申请注销</button>
    <p v-if="cMsg" class="muted">{{ cMsg }}</p>
  </div>
</template>
<script setup>
import { ref, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../lib/api.js'

const route = useRoute()

// ---- 块1 修改密码 ----
const oldPwd = ref(''), newPwd = ref(''), pwdMsg = ref('')
async function changePwd(){
  const j = await api('/auth/change-password', {
    method:'POST', body:{ old_password: oldPwd.value, new_password: newPwd.value },
  })
  pwdMsg.value = j.code===0 ? '✓ 已修改' : (j.message||'失败')
}

// ---- 块2 换绑邮箱 ----
const newEmail = ref(''), emailCode = ref(''), emMsg = ref(''), cd = ref(0)
/** 发送验证码到新邮箱（60s前端倒计时展示） */
async function sendCode(){
  if(!newEmail.value) return
  await api('/auth/email-code', { method:'POST', body:{ email:newEmail.value } })
  cd.value=60; const t=setInterval(()=>{cd.value--;if(cd.value<=0)clearInterval(t)},1000)
}
/** 提交换绑：撞库/唯一性由后端校验 */
async function bindEmail(){
  const j = await api('/auth/email/change', { method:'POST', body:{ new_email:newEmail.value, code:emailCode.value } })
  emMsg.value = j.message
}

// ---- 块3 企业知识库 ----
const kbTitle = ref(''), kbContent = ref(''), kbList = ref([])
/** 上传并切片入库（后端按段落聚合约400字/片） */
async function uploadKb(){
  const j = await api('/admin/kb/upload', {
    method:'POST',
    body:{ title: kbTitle.value, content: kbContent.value, category:'企业知识' },
  })
  alert(j.message); if(j.code===0){ kbTitle.value=''; kbContent.value=''; loadKb() }
}
/** 加载我的知识片段列表 */
async function loadKb(){
  const j = await api('/admin/kb/my?page=1&page_size=50')
  if (j.code===0) kbList.value = j.data.list || []
}
/** 删除指定片段 */
async function delKb(id){
  await api('/admin/kb/my/'+id, { method:'DELETE' }); loadKb()
}

// ---- 块4 账号注销 ----
const cancelPwd = ref(''), cMsg = ref('')
/** 申请注销：需登录密码二次确认；次日生效 */
async function cancelAccount(){
  if (!confirm('确认注销？次日零点起账号停用（数据保留）')) return
  const j = await api('/admin/account/cancel', { method:'POST', body:{ password: cancelPwd.value } })
  cMsg.value = j.message
}

onMounted(loadKb)
</script>

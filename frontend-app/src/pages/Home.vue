<!--
  首页导航：六大功能入口卡片
  登录态由顶栏控制链接显隐；本页始终可访问
-->
<template>
  <div class="card">
    <h2>AI 销售引擎工作台</h2>
    <p class="muted" style="margin-top:6px">策略锚定 · 行业知识库 · 流程培育 · 数据飞轮</p>
  </div>
  <div class="grid" style="grid-template-columns:1fr 1fr;padding:0 12px">
    <div class="card"><h3>💬 客户对话</h3><p class="muted">C端访客入口</p><br/><button class="btn" @click="$router.push('/chat')">进入</button></div>
    <div class="card"><h3>🧑‍💼 顾问台</h3><p class="muted">客户/会话/接管</p><br/><button class="btn" @click="$router.push('/advisor')">进入</button></div>
    <div class="card"><h3>💰 收银台</h3><p class="muted">三桶余额/套餐充值</p><br/><button class="btn" @click="$router.push('/billing')">进入</button></div>
    <div class="card"><h3>🎁 邀请推广</h3><p class="muted">链接/二维码/奖励</p><br/><button class="btn" @click="$router.push('/referral')">进入</button></div>
    <div class="card"><h3>⚙️ 账号设置</h3><p class="muted">改密/换绑/知识库/注销</p><br/><button class="btn gray" @click="$router.push('/settings')">进入</button></div>
    <div class="card"><h3>🧾 定价</h3><p class="muted">行业包免费 · token计费</p><br/><button class="btn gray" @click="$router.push('/billing')">查看</button></div>
  </div>
</template>

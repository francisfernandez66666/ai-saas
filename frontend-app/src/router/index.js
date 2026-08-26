/**
 * 路由表定义
 * ----------------------------------------------------------------------------
 * 公开页：/ 首页导航 · /login 登录 · /register 租户入驻（支持 ?ref= 邀请码）
 * 免登页：/chat C端访客对话（服务端按来源创建访客，无需账号）
 * 鉴权页（meta.auth=true）：顾问台 / 收银台 / 邀请推广 / 账号设置
 */
import Home from '../pages/Home.vue'
import Login from '../pages/Login.vue'
import Register from '../pages/Register.vue'
import Chat from '../pages/Chat.vue'
import Advisor from '../pages/Advisor.vue'
import Billing from '../pages/Billing.vue'
import Referral from '../pages/Referral.vue'
import Settings from '../pages/Settings.vue'

export const routes = [
  { path: '/', component: Home },
  { path: '/login', component: Login },
  { path: '/register', component: Register },
  { path: '/chat', component: Chat },
  // ---- 以下页面依赖登录态（守卫见 main.js）----
  { path: '/advisor', component: Advisor, meta: { auth: true } },
  { path: '/billing', component: Billing, meta: { auth: true } },
  { path: '/referral', component: Referral, meta: { auth: true } },
  { path: '/settings', component: Settings, meta: { auth: true } },
]

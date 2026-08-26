/**
 * ============================================================================
 * AI-SCRM 前端应用入口（Vue3 + vue-router · hash 路由）
 * ----------------------------------------------------------------------------
 * 职责：
 *   1. 创建 Vue 应用并挂载 #app
 *   2. 注册全局路由（hash 模式——免服务端 history 回退配置，
 *      同时兼容 Gin 子路径托管(/app/) 与 Capacitor APK 打包协议）
 *   3. 登录态路由守卫：meta.auth 页面在无 token 时强制跳转登录页
 *
 * Token 存储：localStorage 'scrm_auth_token'（与旧版 HTML 页面共享键名，
 * 双前端并行期间可互享登录态）。
 * ============================================================================
 */
import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import App from './App.vue'
import './style.css'
import { routes } from './router/index.js'

// 创建 hash 路由器
const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

// 全局前置守卫：需要登录的页面校验 token，缺失则携带 redirect 参数跳登录
router.beforeEach((to) => {
  const auth = localStorage.getItem('scrm_auth_token')
  if (to.meta.auth && !auth) return { path: '/login', query: { redirect: to.fullPath } }
})

createApp(App).use(router).mount('#app')

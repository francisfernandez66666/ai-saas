<!--
  应用根组件：全局顶栏 + 路由出口
  顶栏按登录态动态显式导航项；退出按钮清除 token 后回登录页
-->
<template>
  <div class="topbar">
    <b @click="$router.push('/')" style="cursor:pointer">AI-SCRM</b>
    <router-link to="/chat">对话</router-link>
    <router-link to="/advisor" v-if="token">顾问台</router-link>
    <router-link to="/billing" v-if="token">收银台</router-link>
    <router-link to="/referral" v-if="token">邀请</router-link>
    <router-link to="/settings" v-if="token">设置</router-link>
    <span style="flex:1"></span>
    <router-link to="/login" v-if="!token">登录</router-link>
    <a href="#" v-else @click.prevent="logout">退出</a>
  </div>
  <router-view />
</template>
<script setup>
import { getToken, setToken } from './lib/api.js'

// 登录态仅在组件创建时读取一次；登出后整页刷新重置全局状态
const token = getToken()

/** 退出登录：清 token → 回登录页 → 刷新以重置各页面缓存状态 */
const logout = () => { setToken(''); location.hash = '#/login'; location.reload() }
</script>

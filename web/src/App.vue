<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { api } from './api'
import { isAdmin, logout, ready, user } from './session'

const route = useRoute()
const router = useRouter()
const meta = ref({ version: '', build_id: '' })
const onLogin = computed(() => route.name === 'login')

const primaryNav = [
  { to: '/', label: '概览' },
  { to: '/subscriptions', label: '我的订阅' },
  { to: '/channels', label: '通知方式' },
  { to: '/settings', label: '账号设置' },
]
const adminNav = [
  { to: '/admin/users', label: '用户管理' },
  { to: '/admin/pool', label: '轮询池' },
  { to: '/admin/ratelimit', label: '限速状态' },
  { to: '/admin/settings', label: '全局设置' },
  { to: '/admin/audit', label: '审计日志' },
]

onMounted(async () => {
  try {
    meta.value = await api.get<{ version: string; build_id: string }>('/api/meta')
  } catch {
    // The version line is cosmetic; never block the UI on it.
  }
})

async function signOut(): Promise<void> {
  await logout()
  await router.push('/login')
}
</script>

<template>
  <div v-if="!ready" class="boot">正在加载 FeedWatch…</div>

  <div v-else-if="onLogin" class="auth-screen">
    <RouterView />
  </div>

  <div v-else class="layout">
    <aside class="sidebar">
      <RouterLink to="/" class="brand">
        <span class="brand-mark">FW</span>
        <span>
          <strong>FeedWatch</strong>
          <small>订阅更新监控</small>
        </span>
      </RouterLink>

      <nav>
        <p class="nav-label">工作台</p>
        <RouterLink v-for="item in primaryNav" :key="item.to" :to="item.to" class="nav-item">
          {{ item.label }}
        </RouterLink>

        <template v-if="isAdmin">
          <p class="nav-label">管理</p>
          <RouterLink v-for="item in adminNav" :key="item.to" :to="item.to" class="nav-item">
            {{ item.label }}
          </RouterLink>
        </template>
      </nav>

      <div class="sidebar-foot">v{{ meta.version || 'dev' }} · {{ meta.build_id || 'dev' }}</div>
    </aside>

    <div class="main">
      <header class="topbar">
        <span class="topbar-title">WebUI 控制台</span>
        <div class="topbar-user">
          <span class="badge" :class="isAdmin ? 'badge-admin' : ''">{{ isAdmin ? '管理员' : '用户' }}</span>
          <span>{{ user?.username }}</span>
          <button class="ghost" type="button" @click="signOut">退出登录</button>
        </div>
      </header>
      <main class="content"><RouterView /></main>
    </div>
  </div>
</template>

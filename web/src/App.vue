<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { api } from './api'
import { isAdmin, logout, ready, user } from './session'
import { catalogAdminPages, catalogAdminPrefix } from './catalogAdmin'
import type { CatalogAdminPage } from './catalogAdmin'

interface NavItem {
  to: string
  label: string
  children?: CatalogAdminPage[]
}

const route = useRoute()
const router = useRouter()
const meta = ref({ version: '', build_id: '' })
const onLogin = computed(() => route.name === 'login')

// Administrators are an operations role: they never see the subscription and
// channel pages, because they cannot own either. Everyone else gets the
// subscriber workspace plus their account settings.
const adminNav: NavItem[] = [
  { to: '/', label: '概览' },
  { to: '/admin/users', label: '用户管理' },
  { to: '/admin/catalog', label: '目录管理', children: catalogAdminPages },
  { to: '/admin/pool', label: '轮询池' },
  { to: '/admin/ratelimit', label: '限速状态' },
  { to: '/admin/settings', label: '全局设置' },
  { to: '/admin/audit', label: '审计日志' },
  { to: '/settings', label: '账号设置' },
]
const memberNav: NavItem[] = [
  { to: '/', label: '概览' },
  { to: '/catalog', label: '订阅中心' },
  { to: '/subscriptions', label: '我的订阅' },
  { to: '/channels', label: '通知方式' },
  { to: '/settings', label: '账号设置' },
]
const navItems = computed(() => (isAdmin.value ? adminNav : memberNav))
const navLabel = computed(() => (isAdmin.value ? '管理' : '工作台'))

// The catalog submenu is contextual: it unfolds only while the user is in the
// catalog group, so the other admin pages keep the short eight-item sidebar.
// The group parent links to /admin/catalog (the group entry), never to one of
// its children, so Vue Router cannot mark both parent and child as exact-active.
const inCatalogGroup = computed(() => isAdmin.value && route.path.startsWith(catalogAdminPrefix))

// /catalog/pending is a sibling route, not a child of /catalog, and it no
// longer has its own sidebar item. Keep the catalog item highlighted there
// with an explicit prefix check: router-link-active would not match (flat
// records are not ancestors of each other), and applying it globally would
// make '/' active on every page.
function navActive(to: string): boolean {
  return to === '/catalog' && (route.path === '/catalog' || route.path.startsWith('/catalog/'))
}

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
        <p class="nav-label">{{ navLabel }}</p>
        <template v-for="item in navItems" :key="item.to">
          <RouterLink :to="item.to" class="nav-item"
            :class="{ 'nav-item--active': navActive(item.to), 'nav-group-parent': item.children && inCatalogGroup }">
            {{ item.label }}
            <span v-if="item.children && inCatalogGroup" class="nav-caret" aria-hidden="true">▾</span>
          </RouterLink>
          <template v-if="item.children && inCatalogGroup">
            <RouterLink v-for="sub in item.children" :key="sub.to" :to="sub.to"
              class="nav-item nav-subitem">
              {{ sub.label }}
            </RouterLink>
          </template>
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

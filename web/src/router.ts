import { createRouter, createWebHistory } from 'vue-router'
import { bootstrap, isAdmin, mustChangePassword, ready, user } from './session'

import LoginView from './views/LoginView.vue'
import PasswordView from './views/PasswordView.vue'
import DashboardView from './views/DashboardView.vue'
import SubscriptionsView from './views/SubscriptionsView.vue'
import ChannelsView from './views/ChannelsView.vue'
import SettingsView from './views/SettingsView.vue'
import AdminUsersView from './views/AdminUsersView.vue'
import AdminPoolView from './views/AdminPoolView.vue'
import AdminRateLimitView from './views/AdminRateLimitView.vue'
import AdminSettingsView from './views/AdminSettingsView.vue'
import AdminAuditView from './views/AdminAuditView.vue'

const router = createRouter({
  // The Go side serves index.html for any non-asset GET path, so history mode
  // works without a hash and deep links survive a reload.
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
    { path: '/', name: 'dashboard', component: DashboardView },
    { path: '/password', name: 'password', component: PasswordView },
    { path: '/subscriptions', name: 'subscriptions', component: SubscriptionsView },
    { path: '/channels', name: 'channels', component: ChannelsView },
    { path: '/settings', name: 'settings', component: SettingsView },
    { path: '/admin/users', name: 'admin-users', component: AdminUsersView, meta: { admin: true } },
    { path: '/admin/pool', name: 'admin-pool', component: AdminPoolView, meta: { admin: true } },
    { path: '/admin/ratelimit', name: 'admin-ratelimit', component: AdminRateLimitView, meta: { admin: true } },
    { path: '/admin/settings', name: 'admin-settings', component: AdminSettingsView, meta: { admin: true } },
    { path: '/admin/audit', name: 'admin-audit', component: AdminAuditView, meta: { admin: true } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  if (!ready.value) await bootstrap()
  if (to.name === 'login') return true
  if (!user.value) return { name: 'login', query: { redirect: to.fullPath } }
  // The server also blocks all other /api/* calls until the initial password
  // is changed; mirroring that in the router avoids a wall of 403s in the UI.
  if (mustChangePassword.value && to.name !== 'password') return { name: 'password' }
  if (to.meta.admin && !isAdmin.value) return { name: 'dashboard' }
  return true
})

export default router

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api, errorMessage } from '../api'
import { formatTime } from '../time'
import { isAdmin, user } from '../session'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface Cycle {
  id: string
  started_at: string
  finished_at: string
  urls: number
  sent: number
  failed: number
}
interface Overview {
  counts: Record<string, number>
  delivery_status: Record<string, number>
  recent_cycles: Cycle[]
}

const err = ref('')
const subs = ref<unknown[]>([])
const channels = ref<unknown[]>([])
const overview = ref<Overview | null>(null)
const meta = ref({ version: '', build_id: '' })

const countLabels: Array<[string, string]> = [
  ['users', '用户'], ['subscriptions', '订阅'], ['channels', '通知渠道'],
  ['feed_pool', '轮询 URL'], ['pending', '待投递'], ['delivery_sent', '已投递'],
  ['delivery_failed', '已放弃'], ['cycles', '轮询轮次'], ['notifications', '已生成通知'],
]

// Administrators have no subscriptions of their own, so their dashboard is the
// operations view only; asking the member endpoints would just return 403.
async function load(): Promise<void> {
  err.value = ''
  try {
    meta.value = await api.get<{ version: string; build_id: string }>('/api/meta')
    if (isAdmin.value) {
      overview.value = await api.get<Overview>('/api/admin/overview')
      return
    }
    const [s, c] = await Promise.all([
      api.get<{ subscriptions: unknown[] }>('/api/subscriptions'),
      api.get<{ channels: unknown[] }>('/api/channels'),
    ])
    subs.value = s.subscriptions
    channels.value = c.channels
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)
</script>

<template>
  <PageHeader title="概览" :description="`欢迎回来，${user?.username ?? ''}。`">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />

  <div v-if="isAdmin" class="banner">
    当前是管理员账号：只能管理用户与查看运行数据，不能自己配置订阅或通知。需要订阅时请创建普通用户，并用该用户登录。
  </div>

  <div class="stat-grid" style="margin-bottom: 1.2rem">
    <template v-if="isAdmin">
      <div class="stat"><span class="label">用户</span><span class="value">{{ overview?.counts.users ?? '—' }}</span></div>
      <div class="stat"><span class="label">订阅总数</span><span class="value">{{ overview?.counts.subscriptions ?? '—' }}</span></div>
      <div class="stat"><span class="label">通知渠道</span><span class="value">{{ overview?.counts.channels ?? '—' }}</span></div>
      <div class="stat"><span class="label">待投递</span><span class="value">{{ overview?.counts.pending ?? '—' }}</span></div>
    </template>
    <template v-else>
      <div class="stat"><span class="label">我的订阅</span><span class="value">{{ subs.length }}</span></div>
      <div class="stat"><span class="label">我的通知方式</span><span class="value">{{ channels.length }}</span></div>
    </template>
    <div class="stat"><span class="label">版本</span><span class="value" style="font-size: 1.1rem">{{ meta.version || 'dev' }}</span></div>
    <div class="stat"><span class="label">前端构建</span><span class="value mono" style="font-size: .9rem">{{ meta.build_id || 'dev' }}</span></div>
  </div>

  <div class="grid-2">
    <div class="card">
      <h2>{{ isAdmin ? '运维入口' : '快速开始' }}</h2>
      <template v-if="isAdmin">
        <p class="card-sub">创建普通用户、查看轮询池与投递状态。用户的订阅与通知只能启停，不能代改。</p>
        <div class="form-actions">
          <RouterLink to="/admin/users" class="link-button">用户管理</RouterLink>
          <RouterLink to="/admin/pool" class="link-button">轮询池</RouterLink>
          <RouterLink to="/admin/ratelimit" class="link-button">限速状态</RouterLink>
        </div>
      </template>
      <template v-else>
        <p class="card-sub">订阅一个 release.atom 地址，再配置一种通知方式即可开始接收更新。</p>
        <div class="form-actions">
          <RouterLink to="/subscriptions" class="link-button">管理订阅</RouterLink>
          <RouterLink to="/channels" class="link-button">管理通知方式</RouterLink>
        </div>
        <p class="hint">同一 URL 只会在每轮抓取一次；新订阅从当前基线开始，不会补发历史版本。</p>
      </template>
    </div>

    <div v-if="isAdmin" class="card">
      <h2>投递状态</h2>
      <p class="card-sub">按状态统计的投递组（delivery_groups）。</p>
      <div v-if="overview">
        <p v-for="(n, status) in overview.delivery_status" :key="status" style="margin: .3rem 0; font-size: .88rem">
          <span class="tag">{{ status }}</span> <strong>{{ n }}</strong>
        </p>
        <p v-if="!Object.keys(overview.delivery_status).length" class="empty">暂无投递记录</p>
      </div>
      <p v-else class="empty">加载中…</p>
    </div>
  </div>

  <div v-if="isAdmin" class="card">
    <h2>全局计数</h2>
    <div class="stat-grid" style="margin-top: .8rem">
      <div v-for="[key, label] in countLabels" :key="key" class="stat">
        <span class="label">{{ label }}</span>
        <span class="value">{{ overview?.counts[key] ?? '—' }}</span>
      </div>
    </div>
  </div>

  <div v-if="isAdmin" class="card">
    <h2>最近轮询轮次</h2>
    <div class="table-wrap">
      <table>
        <thead><tr><th>轮次</th><th>开始</th><th>结束</th><th>URL</th><th>成功</th><th>失败</th></tr></thead>
        <tbody>
          <tr v-for="cycle in overview?.recent_cycles ?? []" :key="cycle.id">
            <td class="mono">{{ cycle.id }}</td>
            <td class="mono">{{ formatTime(cycle.started_at) }}</td>
            <td class="mono">{{ formatTime(cycle.finished_at) }}</td>
            <td>{{ cycle.urls }}</td><td>{{ cycle.sent }}</td><td>{{ cycle.failed }}</td>
          </tr>
          <tr v-if="!overview?.recent_cycles?.length"><td colspan="6" class="empty">暂无轮次记录</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

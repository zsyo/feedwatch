<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api, errorMessage } from '../api'
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

async function load(): Promise<void> {
  err.value = ''
  try {
    const [s, c, m] = await Promise.all([
      api.get<{ subscriptions: unknown[] }>('/api/subscriptions'),
      api.get<{ channels: unknown[] }>('/api/channels'),
      api.get<{ version: string; build_id: string }>('/api/meta'),
    ])
    subs.value = s.subscriptions
    channels.value = c.channels
    meta.value = m
    if (isAdmin.value) overview.value = await api.get<Overview>('/api/admin/overview')
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)
</script>

<template>
  <PageHeader title="概览" :description="`欢迎回来，${user?.username ?? ''}。这里汇总你的订阅与（管理员可见的）全局运行状态。`">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />

  <div class="stat-grid" style="margin-bottom: 1.2rem">
    <div class="stat"><span class="label">我的订阅</span><span class="value">{{ subs.length }}</span></div>
    <div class="stat"><span class="label">我的通知方式</span><span class="value">{{ channels.length }}</span></div>
    <div class="stat"><span class="label">版本</span><span class="value" style="font-size: 1.1rem">{{ meta.version || 'dev' }}</span></div>
    <div class="stat"><span class="label">前端构建</span><span class="value mono" style="font-size: .9rem">{{ meta.build_id || 'dev' }}</span></div>
  </div>

  <div class="grid-2">
    <div class="card">
      <h2>快速开始</h2>
      <p class="card-sub">订阅一个 release.atom 地址，再配置一种通知方式即可开始接收更新。</p>
      <div class="form-actions">
        <RouterLink to="/subscriptions" class="ghost" style="padding: .5rem .9rem; border: 1px solid var(--border); border-radius: 7px">管理订阅</RouterLink>
        <RouterLink to="/channels" class="ghost" style="padding: .5rem .9rem; border: 1px solid var(--border); border-radius: 7px">管理通知方式</RouterLink>
      </div>
      <p class="hint">同一 URL 只会在每轮抓取一次；新订阅从当前基线开始，不会补发历史版本。</p>
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
            <td class="mono">{{ cycle.started_at }}</td>
            <td class="mono">{{ cycle.finished_at || '—' }}</td>
            <td>{{ cycle.urls }}</td><td>{{ cycle.sent }}</td><td>{{ cycle.failed }}</td>
          </tr>
          <tr v-if="!overview?.recent_cycles?.length"><td colspan="6" class="empty">暂无轮次记录</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

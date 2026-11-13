<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, errorMessage } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface PoolRow {
  url_key: string
  url: string
  source_type: string
  has_etag: boolean
  has_last_modified: boolean
  last_checked: string
  last_version: string
  subscribers: number
  users: number
  seen_releases: number
}

const rows = ref<PoolRow[]>([])
const err = ref('')
const busy = ref(false)

async function load(): Promise<void> {
  err.value = ''
  busy.value = true
  try {
    const out = await api.get<{ pool: PoolRow[] }>('/api/admin/pool?limit=500')
    rows.value = out.pool
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}
onMounted(load)
</script>

<template>
  <PageHeader title="全局轮询池" description="每个规范化 URL 每轮只抓取一次，多个用户共享 ETag / 已见版本。URL 中的凭据已被服务端替换为 REDACTED。">
    <button class="ghost" type="button" :disabled="busy" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />

  <div class="card">
    <h2>轮询池（{{ rows.length }}）</h2>
    <p class="card-sub">订阅数统计启用中的订阅；用户数统计所有未删除的订阅。</p>
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>URL</th><th>类型</th><th>订阅 / 用户</th><th>已见版本</th><th>条件请求</th><th>最新稳定版</th><th>上次检查</th></tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.url_key">
            <td class="mono" style="max-width: 420px; word-break: break-all">{{ row.url }}</td>
            <td><span class="tag">{{ row.source_type }}</span></td>
            <td>{{ row.subscribers }} / {{ row.users }}</td>
            <td>{{ row.seen_releases }}</td>
            <td class="muted">
              ETag {{ row.has_etag ? '有' : '无' }} · LM {{ row.has_last_modified ? '有' : '无' }}
            </td>
            <td class="mono">{{ row.last_version || '—' }}</td>
            <td class="mono">{{ row.last_checked || '—' }}</td>
          </tr>
          <tr v-if="!rows.length"><td colspan="7" class="empty">轮询池为空，添加订阅后会自动出现。</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, errorMessage } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface AuditRow {
  id: number
  ts: string
  actor_id: number
  actor_username: string
  action: string
  target: string
  detail_json: string
  ip: string
}
interface AuditResponse {
  events: AuditRow[]
  total: number
  limit: number
  offset: number
}

const data = ref<AuditResponse | null>(null)
const err = ref('')
const limit = ref(50)
const offset = ref(0)

async function load(): Promise<void> {
  err.value = ''
  try {
    data.value = await api.get<AuditResponse>(`/api/admin/audit?limit=${limit.value}&offset=${offset.value}`)
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

async function setLimit(value: number): Promise<void> {
  limit.value = value
  offset.value = 0
  await load()
}

async function page(delta: number): Promise<void> {
  const next = Math.max(0, offset.value + delta * limit.value)
  if (data.value && next >= data.value.total) return
  offset.value = next
  await load()
}
</script>

<template>
  <PageHeader title="审计日志" description="记录所有写操作（登录、增删改、设置变更），按时间倒序。">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />

  <div class="card">
    <div class="page-actions" style="margin-bottom: 1rem">
      <span class="muted">每页</span>
      <select :value="limit" style="width: auto; margin: 0" @change="setLimit(Number(($event.target as HTMLSelectElement).value))">
        <option :value="20">20</option>
        <option :value="50">50</option>
        <option :value="100">100</option>
        <option :value="200">200</option>
      </select>
      <span class="muted">共 {{ data?.total ?? 0 }} 条</span>
      <button class="small" type="button" :disabled="offset === 0" @click="page(-1)">上一页</button>
      <button class="small" type="button" :disabled="!data || offset + limit >= data.total" @click="page(1)">下一页</button>
    </div>
    <div class="table-wrap">
      <table>
        <thead><tr><th>ID</th><th>时间</th><th>操作者</th><th>动作</th><th>目标</th><th>IP</th></tr></thead>
        <tbody>
          <tr v-for="row in data?.events ?? []" :key="row.id">
            <td class="mono">{{ row.id }}</td>
            <td class="mono">{{ row.ts }}</td>
            <td>{{ row.actor_username || (row.actor_id ? `id:${row.actor_id}` : '匿名') }}</td>
            <td class="mono">{{ row.action }}</td>
            <td class="mono">{{ row.target }}</td>
            <td class="mono">{{ row.ip }}</td>
          </tr>
          <tr v-if="!data?.events?.length"><td colspan="6" class="empty">暂无审计记录</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

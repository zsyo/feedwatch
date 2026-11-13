<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, errorMessage } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface Subscription {
  id: number
  url: string
  url_key: string
  display_name: string
  source_type: string
  notify_policy: 'always' | 'on_update'
  mode: 'digest' | 'per_feed'
  enabled: boolean
  seeded_at: string | null
}

const items = ref<Subscription[]>([])
const err = ref('')
const success = ref('')
const busy = ref(false)
const draft = ref({ url: '', display_name: '', notify_policy: 'on_update', mode: 'digest' })

async function load(): Promise<void> {
  err.value = ''
  try {
    const out = await api.get<{ subscriptions: Subscription[] }>('/api/subscriptions')
    items.value = out.subscriptions
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

async function create(): Promise<void> {
  err.value = ''
  success.value = ''
  busy.value = true
  try {
    await api.post('/api/subscriptions', {
      url: draft.value.url.trim(),
      display_name: draft.value.display_name.trim(),
      notify_policy: draft.value.notify_policy,
      mode: draft.value.mode,
    })
    draft.value = { url: '', display_name: '', notify_policy: 'on_update', mode: 'digest' }
    success.value = '订阅已创建，从当前基线开始，不会补发历史版本。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

async function save(row: Subscription): Promise<void> {
  err.value = ''
  success.value = ''
  try {
    await api.patch(`/api/subscriptions/${row.id}`, {
      display_name: row.display_name,
      notify_policy: row.notify_policy,
      mode: row.mode,
      enabled: row.enabled,
    })
    success.value = `订阅 #${row.id} 已保存`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function remove(row: Subscription): Promise<void> {
  if (!window.confirm(`确定删除订阅「${row.display_name || row.url}」？删除后重新订阅也不会补发历史版本。`)) return
  err.value = ''
  success.value = ''
  try {
    await api.del(`/api/subscriptions/${row.id}`)
    success.value = '订阅已删除'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}
</script>

<template>
  <PageHeader title="我的订阅" description="管理要轮询的 release.atom 地址。相同 URL 全局只抓取一次，各自按策略生成通知。">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="card">
    <h2>新增订阅</h2>
    <form @submit.prevent="create">
      <div class="form-row">
        <label>订阅 URL
          <input v-model="draft.url" type="url" placeholder="https://github.com/owner/repo/releases.atom" required />
        </label>
        <label>显示名称<input v-model="draft.display_name" placeholder="选填，用于通知标题" /></label>
        <label>通知策略
          <select v-model="draft.notify_policy">
            <option value="on_update">仅有更新时通知</option>
            <option value="always">每轮都发状态摘要</option>
          </select>
        </label>
        <label>通知形式
          <select v-model="draft.mode">
            <option value="digest">汇总一条</option>
            <option value="per_feed">每个项目一条</option>
          </select>
        </label>
      </div>
      <div class="form-actions"><button class="primary" type="submit" :disabled="busy">添加订阅</button></div>
    </form>
  </div>

  <div class="card">
    <h2>订阅列表</h2>
    <p class="card-sub">表格中可直接修改，点「保存」生效。</p>
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>ID</th><th>项目 / URL</th><th>策略</th><th>形式</th><th>启用</th><th>操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="row in items" :key="row.id">
            <td class="mono">{{ row.id }}</td>
            <td style="min-width: 260px">
              <input v-model="row.display_name" placeholder="显示名称" style="margin: 0 0 .35rem" />
              <div class="mono muted" style="word-break: break-all">{{ row.url }}</div>
            </td>
            <td>
              <select v-model="row.notify_policy" style="min-width: 150px">
                <option value="on_update">仅更新</option>
                <option value="always">每轮摘要</option>
              </select>
            </td>
            <td>
              <select v-model="row.mode" style="min-width: 120px">
                <option value="digest">汇总</option>
                <option value="per_feed">逐项目</option>
              </select>
            </td>
            <td><label class="inline-check"><input v-model="row.enabled" type="checkbox" /></label></td>
            <td>
              <div class="cell-actions">
                <button class="small" type="button" @click="save(row)">保存</button>
                <button class="small" type="button" @click="remove(row)">删除</button>
              </div>
            </td>
          </tr>
          <tr v-if="!items.length"><td colspan="6" class="empty">还没有订阅，先添加一个 release.atom 地址吧。</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

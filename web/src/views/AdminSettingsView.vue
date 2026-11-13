<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, errorMessage } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface SettingRow {
  key: string
  value: string
  updated_at: string
  editable: boolean
}
interface SettingsResponse {
  settings: SettingRow[]
  editable: Record<string, string>
  env_only: string[]
  informational: string[]
  applies: string
  note: string
}

const data = ref<SettingsResponse | null>(null)
const draft = ref<Record<string, string>>({})
const err = ref('')
const success = ref('')
const busy = ref(false)

function apply(out: SettingsResponse): void {
  data.value = out
  const next: Record<string, string> = {}
  for (const row of out.settings) if (row.editable) next[row.key] = row.value
  draft.value = next
}

async function load(): Promise<void> {
  err.value = ''
  try {
    apply(await api.get<SettingsResponse>('/api/admin/settings'))
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

function changed(): Record<string, string> {
  const out: Record<string, string> = {}
  for (const row of data.value?.settings ?? []) {
    if (row.editable && draft.value[row.key] !== row.value) out[row.key] = draft.value[row.key]
  }
  return out
}

async function save(): Promise<void> {
  err.value = ''
  success.value = ''
  const patch = changed()
  if (!Object.keys(patch).length) {
    success.value = '没有待保存的修改'
    return
  }
  busy.value = true
  try {
    apply(await api.put<SettingsResponse>('/api/admin/settings', { settings: patch }))
    success.value = '设置已保存，重启进程后生效。'
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

function kindOf(key: string): string {
  if (data.value?.env_only.includes(key)) return '环境变量控制'
  if (data.value?.informational.includes(key)) return '只读参考'
  return '只读'
}
</script>

<template>
  <PageHeader title="全局设置" description="数据库是首次启动之后的唯一配置来源；修改在此保存，进程重启后由组合根读取。">
    <button class="ghost" type="button" @click="load">重新加载</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />
  <Notice kind="info" :message="data?.note" />

  <div class="card">
    <h2>可编辑参数</h2>
    <p class="card-sub">服务端会逐项校验类型与范围，全部合法才在同一事务中写入。</p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>键</th><th>值</th><th>说明</th><th>更新时间</th></tr></thead>
        <tbody>
          <tr v-for="row in (data?.settings ?? []).filter((r) => r.editable)" :key="row.key">
            <td class="mono">{{ row.key }}</td>
            <td style="min-width: 200px"><input v-model="draft[row.key]" /></td>
            <td class="muted">{{ data?.editable[row.key] }}</td>
            <td class="mono">{{ row.updated_at }}</td>
          </tr>
          <tr v-if="!data?.settings.some((r) => r.editable)"><td colspan="4" class="empty">加载中…</td></tr>
        </tbody>
      </table>
    </div>
    <div class="form-actions" style="margin-top: 1rem">
      <button class="primary" type="button" :disabled="busy" @click="save">保存修改</button>
    </div>
  </div>

  <div class="card">
    <h2>只读参数</h2>
    <p class="card-sub">
      Cookie Secure 与会话有效期由服务启动环境变量决定（防止在纯 HTTP 上误开 Secure 导致无人可登录）；
      notify.mode 在多租户模式下属于每个订阅自己的策略。
    </p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>键</th><th>值</th><th>原因</th></tr></thead>
        <tbody>
          <tr v-for="row in (data?.settings ?? []).filter((r) => !r.editable)" :key="row.key">
            <td class="mono">{{ row.key }}</td>
            <td class="mono">{{ row.value }}</td>
            <td><span class="tag">{{ kindOf(row.key) }}</span></td>
          </tr>
          <tr v-if="!data?.settings.some((r) => !r.editable)"><td colspan="3" class="empty">无只读参数</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

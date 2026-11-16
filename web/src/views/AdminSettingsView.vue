<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, errorMessage } from '../api'
import { formatTime } from '../time'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

/** Purpose line, accepted format and default, plus the long explanation. */
interface SettingDoc {
  summary: string
  format: string
  default?: string
  detail?: string
}
interface SettingRow {
  key: string
  value: string
  updated_at: string
  editable: boolean
}
interface SettingsResponse {
  settings: SettingRow[]
  docs: Record<string, SettingDoc>
  env_only: string[]
  informational: string[]
  auth_managed: string[]
  applies: string
  note: string
}

const EMPTY_DOC: SettingDoc = { summary: '', format: '' }

const data = ref<SettingsResponse | null>(null)
const draft = ref<Record<string, string>>({})
const expanded = ref<Set<string>>(new Set())
const err = ref('')
const success = ref('')
const busy = ref(false)

interface GithubState { set: boolean; graphql: boolean }
const github = ref<GithubState>({ set: false, graphql: false })
const githubToken = ref('')
const githubBusy = ref(false)
const githubSuccess = ref('')

const editableRows = computed(() => (data.value?.settings ?? []).filter((r) => r.editable))
const readonlyRows = computed(() => (data.value?.settings ?? []).filter((r) => !r.editable))

function docFor(key: string): SettingDoc {
  return data.value?.docs[key] ?? EMPTY_DOC
}

function toggle(key: string): void {
  // Replace the Set instead of mutating it: Vue tracks the ref assignment, not
  // Set internals, and this keeps the template reactive.
  const next = new Set(expanded.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expanded.value = next
}

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
    github.value = await api.get<GithubState>('/api/admin/github-token')
    githubToken.value = ''
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

async function clearGithub(): Promise<void> {
  if (!window.confirm('确定清除已保存的 GitHub token？清除后 GitHub 订阅改用匿名访问，限流会降到每小时 60 次。')) return
  err.value = ''
  githubSuccess.value = ''
  githubBusy.value = true
  try {
    github.value = await api.put<GithubState>('/api/admin/github-token', {
      clear: true, graphql: github.value.graphql,
    })
    githubToken.value = ''
    githubSuccess.value = '已清除，重启进程后生效。'
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    githubBusy.value = false
  }
}

async function saveGithub(): Promise<void> {
  err.value = ''
  githubSuccess.value = ''
  githubBusy.value = true
  try {
    github.value = await api.put<GithubState>('/api/admin/github-token', {
      token: githubToken.value.trim(),
      graphql: github.value.graphql,
    })
    githubToken.value = ''
    githubSuccess.value = '已保存，重启进程后生效。'
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    githubBusy.value = false
  }
}

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
  if (data.value?.auth_managed.includes(key)) return '登录与注册页管理'
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
    <h2>GitHub 访问凭据</h2>
    <p class="card-sub">
      用于追踪没有 releases.atom 的仓库。留空表示不修改已保存的 token；密文加密存放，页面不会回显。匿名访问每小时 60 次，带 token 每小时 5000 次。
    </p>
    <div class="form-row">
      <label>GitHub token
        <input v-model="githubToken" type="password" autocomplete="off"
          :placeholder="github.set ? '已保存，留空表示不修改' : '粘贴 Personal Access Token'" />
      </label>
      <label class="inline-check" style="align-self: end; padding-bottom: .5rem">
        <input v-model="github.graphql" type="checkbox" /> 启用 GraphQL 批量查询
      </label>
    </div>
    <p class="hint">
      GraphQL 一次请求可查多个仓库，减少请求数，但需要 token；它没有条件请求，每轮都会重新拉取最新版本。不启用时每个仓库走 REST 逐仓库抓取。
    </p>
    <div class="form-actions">
      <button class="primary" type="button" :disabled="githubBusy" @click="saveGithub">保存</button>
      <button class="ghost" type="button" :disabled="githubBusy || !github.set" @click="clearGithub">清除 token</button>
    </div>
    <Notice kind="success" :message="githubSuccess" />
  </div>

  <div class="card">
    <h2>可编辑参数</h2>
    <p class="card-sub">
      键名下面一行是这个参数的作用；输入框下方给出格式与默认值。点右侧「说明」展开调整建议与它和其他参数的关系。
    </p>
    <div class="table-wrap">
      <table class="settings-table">
        <thead>
          <tr><th>参数</th><th>值</th><th>更新时间</th><th></th></tr>
        </thead>
        <tbody>
          <template v-for="row in editableRows" :key="row.key">
            <tr>
              <td class="setting-name">
                <div class="mono">{{ row.key }}</div>
                <div class="setting-purpose" :title="docFor(row.key).summary">{{ docFor(row.key).summary }}</div>
              </td>
              <td class="setting-value">
                <input v-model="draft[row.key]" :placeholder="docFor(row.key).default" spellcheck="false" />
                <div class="setting-meta">
                  {{ docFor(row.key).format }}
                  <template v-if="docFor(row.key).default"> · 默认 <span class="mono">{{ docFor(row.key).default }}</span></template>
                </div>
              </td>
              <td class="mono setting-time">{{ formatTime(row.updated_at) }}</td>
              <td class="setting-toggle">
                <button
                  class="small"
                  type="button"
                  :aria-expanded="expanded.has(row.key)"
                  @click="toggle(row.key)"
                >{{ expanded.has(row.key) ? '收起' : '说明' }}</button>
              </td>
            </tr>
            <tr v-if="expanded.has(row.key)" class="setting-detail">
              <td colspan="4">
                <p>{{ docFor(row.key).detail }}</p>
              </td>
            </tr>
          </template>
          <tr v-if="!editableRows.length"><td colspan="4" class="empty">加载中…</td></tr>
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
      Cookie Secure 与会话有效期由启动时的环境变量决定（防止在纯 HTTP 上误开 Secure 导致无人可登录）；
      notify.mode 是账号级偏好，在「通知方式」页按账号设置，这里显示的只是旧数据库遗留的默认值。
    </p>
    <div class="table-wrap">
      <table class="settings-table">
        <thead><tr><th>参数</th><th>值</th><th>原因</th><th></th></tr></thead>
        <tbody>
          <template v-for="row in readonlyRows" :key="row.key">
            <tr>
              <td class="setting-name">
                <div class="mono">{{ row.key }}</div>
                <div class="setting-purpose">{{ docFor(row.key).summary }}</div>
              </td>
              <td class="mono">{{ row.value }}</td>
              <td><span class="tag">{{ kindOf(row.key) }}</span></td>
              <td class="setting-toggle">
                <button
                  v-if="docFor(row.key).detail"
                  class="small"
                  type="button"
                  :aria-expanded="expanded.has(row.key)"
                  @click="toggle(row.key)"
                >{{ expanded.has(row.key) ? '收起' : '说明' }}</button>
              </td>
            </tr>
            <tr v-if="expanded.has(row.key)" class="setting-detail">
              <td colspan="4">
                <p>{{ docFor(row.key).detail }}</p>
                <p v-if="docFor(row.key).format" class="setting-detail-format">来源：{{ docFor(row.key).format }}</p>
              </td>
            </tr>
          </template>
          <tr v-if="!readonlyRows.length"><td colspan="4" class="empty">无只读参数</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

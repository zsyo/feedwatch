<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, errorMessage } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface ChannelSpec {
  type: string
  name?: string
  from?: string
  to?: string[]
  cc?: string[]
  api_key_env?: string
  subject_prefix?: string
  options?: Record<string, string>
}
interface Channel {
  id: number
  type: string
  name: string
  config: ChannelSpec
  enabled: boolean
}

interface FormState {
  type: string
  name: string
  from: string
  to: string
  cc: string
  api_key_env: string
  subject_prefix: string
  options: string
}

const blank = (): FormState => ({
  type: 'email', name: '', from: '', to: '', cc: '', api_key_env: '', subject_prefix: '', options: '{}',
})
const form = ref<FormState>(blank())
const editingId = ref<number | null>(null)
const items = ref<Channel[]>([])
const err = ref('')
const success = ref('')
const busy = ref(false)

const channelTypes = ['email', 'telegram', 'wecom', 'feishu', 'webhook', 'log']

async function load(): Promise<void> {
  err.value = ''
  try {
    const out = await api.get<{ channels: Channel[] }>('/api/channels')
    items.value = out.channels
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

function list(value: string): string[] {
  return value.split(',').map((v) => v.trim()).filter(Boolean)
}

function parseOptions(): Record<string, string> {
  const raw = form.value.options.trim()
  if (!raw) return {}
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    throw new Error('附加参数必须是合法的 JSON 对象')
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error('附加参数必须是 JSON 对象，例如 {"url": "https://..."}')
  }
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(parsed)) out[k] = String(v)
  return out
}

function payload(): ChannelSpec {
  const f = form.value
  const spec: ChannelSpec = {
    type: f.type,
    name: f.name.trim(),
    from: f.from.trim(),
    to: list(f.to),
    cc: list(f.cc),
    api_key_env: f.api_key_env.trim(),
    subject_prefix: f.subject_prefix.trim(),
    options: parseOptions(),
  }
  return spec
}

async function submit(): Promise<void> {
  err.value = ''
  success.value = ''
  let spec: ChannelSpec
  try {
    spec = payload()
  } catch (e) {
    err.value = errorMessage(e)
    return
  }
  busy.value = true
  try {
    if (editingId.value === null) {
      await api.post('/api/channels', spec)
      success.value = '通知方式已创建'
    } else {
      await api.patch(`/api/channels/${editingId.value}`, spec)
      success.value = '通知方式已更新'
    }
    reset()
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

function reset(): void {
  form.value = blank()
  editingId.value = null
}

function startEdit(ch: Channel): void {
  const c = ch.config
  editingId.value = ch.id
  form.value = {
    type: ch.type || c.type || 'email',
    name: ch.name || c.name || '',
    from: c.from || '',
    to: (c.to || []).join(', '),
    cc: (c.cc || []).join(', '),
    api_key_env: c.api_key_env || '',
    subject_prefix: c.subject_prefix || '',
    options: JSON.stringify(c.options || {}, null, 2),
  }
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function remove(ch: Channel): Promise<void> {
  if (!window.confirm(`确定删除通知方式「${ch.name || ch.type} #${ch.id}」？`)) return
  err.value = ''
  success.value = ''
  try {
    await api.del(`/api/channels/${ch.id}`)
    if (editingId.value === ch.id) reset()
    success.value = '通知方式已删除'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}
</script>

<template>
  <PageHeader title="通知方式" description="每个用户使用自己的密钥独立发送；密钥只以环境变量引用保存，绝不写入数据库。">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="card">
    <h2>{{ editingId === null ? '新增通知方式' : `编辑通知方式 #${editingId}` }}</h2>
    <p class="card-sub">
      邮件密钥填环境变量名（如 <code class="mono">RESEND_API_KEY</code>）；webhook 类在附加参数里用
      <code class="mono">${ENV_VAR}</code> 占位符，明文密钥会被服务端拒绝。
    </p>
    <form @submit.prevent="submit">
      <div class="form-row">
        <label>类型
          <select v-model="form.type">
            <option v-for="t in channelTypes" :key="t" :value="t">{{ t }}</option>
          </select>
        </label>
        <label>名称<input v-model="form.name" placeholder="选填" /></label>
        <label>发件人 From<input v-model="form.from" placeholder="email 类型使用" /></label>
        <label>邮件密钥环境变量名<input v-model="form.api_key_env" placeholder="例如 RESEND_API_KEY" /></label>
        <label>收件人 To（逗号分隔）<input v-model="form.to" placeholder="a@example.com, b@example.com" /></label>
        <label>抄送 Cc（逗号分隔）<input v-model="form.cc" placeholder="选填" /></label>
        <label>标题前缀<input v-model="form.subject_prefix" placeholder="选填" /></label>
      </div>
      <label>附加参数（JSON 对象）
        <textarea v-model="form.options" spellcheck="false" placeholder='{"url": "https://api.example/bot${TG_TOKEN}/send"}'></textarea>
      </label>
      <div class="form-actions">
        <button class="primary" type="submit" :disabled="busy">{{ editingId === null ? '添加' : '保存修改' }}</button>
        <button v-if="editingId !== null" class="ghost" type="button" @click="reset">取消编辑</button>
      </div>
    </form>
  </div>

  <div class="card">
    <h2>已配置的通知方式</h2>
    <p class="card-sub">列表回显已脱敏；任何看起来像明文密钥的值都会显示为 ***。</p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>ID</th><th>类型 / 名称</th><th>配置</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="ch in items" :key="ch.id">
            <td class="mono">{{ ch.id }}</td>
            <td>
              <span class="badge">{{ ch.type }}</span>
              <div class="muted" style="margin-top: .3rem">{{ ch.name || '—' }}</div>
            </td>
            <td class="mono" style="max-width: 420px; word-break: break-all">
              <div v-if="ch.config.from">from: {{ ch.config.from }}</div>
              <div v-if="ch.config.to?.length">to: {{ ch.config.to.join(', ') }}</div>
              <div v-if="ch.config.cc?.length">cc: {{ ch.config.cc.join(', ') }}</div>
              <div v-if="ch.config.api_key_env">api_key_env: {{ ch.config.api_key_env }}</div>
              <div v-if="ch.config.subject_prefix">prefix: {{ ch.config.subject_prefix }}</div>
              <div v-if="Object.keys(ch.config.options || {}).length">options: {{ JSON.stringify(ch.config.options) }}</div>
            </td>
            <td><span class="badge" :class="ch.enabled ? 'badge-on' : 'badge-off'">{{ ch.enabled ? '启用' : '停用' }}</span></td>
            <td>
              <div class="cell-actions">
                <button class="small" type="button" @click="startEdit(ch)">编辑</button>
                <button class="small" type="button" @click="remove(ch)">删除</button>
              </div>
            </td>
          </tr>
          <tr v-if="!items.length"><td colspan="5" class="empty">还没有通知方式。</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

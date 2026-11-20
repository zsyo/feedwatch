<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { api, errorMessage } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'
import { saveNotifyMode, user } from '../session'
import type { NotifyMode } from '../session'

type Kind = 'text' | 'password' | 'select' | 'bool' | 'list' | 'textarea'

interface Field {
  key: string
  label: string
  kind: Kind
  scope: 'spec' | 'option' | 'secret'
  required?: boolean
  choices?: string[]
  placeholder?: string
  help?: string
}

interface ChannelConfig {
  type?: string
  name?: string
  from?: string
  to?: string[]
  cc?: string[]
  subject_prefix?: string
  options?: Record<string, string>
}

interface Channel {
  id: number
  type: string
  name: string
  enabled: boolean
  config: ChannelConfig
  secret_fields: string[]
  secret_hints?: Record<string, string>
  secret_state: 'ok' | 'missing' | 'error' | 'none'
}

interface ChannelPreview {
  type: string
  kind: string
  format: 'html' | 'markdown' | 'text'
  subject: string
  body: string
  payload: string
}

type PreviewKind = 'new_releases' | 'baseline'

const types = ref<string[]>([])
const schema = ref<Record<string, Field[]>>({})
const items = ref<Channel[]>([])
const editing = ref<Channel | null>(null)
const originalType = ref('')

const form = reactive({ type: 'email', name: '' })
const values = ref<Record<string, string>>({})
const secrets = ref<Record<string, string>>({})
const clearSecrets = ref<string[]>([])

const err = ref('')
const success = ref('')
const busy = ref(false)

// Notification shape is an account preference, not a channel field: it decides
// how many messages one cycle turns into, regardless of where they are sent.
const mode = ref<NotifyMode>('digest')
const modeBusy = ref(false)
const modeErr = ref('')
const modeOk = ref('')

// The preview is rendered by the server with the same templates and channel
// adapters used for real delivery, so it cannot drift from what users get.
const previewKind = ref<PreviewKind>('new_releases')
const preview = ref<ChannelPreview | null>(null)
const previewErr = ref('')
const previewLoading = ref(false)
let previewTimer: ReturnType<typeof setTimeout> | null = null
let previewSeq = 0

const enabledCount = computed(() => items.value.filter((c) => c.enabled).length)
const fields = computed<Field[]>(() => schema.value[form.type] ?? [])
const plainFields = computed(() => fields.value.filter((f) => f.scope !== 'secret'))
const inlineFields = computed(() => plainFields.value.filter((f) => f.kind !== 'textarea'))
const textareaFields = computed(() => plainFields.value.filter((f) => f.kind === 'textarea'))
const secretFields = computed(() => fields.value.filter((f) => f.scope === 'secret'))
const typeChanged = computed(() => editing.value !== null && editing.value.type !== form.type)

const stateLabel: Record<Channel['secret_state'], string> = {
  ok: '已配置',
  missing: '缺少密钥',
  error: '无法解密',
  none: '无需密钥',
}

async function load(): Promise<void> {
  err.value = ''
  try {
    const out = await api.get<{ channels: Channel[] }>('/api/channels')
    items.value = out.channels
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function loadSchema(): Promise<void> {
  try {
    const out = await api.get<{ types: string[]; fields: Record<string, Field[]> }>('/api/channel-fields')
    types.value = out.types
    schema.value = out.fields
    reset()
    await refreshPreview()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function refreshPreview(): Promise<void> {
  const seq = ++previewSeq
  previewLoading.value = true
  try {
    const out = await api.post<ChannelPreview>('/api/channels/preview', {
      type: form.type,
      kind: previewKind.value,
      fields: { ...values.value },
    })
    if (seq !== previewSeq) return
    preview.value = out
    previewErr.value = ''
  } catch (e) {
    if (seq !== previewSeq) return
    preview.value = null
    previewErr.value = errorMessage(e)
  } finally {
    if (seq === previewSeq) previewLoading.value = false
  }
}

function schedulePreview(): void {
  if (previewTimer !== null) clearTimeout(previewTimer)
  previewTimer = setTimeout(() => {
    previewTimer = null
    void refreshPreview()
  }, 250)
}

// JSON.stringify turns the values map into a primitive so any field edit (not
// just a replacement of the whole object) refreshes the sample after a debounce.
watch(
  () => [form.type, form.name, previewKind.value, JSON.stringify(values.value)].join('\u0000'),
  () => schedulePreview(),
)
onUnmounted(() => {
  if (previewTimer !== null) clearTimeout(previewTimer)
})

onMounted(async () => {
  // The account is loaded before the router lets this page render, so the saved
  // value is already available; the fallback only guards a direct reload race.
  mode.value = user.value?.notify_mode ?? 'digest'
  await loadSchema()
  await load()
})

async function saveMode(): Promise<void> {
  modeErr.value = ''
  modeOk.value = ''
  modeBusy.value = true
  try {
    mode.value = await saveNotifyMode(mode.value)
    modeOk.value = '已保存，之后每一轮抓取都按这个形式生成通知。'
  } catch (e) {
    modeErr.value = errorMessage(e)
  } finally {
    modeBusy.value = false
  }
}

function defaultsFor(type: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const field of schema.value[type] ?? []) {
    if (field.scope !== 'secret') out[field.key] = field.kind === 'bool' ? 'false' : ''
  }
  return out
}

function reset(): void {
  editing.value = null
  originalType.value = ''
  form.type = types.value[0] ?? 'email'
  form.name = ''
  values.value = defaultsFor(form.type)
  secrets.value = {}
  clearSecrets.value = []
}

function onTypeChange(next: string): void {
  form.type = next
  values.value = defaultsFor(next)
  secrets.value = {}
  clearSecrets.value = []
}

function fieldValue(ch: Channel, field: Field): string {
  if (field.scope === 'spec') {
    const raw = (ch.config as Record<string, unknown>)[field.key]
    if (Array.isArray(raw)) return raw.join(', ')
    return typeof raw === 'string' ? raw : ''
  }
  return ch.config.options?.[field.key] ?? ''
}

function startEdit(ch: Channel): void {
  editing.value = ch
  originalType.value = ch.type
  form.type = ch.type
  form.name = ch.name
  const next: Record<string, string> = {}
  for (const field of schema.value[ch.type] ?? []) {
    if (field.scope === 'secret') continue
    next[field.key] = fieldValue(ch, field)
  }
  values.value = next
  secrets.value = {}
  clearSecrets.value = []
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

function isSet(key: string): boolean {
  if (!editing.value || typeChanged.value) return false
  return editing.value.secret_fields.includes(key)
}

function secretPlaceholder(field: Field): string {
  if (isSet(field.key)) return '已保存（留空则保持不变）'
  return field.placeholder || (field.required ? '必填' : '选填')
}

function toggleClear(key: string): void {
  const idx = clearSecrets.value.indexOf(key)
  if (idx >= 0) clearSecrets.value.splice(idx, 1)
  else {
    clearSecrets.value.push(key)
    secrets.value[key] = ''
  }
}

function setBool(key: string, event: Event): void {
  const target = event.target as HTMLInputElement
  values.value[key] = target.checked ? 'true' : 'false'
}

async function submit(): Promise<void> {
  err.value = ''
  success.value = ''
  busy.value = true
  try {
    const payload: Record<string, unknown> = {
      type: form.type,
      name: form.name.trim(),
      fields: values.value,
      // Empty inputs mean "keep what is stored"; only non-empty values are sent.
      secrets: Object.fromEntries(Object.entries(secrets.value).filter(([, v]) => v !== '')),
      clear_secrets: clearSecrets.value,
    }
    if (editing.value) {
      await api.patch(`/api/channels/${editing.value.id}`, payload)
      success.value = '通知方式已更新'
    } else {
      await api.post('/api/channels', payload)
      success.value = '通知方式已创建'
    }
    reset()
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

async function toggleEnabled(ch: Channel): Promise<void> {
  err.value = ''
  success.value = ''
  try {
    await api.patch(`/api/channels/${ch.id}`, { enabled: !ch.enabled })
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function remove(ch: Channel): Promise<void> {
  if (!window.confirm(`确定删除通知方式「${ch.name || ch.type} #${ch.id}」？`)) return
  err.value = ''
  success.value = ''
  try {
    await api.del(`/api/channels/${ch.id}`)
    if (editing.value?.id === ch.id) reset()
    success.value = '通知方式已删除'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

function summary(ch: Channel): string[] {
  const out: string[] = []
  if (ch.config.from) out.push(`from: ${ch.config.from}`)
  if (ch.config.to?.length) out.push(`to: ${ch.config.to.join(', ')}`)
  if (ch.config.cc?.length) out.push(`cc: ${ch.config.cc.join(', ')}`)
  for (const [key, value] of Object.entries(ch.config.options ?? {})) {
    if (value) out.push(`${key}: ${value}`)
  }
  if (ch.config.subject_prefix) out.push(`prefix: ${ch.config.subject_prefix}`)
  return out
}
</script>

<template>
  <PageHeader title="通知方式" description="通知只发给你自己：每个渠道使用你自己的凭据独立请求。凭据加密保存在本机，页面不会回显明文。">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="card">
    <h2>通知形式</h2>
    <p class="card-sub">
      账号级设置，对下面所有通知方式生效，不跟随单条订阅。一轮抓取里出现多个新版本时，是合成一条发出，还是每个项目各自一条。
    </p>
    <div class="choice-row">
      <label class="inline-check">
        <input v-model="mode" type="radio" value="digest" />
        <span>汇总一条<span class="muted">：多个项目合并在同一封里，邮件数量少</span></span>
      </label>
      <label class="inline-check">
        <input v-model="mode" type="radio" value="per_feed" />
        <span>每个项目一条<span class="muted">：便于按项目归档或转发</span></span>
      </label>
    </div>
    <div class="form-actions">
      <button class="primary" type="button" :disabled="modeBusy" @click="saveMode">保存</button>
    </div>
    <Notice kind="error" :message="modeErr" />
    <Notice kind="success" :message="modeOk" />
  </div>

  <div class="card">
    <h2>{{ editing === null ? '新增通知方式' : `编辑通知方式 #${editing.id}` }}</h2>
    <p class="card-sub">
      选择渠道后按提示填写即可。密钥类字段保存后只会显示「已配置」，再次编辑时留空表示沿用原值。
    </p>
    <form @submit.prevent="submit">
      <div class="form-row">
        <label>类型
          <select :value="form.type" @change="onTypeChange(($event.target as HTMLSelectElement).value)">
            <option v-for="t in types" :key="t" :value="t">{{ t }}</option>
          </select>
        </label>
        <label>名称<input v-model="form.name" placeholder="选填，便于自己区分" /></label>
      </div>

      <div class="form-row">
        <template v-for="field in inlineFields" :key="field.key">
          <label v-if="field.kind === 'select'">{{ field.label }}
            <select v-model="values[field.key]">
              <option value="">默认</option>
              <option v-for="choice in field.choices ?? []" :key="choice" :value="choice">{{ choice }}</option>
            </select>
          </label>
          <label v-else-if="field.kind === 'bool'" class="check-line">
            <input type="checkbox" :checked="values[field.key] === 'true'" @change="setBool(field.key, $event)" />
            {{ field.label }}
          </label>
          <label v-else>{{ field.label }}
            <input
              v-model="values[field.key]"
              :placeholder="field.placeholder || (field.required ? '必填' : '选填')"
            />
          </label>
        </template>
      </div>

      <label v-for="field in textareaFields" :key="field.key">{{ field.label }}
        <textarea v-model="values[field.key]" spellcheck="false" :placeholder="field.placeholder || ''"></textarea>
        <span v-if="field.help" class="hint">{{ field.help }}</span>
      </label>

      <template v-if="secretFields.length">
        <h3 class="section-title">凭据</h3>
        <div class="form-row">
          <div v-for="field in secretFields" :key="field.key" class="secret-field">
            <label>{{ field.label }}
              <input
                v-model="secrets[field.key]"
                type="password"
                autocomplete="new-password"
                :disabled="clearSecrets.includes(field.key)"
                :placeholder="secretPlaceholder(field)"
              />
            </label>
            <p class="hint">
              <template v-if="isSet(field.key) && !clearSecrets.includes(field.key)">
                已保存<span v-if="editing?.secret_hints?.[field.key]">（{{ editing?.secret_hints?.[field.key] }}）</span>
                · <button class="link" type="button" @click="toggleClear(field.key)">清除</button>
              </template>
              <template v-else-if="clearSecrets.includes(field.key)">
                保存后将清除 ·
                <button class="link" type="button" @click="toggleClear(field.key)">撤销</button>
              </template>
              <template v-else>{{ field.help || '保存后不再显示明文' }}</template>
            </p>
          </div>
        </div>
        <p class="hint">凭据用主密钥加密后写入本机数据库；管理员和其他用户都看不到，也不会出现在任何页面或日志里。</p>
      </template>

      <div class="form-actions">
        <button class="primary" type="submit" :disabled="busy">{{ editing === null ? '添加' : '保存修改' }}</button>
        <button v-if="editing !== null" class="ghost" type="button" @click="reset">取消编辑</button>
      </div>
    </form>
  </div>

  <div class="card">
    <h2>通知内容预览</h2>
    <p class="card-sub">
      用示例订阅 golang/go（go1.27.4）按上方表单参数实时渲染，不会发送任何消息。名称只用于自己区分，不出现在通知内容里；凭据与目标地址不参与预览。
    </p>
    <div class="preview-toolbar">
      <div class="choice-row" style="margin: 0">
        <label class="inline-check">
          <input v-model="previewKind" type="radio" value="new_releases" />
          <span>发现新版本</span>
        </label>
        <label class="inline-check">
          <input v-model="previewKind" type="radio" value="baseline" />
          <span>当前版本基线</span>
        </label>
      </div>
      <span class="muted preview-kind">
        {{ form.type }} ·
        {{ preview === null ? '—' : preview.format === 'html' ? 'HTML' : preview.format === 'markdown' ? 'Markdown' : '纯文本' }}
      </span>
    </div>
    <Notice kind="error" :message="previewErr" />
    <div v-if="previewLoading" class="muted preview-loading">正在生成预览…</div>
    <template v-if="preview">
      <h3 class="section-title">标题</h3>
      <div class="preview-subject">{{ preview.subject }}</div>
      <h3 class="section-title">正文</h3>
      <div v-if="preview.format === 'html'" class="preview-body preview-html" v-html="preview.body"></div>
      <pre v-else class="preview-body">{{ preview.body }}</pre>
      <details class="preview-details">
        <summary>实际发送内容（已隐藏目标地址与凭据）</summary>
        <pre class="preview-payload">{{ preview.payload }}</pre>
      </details>
    </template>
  </div>

  <div class="card">
    <h2>已配置的通知方式</h2>
    <p class="card-sub">密钥只显示「已配置」以及主机名提示，明文永不回显。</p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>ID</th><th>类型</th><th>名称</th><th>配置</th><th>凭据</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="ch in items" :key="ch.id">
            <td class="mono">{{ ch.id }}</td>
            <td><span class="badge">{{ ch.type }}</span></td>
            <td>{{ ch.name || '—' }}</td>
            <td class="mono cell-config">
              <div v-for="line in summary(ch)" :key="line">{{ line }}</div>
              <div v-if="!summary(ch).length" class="muted">—</div>
            </td>
            <td>
              <div v-if="ch.secret_state === 'none'" class="muted">—</div>
              <div v-else class="secret-tags">
                <span v-for="key in ch.secret_fields" :key="key" class="tag">{{ key }} ✓</span>
                <span v-if="!ch.secret_fields.length" class="tag">未设置</span>
                <div v-if="ch.secret_hints && Object.keys(ch.secret_hints).length" class="hint">
                  <div v-for="(hint, key) in ch.secret_hints" :key="key">{{ key }}: {{ hint }}</div>
                </div>
              </div>
            </td>
            <td>
              <span class="badge" :class="ch.enabled ? 'badge-on' : 'badge-off'">{{ ch.enabled ? '启用' : '停用' }}</span>
              <div v-if="ch.secret_state === 'missing'" class="badge badge-warn" style="margin-top: .3rem">缺少密钥</div>
              <div v-else-if="ch.secret_state === 'error'" class="badge badge-danger" style="margin-top: .3rem">无法解密</div>
            </td>
            <td>
              <div class="cell-actions">
                <button class="small" type="button" @click="startEdit(ch)">编辑</button>
                <button class="small" type="button" :disabled="ch.enabled && enabledCount <= 1" :title="ch.enabled && enabledCount <= 1 ? '请至少保留一个启用的通知渠道' : ''" @click="toggleEnabled(ch)">{{ ch.enabled ? '停用' : '启用' }}</button>
                <button class="small" type="button" :title="ch.enabled && enabledCount <= 1 ? '仍有订阅时无法删除最后一个启用的通知渠道' : ''" @click="remove(ch)">删除</button>
              </div>
            </td>
          </tr>
          <tr v-if="!items.length"><td colspan="7" class="empty">还没有通知方式。先添加一个，之后的更新会推送到这里。</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

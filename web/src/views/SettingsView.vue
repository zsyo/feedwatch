<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, errorMessage } from '../api'
import { changePassword, isAdmin, saveTimezone, user } from '../session'
import { previewAt, serverTimeZone, supportedZones, timeZone, zoneLabel } from '../time'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

const route = useRoute()
const router = useRouter()
const oldPassword = ref('')
const newPassword = ref('')
const confirm = ref('')
const err = ref('')
const busy = ref(false)
const meta = ref({ version: '', build_id: '', mode: '' })

// Timezone: the select drafts a value and only the save button persists it, so
// a stray arrow-key cannot silently change what every list on the site shows.
const tzDraft = ref('')
const tzBusy = ref(false)
const tzErr = ref('')
const tzOk = ref('')

const zones = computed(() => supportedZones(tzDraft.value))
const serverLabel = computed(() => zoneLabel(serverTimeZone.value || 'Local'))
const effectiveLabel = computed(() => zoneLabel(timeZone.value || serverTimeZone.value || 'Local'))
const preview = computed(() => previewAt(tzDraft.value))

interface Identity {
  provider: string
  login: string
  email: string
  created_at: string
}
interface ProviderView {
  key: string
  name: string
  available: boolean
  bound: boolean
}
interface IdentityResponse {
  password_set: boolean
  identities: Identity[]
  providers: ProviderView[]
}

// Administrators sign in with a password only, so the whole third-party block
// is absent for them — the same reason subscriptions and channels are.
const showIdentities = computed(() => !isAdmin.value)
const passwordSet = ref(user.value?.password_set ?? true)
const identities = ref<Identity[]>([])
const providers = ref<ProviderView[]>([])
const idErr = ref('')
const idOk = ref('')
const idBusy = ref('')

const bindErrors: Record<string, string> = {
  not_configured: '该登录方式还没有配置完成，请联系管理员。',
  denied: '你取消了授权。',
  state_invalid: '授权请求已失效，请重新发起绑定。',
  exchange_failed: '无法向第三方平台确认身份，请稍后重试。',
  already_linked: '这个第三方账号已经绑定了本站的另一个账号。',
  already_linked_other: '你的账号已经绑定过同一平台的另一个账号，请先解绑。',
  account_failed: '保存绑定失败，请重试。',
  admin_not_allowed: '管理员账号不支持第三方登录。',
}

onMounted(async () => {
  tzDraft.value = user.value?.timezone ?? ''
  try {
    meta.value = await api.get<{ version: string; build_id: string; mode: string }>('/api/meta')
  } catch {
    // cosmetic
  }
  // Load the bindings first: the banner left by the OAuth callback names the
  // provider, which comes from this response.
  if (showIdentities.value) await loadIdentities()
  handleBindResult()
})

// handleBindResult reads the ?bind= flag the OAuth callback leaves behind, then
// strips it so a refresh does not replay the banner.
function handleBindResult(): void {
  const ok = typeof route.query.bind === 'string' ? route.query.bind : ''
  const failed = typeof route.query.bind_error === 'string' ? route.query.bind_error : ''
  if (!ok && !failed) return
  if (ok === 'ok') {
    const key = typeof route.query.provider === 'string' ? route.query.provider : ''
    const label = providers.value.find((p) => p.key === key)?.name ?? key
    idOk.value = label ? `已绑定 ${label} 账号。` : '已绑定第三方账号。'
  } else {
    idErr.value = bindErrors[failed] ?? '绑定未完成，请重试。'
  }
  void router.replace({ path: '/settings' })
}

async function loadIdentities(): Promise<void> {
  try {
    const out = await api.get<IdentityResponse>('/api/me/identities')
    identities.value = out.identities
    providers.value = out.providers
    passwordSet.value = out.password_set
  } catch (e) {
    idErr.value = errorMessage(e)
  }
}

function identityOf(key: string): Identity | undefined {
  return identities.value.find((i) => i.provider === key)
}

// bind navigates the whole page: the provider needs a top-level redirect, and
// the session cookie is same-site here, so mode=bind is accepted.
function bind(key: string): void {
  window.location.href = `/api/auth/oauth/${key}/start?mode=bind`
}

async function unbind(key: string, name: string): Promise<void> {
  if (!window.confirm(`解绑 ${name} 后，就不能再用它登录这个账号了。继续？`)) return
  idErr.value = ''
  idOk.value = ''
  idBusy.value = key
  try {
    await api.del(`/api/me/identities/${key}`)
    idOk.value = `已解绑 ${name}。`
    await loadIdentities()
  } catch (e) {
    idErr.value = errorMessage(e)
  } finally {
    idBusy.value = ''
  }
}

async function saveTz(): Promise<void> {
  tzErr.value = ''
  tzOk.value = ''
  tzBusy.value = true
  try {
    tzDraft.value = await saveTimezone(tzDraft.value)
    tzOk.value = '已保存，所有页面按新时区显示。'
  } catch (e) {
    tzErr.value = errorMessage(e)
  } finally {
    tzBusy.value = false
  }
}

async function submit(): Promise<void> {
  err.value = ''
  if (newPassword.value.length < 8) {
    err.value = '新密码至少需要 8 个字符'
    return
  }
  if (newPassword.value !== confirm.value) {
    err.value = '两次输入的新密码不一致'
    return
  }
  busy.value = true
  try {
    await changePassword(passwordSet.value ? oldPassword.value : '', newPassword.value)
    await router.push('/login')
  } catch (e) {
    err.value = errorMessage(e)
    busy.value = false
  }
}
</script>

<template>
  <PageHeader title="账号设置" description="查看账号信息、选择页面显示时区并修改密码。" />

  <div class="grid-2">
    <div class="card">
      <h2>账号信息</h2>
      <p class="card-sub">会话通过 HttpOnly Cookie 维持，服务端只保存令牌哈希。</p>
      <table>
        <tbody>
          <tr><th>用户名</th><td>{{ user?.username }}</td></tr>
          <tr><th>角色</th><td>{{ user?.role === 'admin' ? '管理员' : '普通用户' }}</td></tr>
          <tr><th>用户 ID</th><td class="mono">{{ user?.id }}</td></tr>
          <tr><th>登录密码</th><td>{{ passwordSet ? '已设置' : '未设置（仅第三方登录）' }}</td></tr>
          <tr><th>显示时区</th><td class="mono">{{ effectiveLabel }}</td></tr>
          <tr><th>运行模式</th><td class="mono">{{ meta.mode || 'webui' }}</td></tr>
          <tr><th>版本 / 构建</th><td class="mono">{{ meta.version || 'dev' }} / {{ meta.build_id || 'dev' }}</td></tr>
        </tbody>
      </table>
    </div>

    <div class="card">
      <h2>{{ passwordSet ? '修改密码' : '设置登录密码' }}</h2>
      <p class="card-sub">
        {{ passwordSet
          ? '修改后所有会话立即失效，需要重新登录。'
          : '这个账号是通过第三方登录创建的，还没有密码。设置后既能用密码登录，也能解绑第三方账号。' }}
      </p>
      <form @submit.prevent="submit">
        <label v-if="passwordSet">当前密码
          <input v-model="oldPassword" type="password" autocomplete="current-password" required />
        </label>
        <label>{{ passwordSet ? '新密码' : '密码' }}
          <input v-model="newPassword" type="password" autocomplete="new-password" required minlength="8" />
        </label>
        <label>确认{{ passwordSet ? '新' : '' }}密码
          <input v-model="confirm" type="password" autocomplete="new-password" required minlength="8" />
        </label>
        <Notice kind="error" :message="err" />
        <button class="primary" type="submit" :disabled="busy">{{ busy ? '提交中…' : '保存密码' }}</button>
      </form>
    </div>
  </div>

  <div v-if="showIdentities" class="card">
    <h2>第三方登录</h2>
    <p class="card-sub">
      绑定后可以不用密码直接登录这个账号。同一个平台只能绑定一个账号；
      解绑前请确认还有别的登录方式，否则账号会进不去。
    </p>
    <Notice kind="error" :message="idErr" />
    <Notice kind="success" :message="idOk" />
    <div v-for="p in providers" :key="p.key" class="provider-row">
      <div>
        <div class="provider-name">{{ p.name }}</div>
        <div v-if="identityOf(p.key)" class="provider-meta">
          已绑定 <span class="mono">{{ identityOf(p.key)?.login || '未知账号' }}</span>
          <template v-if="identityOf(p.key)?.email"> · {{ identityOf(p.key)?.email }}</template>
        </div>
        <div v-else-if="!p.available" class="provider-meta">管理员尚未配置这种登录方式。</div>
        <div v-else class="provider-meta">未绑定</div>
      </div>
      <div class="provider-actions">
        <button v-if="p.bound" class="danger" type="button" :disabled="idBusy === p.key"
          @click="unbind(p.key, p.name)">{{ idBusy === p.key ? '解绑中…' : '解绑' }}</button>
        <a v-else-if="p.available" class="link-button" :href="`/api/auth/oauth/${p.key}/start?mode=bind`">绑定</a>
        <span v-else class="tag">未配置</span>
      </div>
    </div>
    <p v-if="!passwordSet" class="hint">
      当前账号没有密码：请先在上方设置密码，再把这里唯一的第三方账号解绑。
    </p>
  </div>

  <div class="card">
    <h2>显示时区</h2>
    <p class="card-sub">
      只影响页面里时间的展示，数据库里仍统一存 UTC；设置跟着账号走，换设备登录也会同步。
    </p>
    <form @submit.prevent="saveTz">
      <label>
        时区
        <select v-model="tzDraft">
          <option value="">跟随服务器（{{ serverLabel }}）</option>
          <option v-for="z in zones" :key="z" :value="z">{{ z }}</option>
        </select>
      </label>
      <p class="card-sub">
        按此设置显示的当前时间：<span class="mono">{{ preview }}</span>
      </p>
      <Notice kind="error" :message="tzErr" />
      <Notice kind="success" :message="tzOk" />
      <button class="primary" type="submit" :disabled="tzBusy">{{ tzBusy ? '保存中…' : '保存时区' }}</button>
    </form>
  </div>

  <div class="card">
    <h2>安全说明</h2>
    <ul class="muted" style="font-size: .86rem; line-height: 1.8; padding-left: 1.2rem; margin: 0">
      <li>通知方式里的凭据用主密钥 AES-256-GCM 加密后保存在本机数据库，页面只显示「已配置 / 未设置」，任何页面都不会回显明文。</li>
      <li>每个用户使用自己的凭据独立发送，不做跨用户合并；每种渠道类型各自限速。</li>
      <li>管理员可以停用某个用户的通知，但看不到也改不了它的凭据。</li>
      <li>站点级参数由管理员在后台维护，修改后重启进程生效。</li>
    </ul>
  </div>
</template>

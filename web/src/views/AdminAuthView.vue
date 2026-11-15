<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { api, errorMessage } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface ProviderRow {
  key: string
  name: string
  enabled: boolean
  client_id: string
  secret_set: boolean
  available: boolean
  callback_url: string
  scope: string
  pkce: boolean
}
interface AuthResponse {
  registration_enabled: boolean
  providers: ProviderRow[]
}

interface Draft {
  enabled: boolean
  client_id: string
  client_secret: string
}

const data = ref<AuthResponse | null>(null)
const drafts = reactive<Record<string, Draft>>({})
const registration = ref(false)
const err = ref('')
const ok = ref('')
const busy = ref(false)
const copied = ref('')

const providers = computed(() => data.value?.providers ?? [])

function apply(out: AuthResponse): void {
  data.value = out
  registration.value = out.registration_enabled
  for (const p of out.providers) {
    drafts[p.key] = { enabled: p.enabled, client_id: p.client_id, client_secret: '' }
  }
}

async function load(): Promise<void> {
  err.value = ''
  try {
    apply(await api.get<AuthResponse>('/api/admin/auth'))
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

async function save(): Promise<void> {
  err.value = ''
  ok.value = ''
  busy.value = true
  try {
    // Only changed fields are sent. An empty client_secret means "keep the
    // stored one", which is why the form never receives the current value.
    const patch: Record<string, unknown> = {}
    for (const p of providers.value) {
      const draft = drafts[p.key]
      if (!draft) continue
      const change: Record<string, unknown> = {}
      if (draft.enabled !== p.enabled) change.enabled = draft.enabled
      if (draft.client_id.trim() !== p.client_id) change.client_id = draft.client_id.trim()
      if (draft.client_secret) change.client_secret = draft.client_secret
      if (Object.keys(change).length) patch[p.key] = change
    }
    const body: Record<string, unknown> = {}
    if (registration.value !== data.value?.registration_enabled) body.registration_enabled = registration.value
    if (Object.keys(patch).length) body.providers = patch
    if (!Object.keys(body).length) {
      ok.value = '没有待保存的修改'
      return
    }
    apply(await api.put<AuthResponse>('/api/admin/auth', body))
    ok.value = '已保存，立即生效。'
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

async function clearCredentials(p: ProviderRow): Promise<void> {
  if (!window.confirm(`清除 ${p.name} 的 Client ID 与 Secret？登录页会立刻去掉这个入口，已绑定的用户可以继续用密码登录。`)) return
  err.value = ''
  ok.value = ''
  try {
    apply(await api.del<AuthResponse>(`/api/admin/auth/${p.key}`))
    ok.value = `${p.name} 的凭据已清除。`
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function copy(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = text
    window.setTimeout(() => {
      if (copied.value === text) copied.value = ''
    }, 2000)
  } catch {
    err.value = '复制失败，请手动选中复制。'
  }
}
</script>

<template>
  <PageHeader title="登录与注册" description="自助注册开关，以及 GitHub / linux.do 的 OAuth 应用凭据。保存后立即生效，不需要重启。">
    <button class="ghost" type="button" @click="load">重新加载</button>
  </PageHeader>

  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="ok" />

  <div class="card">
    <h2>自助注册</h2>
    <p class="card-sub">
      打开后登录页出现「注册」入口，任何人都能创建普通用户账号；第三方登录遇到陌生账号时也会直接建号。
      关闭时两者都不生效，已有账号不受影响。
    </p>
    <label class="inline-check">
      <input v-model="registration" type="checkbox" />
      允许访客自助注册（含第三方登录自动建号）
    </label>
    <p class="hint">
      关闭状态适合只给熟人使用的私有部署。管理员账号始终只能用密码登录，与这里的设置无关。
    </p>
  </div>

  <div v-for="p in providers" :key="p.key" class="card">
    <h2>{{ p.name }}</h2>
    <p class="card-sub">
      <template v-if="p.available">已启用，登录页会显示「使用 {{ p.name }} 登录」。</template>
      <template v-else-if="p.enabled">已勾选启用，但凭据不完整，登录页还不会显示入口。</template>
      <template v-else>未启用。</template>
    </p>

    <label class="inline-check">
      <input v-model="drafts[p.key].enabled" type="checkbox" :disabled="!drafts[p.key]" />
      在登录页启用 {{ p.name }}
    </label>

    <div class="form-row">
      <label>Client ID
        <input v-model="drafts[p.key].client_id" spellcheck="false" autocomplete="off" />
      </label>
      <label>Client Secret
        <input v-model="drafts[p.key].client_secret" type="password" autocomplete="new-password"
          :placeholder="p.secret_set ? '已保存，留空表示不修改' : '未设置'" spellcheck="false" />
      </label>
    </div>

    <label>回调地址（填到 {{ p.name }} 的应用设置里）
      <span class="copy-line">
        <input class="mono" :value="p.callback_url" readonly @focus="($event.target as HTMLInputElement).select()" />
        <button class="small" type="button" @click="copy(p.callback_url)">{{ copied === p.callback_url ? '已复制' : '复制' }}</button>
      </span>
    </label>
    <p class="hint">
      必须与平台后台登记的回调地址完全一致（协议、域名、路径都要一样）。经反向代理暴露时，
      这里显示的是当前浏览器访问的地址；代理需要传递 <span class="mono">X-Forwarded-Proto</span> 与
      <span class="mono">X-Forwarded-Host</span>，否则会算成 http 或内网地址。
    </p>
    <p class="hint">
      申请的权限：<span class="mono">{{ p.scope }}</span>
      <template v-if="p.pkce"> · 使用 PKCE（S256）</template>
      <template v-else> · 该平台未提供 PKCE，仅用 state 防伪</template>
    </p>

    <div class="form-actions" style="margin-top: .9rem">
      <button class="danger" type="button" @click="clearCredentials(p)"
        :disabled="!p.secret_set && !p.client_id">清除凭据</button>
    </div>
  </div>

  <div class="form-actions">
    <button class="primary" type="button" :disabled="busy" @click="save">{{ busy ? '保存中…' : '保存修改' }}</button>
  </div>

  <div class="card" style="margin-top: 1.2rem">
    <h2>说明</h2>
    <ul class="muted" style="font-size: .86rem; line-height: 1.8; padding-left: 1.2rem; margin: 0">
      <li>Client Secret 用主密钥加密后存库，保存后任何接口都不再回显；主密钥丢失时凭据解不开，需要在两个平台重新填一次。</li>
      <li>GitHub：Settings → Developer settings → OAuth Apps → New OAuth App，主页随意，回调地址填上面的值。</li>
      <li>linux.do：在 connect.linux.do 的「我的应用接入」申请，回调地址填上面的值。</li>
      <li>第三方登录只创建普通用户；管理员账号不参与绑定，也不能用第三方账号登录。</li>
      <li>用户在「账号设置 → 第三方登录」里自行绑定或解绑；没有密码的账号必须先设置密码才能解绑最后一个第三方账号。</li>
    </ul>
  </div>
</template>

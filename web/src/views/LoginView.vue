<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { errorMessage } from '../api'
import { login, mustChangePassword } from '../session'
import { authMeta, loadAuthMeta, oauthProviderLabels } from '../authMeta'
import Notice from '../components/Notice.vue'

const route = useRoute()
const router = useRouter()
const username = ref('')
const password = ref('')
const err = ref('')
const busy = ref(false)

// Why the redirect back from a provider ended on this page instead of the
// dashboard. The codes come from the server's oauthFail helper.
const oauthErrors: Record<string, string> = {
  not_configured: '该登录方式还没有配置完成，请联系管理员。',
  denied: '你取消了授权，没有完成登录。',
  state_invalid: '授权请求已失效，请重新发起登录。',
  exchange_failed: '无法向第三方平台确认身份，请稍后重试。',
  registration_closed: '这个第三方账号还没有关联本站账号，且本站未开放自助注册。',
  user_disabled: '该账号已被停用，请联系管理员。',
  admin_not_allowed: '管理员账号只能使用用户名和密码登录。',
  account_failed: '创建或读取账号失败，请联系管理员。',
  session_failed: '登录会话创建失败，请重试。',
}

const storedError = computed(() => {
  const code = typeof route.query.oauth_error === 'string' ? route.query.oauth_error : ''
  if (!code) return ''
  return oauthErrors[code] ?? '第三方登录未完成，请重试。'
})

const providers = computed(() =>
  Object.keys(oauthProviderLabels)
    .filter((key) => authMeta.value.oauth[key])
    .map((key) => ({ key, label: oauthProviderLabels[key] })),
)
const canRegister = computed(() => authMeta.value.registration_enabled)

// The redirect target has to survive the round trip through the provider, so it
// travels in the start URL rather than in component state.
const startQuery = computed(() => {
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : ''
  return redirect ? `?redirect=${encodeURIComponent(redirect)}` : ''
})

onMounted(() => {
  void loadAuthMeta()
})

async function submit(): Promise<void> {
  err.value = ''
  busy.value = true
  try {
    await login(username.value, password.value)
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    await router.push(mustChangePassword.value ? '/password' : redirect)
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-card">
    <h1>FeedWatch</h1>
    <p>登录以管理订阅与通知方式</p>
    <Notice kind="error" :message="storedError" />
    <form @submit.prevent="submit">
      <label>用户名
        <input v-model="username" autocomplete="username" required autofocus />
      </label>
      <label>密码
        <input v-model="password" type="password" autocomplete="current-password" required />
      </label>
      <Notice kind="error" :message="err" />
      <button class="primary" type="submit" :disabled="busy">{{ busy ? '登录中…' : '登录' }}</button>
    </form>

    <template v-if="providers.length">
      <p class="auth-divider"><span>或使用第三方账号</span></p>
      <div class="oauth-buttons">
        <a v-for="p in providers" :key="p.key" class="oauth-btn"
          :href="`/api/auth/oauth/${p.key}/start${startQuery}`">
          使用 {{ p.label }} 登录
        </a>
      </div>
    </template>

    <p v-if="canRegister" class="auth-switch">
      还没有账号？<RouterLink class="link" to="/register">注册一个</RouterLink>
    </p>
    <p class="hint">首次启动生成的随机管理员密码只在服务日志中打印一次，登录后必须立即修改。</p>
  </div>
</template>

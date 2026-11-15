<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { errorMessage } from '../api'
import { register } from '../session'
import { authMeta, loadAuthMeta } from '../authMeta'
import Notice from '../components/Notice.vue'

const router = useRouter()
const username = ref('')
const password = ref('')
const confirm = ref('')
const err = ref('')
const busy = ref(false)

const canRegister = computed(() => authMeta.value.registration_enabled)

onMounted(() => {
  void loadAuthMeta()
})

async function submit(): Promise<void> {
  err.value = ''
  if (password.value.length < 8) {
    err.value = '密码至少需要 8 个字符'
    return
  }
  if (password.value !== confirm.value) {
    err.value = '两次输入的密码不一致'
    return
  }
  busy.value = true
  try {
    await register(username.value, password.value)
    // Registration signs the account in, so there is no detour through /login.
    await router.push('/')
  } catch (e) {
    err.value = errorMessage(e)
    busy.value = false
  }
}
</script>

<template>
  <div class="login-card">
    <h1>注册账号</h1>
    <p v-if="canRegister">创建后即可订阅项目、配置通知方式。</p>
    <p v-else>本站未开放自助注册。</p>

    <template v-if="canRegister">
      <form @submit.prevent="submit">
        <label>用户名
          <input v-model="username" autocomplete="username" required autofocus minlength="2" maxlength="32" />
        </label>
        <label>密码
          <input v-model="password" type="password" autocomplete="new-password" required minlength="8" />
        </label>
        <label>确认密码
          <input v-model="confirm" type="password" autocomplete="new-password" required minlength="8" />
        </label>
        <Notice kind="error" :message="err" />
        <button class="primary" type="submit" :disabled="busy">{{ busy ? '创建中…' : '创建账号' }}</button>
      </form>
      <p class="hint">用户名 2–32 个字符，不能包含空格；密码至少 8 位。管理员可以在「登录与注册」页关闭注册。</p>
    </template>
    <Notice v-else kind="info" message="需要账号请联系站点管理员开通。" />

    <p class="auth-switch">
      已有账号？<RouterLink class="link" to="/login">去登录</RouterLink>
    </p>
  </div>
</template>

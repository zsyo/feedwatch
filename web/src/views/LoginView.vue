<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { errorMessage } from '../api'
import { login, mustChangePassword } from '../session'
import Notice from '../components/Notice.vue'

const route = useRoute()
const router = useRouter()
const username = ref('')
const password = ref('')
const err = ref('')
const busy = ref(false)

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
    <p class="hint">首次启动生成的随机管理员密码只在服务日志中打印一次，登录后必须立即修改。</p>
  </div>
</template>

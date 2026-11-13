<script setup lang="ts">
import { ref } from 'vue'
import { RouterLink } from 'vue-router'
import { errorMessage } from '../api'
import { changePassword } from '../session'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

const oldPassword = ref('')
const newPassword = ref('')
const confirm = ref('')
const err = ref('')
const done = ref(false)
const busy = ref(false)

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
    await changePassword(oldPassword.value, newPassword.value)
    done.value = true
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageHeader title="修改密码" description="首次登录或管理员重置密码后，必须先设置新密码才能使用其它功能。" />
  <div class="card" style="max-width: 480px">
    <div v-if="done">
      <Notice kind="success" message="密码已修改，所有旧会话已失效，请重新登录。" />
      <RouterLink to="/login" class="primary" style="display: inline-block; padding: .5rem .9rem; border-radius: 7px; background: var(--accent); color: #fff">前往登录</RouterLink>
    </div>
    <form v-else @submit.prevent="submit">
      <label>当前密码
        <input v-model="oldPassword" type="password" autocomplete="current-password" required />
      </label>
      <label>新密码
        <input v-model="newPassword" type="password" autocomplete="new-password" required />
      </label>
      <label>确认新密码
        <input v-model="confirm" type="password" autocomplete="new-password" required />
      </label>
      <Notice kind="error" :message="err" />
      <button class="primary" type="submit" :disabled="busy">{{ busy ? '提交中…' : '保存新密码' }}</button>
      <p class="hint">修改成功后当前会话会立即失效，需要重新登录。</p>
    </form>
  </div>
</template>

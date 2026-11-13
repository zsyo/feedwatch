<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, errorMessage } from '../api'
import { changePassword, user } from '../session'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

const router = useRouter()
const oldPassword = ref('')
const newPassword = ref('')
const confirm = ref('')
const err = ref('')
const busy = ref(false)
const meta = ref({ version: '', build_id: '', mode: '' })

onMounted(async () => {
  try {
    meta.value = await api.get<{ version: string; build_id: string; mode: string }>('/api/meta')
  } catch {
    // cosmetic
  }
})

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
    await router.push('/login')
  } catch (e) {
    err.value = errorMessage(e)
    busy.value = false
  }
}
</script>

<template>
  <PageHeader title="账号设置" description="查看当前账号信息并修改密码。" />

  <div class="grid-2">
    <div class="card">
      <h2>账号信息</h2>
      <p class="card-sub">会话通过 HttpOnly Cookie 维持，服务端只保存令牌哈希。</p>
      <table>
        <tbody>
          <tr><th>用户名</th><td>{{ user?.username }}</td></tr>
          <tr><th>角色</th><td>{{ user?.role === 'admin' ? '管理员' : '普通用户' }}</td></tr>
          <tr><th>用户 ID</th><td class="mono">{{ user?.id }}</td></tr>
          <tr><th>运行模式</th><td class="mono">{{ meta.mode || 'webui' }}</td></tr>
          <tr><th>版本 / 构建</th><td class="mono">{{ meta.version || 'dev' }} / {{ meta.build_id || 'dev' }}</td></tr>
        </tbody>
      </table>
    </div>

    <div class="card">
      <h2>修改密码</h2>
      <p class="card-sub">修改后所有会话立即失效，需要重新登录。</p>
      <form @submit.prevent="submit">
        <label>当前密码<input v-model="oldPassword" type="password" autocomplete="current-password" required /></label>
        <label>新密码<input v-model="newPassword" type="password" autocomplete="new-password" required /></label>
        <label>确认新密码<input v-model="confirm" type="password" autocomplete="new-password" required /></label>
        <Notice kind="error" :message="err" />
        <button class="primary" type="submit" :disabled="busy">{{ busy ? '提交中…' : '保存新密码' }}</button>
      </form>
    </div>
  </div>

  <div class="card">
    <h2>安全说明</h2>
    <ul class="muted" style="font-size: .86rem; line-height: 1.8; padding-left: 1.2rem; margin: 0">
      <li>通知渠道的密钥不会写入数据库，只能以 <code class="mono">${ENV_VAR}</code> 占位符或环境变量名保存，发送时在内存中展开。</li>
      <li>每个用户的通知使用自己的密钥独立请求，不做跨用户合并；每种渠道类型各自限速。</li>
      <li>Cookie 的 Secure 开关与会话有效期由服务端环境变量 <code class="mono">FEEDWATCH_WEBUI_COOKIE_SECURE</code>、<code class="mono">FEEDWATCH_WEBUI_SESSION_TTL</code> 控制，避免误操作锁死登录。</li>
      <li>其它全局参数在「全局设置」页修改，重启进程后生效。</li>
    </ul>
  </div>
</template>

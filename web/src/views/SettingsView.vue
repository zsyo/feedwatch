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
      <li>通知方式里的凭据用主密钥 AES-256-GCM 加密后保存在本机数据库，页面只显示「已配置 / 未设置」，任何页面都不会回显明文。</li>
      <li>每个用户使用自己的凭据独立发送，不做跨用户合并；每种渠道类型各自限速。</li>
      <li>管理员可以停用某个用户的通知，但看不到也改不了它的凭据。</li>
      <li>站点级参数由管理员在后台维护，修改后重启进程生效。</li>
    </ul>
  </div>
</template>

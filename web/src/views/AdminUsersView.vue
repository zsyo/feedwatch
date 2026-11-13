<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, errorMessage } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface ManagedUser {
  id: number
  username: string
  role: 'admin' | 'user'
  is_active: boolean
  must_change_password: boolean
  created_at: string
}

const users = ref<ManagedUser[]>([])
const err = ref('')
const success = ref('')
const resetNote = ref('')
const resetPassword = ref('')
const form = ref({ username: '', password: '', role: 'user' })

async function load(): Promise<void> {
  err.value = ''
  try {
    const out = await api.get<{ users: ManagedUser[] }>('/api/admin/users')
    users.value = out.users
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

async function create(): Promise<void> {
  err.value = ''
  success.value = ''
  try {
    await api.post('/api/admin/users', {
      username: form.value.username.trim(),
      password: form.value.password,
      role: form.value.role,
    })
    form.value = { username: '', password: '', role: 'user' }
    success.value = '用户已创建，首次登录时会被要求修改密码。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function save(row: ManagedUser): Promise<void> {
  err.value = ''
  success.value = ''
  try {
    await api.patch(`/api/admin/users/${row.id}`, { role: row.role, is_active: row.is_active })
    success.value = `用户 ${row.username} 已保存`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function reset(row: ManagedUser): Promise<void> {
  if (!window.confirm(`重置 ${row.username} 的密码？旧会话会立即失效。`)) return
  err.value = ''
  resetNote.value = ''
  resetPassword.value = ''
  try {
    const out = await api.post<{ password: string }>(`/api/admin/users/${row.id}/reset-password`)
    resetPassword.value = out.password
    resetNote.value = `${row.username} 的新密码只显示这一次，请立即复制并告知本人：`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function remove(row: ManagedUser): Promise<void> {
  if (!window.confirm(`删除用户 ${row.username}？该用户的订阅与渠道会一并删除。`)) return
  err.value = ''
  success.value = ''
  try {
    await api.del(`/api/admin/users/${row.id}`)
    success.value = '用户已删除'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}
</script>

<template>
  <PageHeader title="用户管理" description="创建账号、调整角色与启用状态、重置密码。服务端禁止降级/停用/删除自己或最后一个管理员。">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />
  <Notice kind="success" :message="resetNote">
    <code v-if="resetPassword">{{ resetPassword }}</code>
  </Notice>

  <div class="card">
    <h2>新建用户</h2>
    <form @submit.prevent="create">
      <div class="form-row">
        <label>用户名<input v-model="form.username" required /></label>
        <label>初始密码<input v-model="form.password" type="password" required /></label>
        <label>角色
          <select v-model="form.role">
            <option value="user">普通用户</option>
            <option value="admin">管理员</option>
          </select>
        </label>
      </div>
      <div class="form-actions"><button class="primary" type="submit">创建用户</button></div>
    </form>
  </div>

  <div class="card">
    <h2>用户列表</h2>
    <div class="table-wrap">
      <table>
        <thead><tr><th>ID</th><th>用户名</th><th>角色</th><th>启用</th><th>需改密</th><th>创建时间</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="row in users" :key="row.id">
            <td class="mono">{{ row.id }}</td>
            <td>{{ row.username }}</td>
            <td>
              <select v-model="row.role" style="min-width: 110px">
                <option value="user">普通用户</option>
                <option value="admin">管理员</option>
              </select>
            </td>
            <td><label class="inline-check"><input v-model="row.is_active" type="checkbox" /></label></td>
            <td><span class="badge" :class="row.must_change_password ? 'badge-admin' : 'badge-off'">{{ row.must_change_password ? '需要' : '否' }}</span></td>
            <td class="mono">{{ row.created_at }}</td>
            <td>
              <div class="cell-actions">
                <button class="small" type="button" @click="save(row)">保存</button>
                <button class="small" type="button" @click="reset(row)">重置密码</button>
                <button class="small" type="button" @click="remove(row)">删除</button>
              </div>
            </td>
          </tr>
          <tr v-if="!users.length"><td colspan="7" class="empty">暂无用户</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

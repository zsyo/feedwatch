<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { api, errorMessage } from '../api'
import { formatTime } from '../time'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface ManagedUser {
  id: number
  username: string
  role: 'admin' | 'user'
  is_active: boolean
  must_change_password: boolean
  notify_mode: 'digest' | 'per_feed'
  created_at: string
}

interface Subscription {
  id: number
  url: string
  display_name: string
  source_type: string
  notify_policy: string
  enabled: boolean
}

interface Channel {
  id: number
  type: string
  name: string
  enabled: boolean
  secret_fields: string[]
  secret_state: 'ok' | 'missing' | 'error' | 'none'
}

interface Delivery {
  id: number
  cycle_id: string
  kind: string
  status: string
  attempts: number
  sent_at: string
  channel_type: string
  channel_name: string
  last_error: string
  created_at: string
}

const route = useRoute()
const userId = computed(() => Number(route.params.id))

const user = ref<ManagedUser | null>(null)
const counts = ref<Record<string, number>>({})
const subscriptions = ref<Subscription[]>([])
const channels = ref<Channel[]>([])
const enabledChannelCount = computed(() => channels.value.filter((c) => c.enabled).length)
const deliveries = ref<Delivery[]>([])
const err = ref('')
const notice = ref('')

const stateText: Record<Channel['secret_state'], string> = {
  ok: '凭据已配置',
  missing: '缺少凭据',
  error: '无法解密',
  none: '无需凭据',
}

async function load(): Promise<void> {
  err.value = ''
  try {
    const detail = await api.get<{ user: ManagedUser; counts: Record<string, number> }>(
      `/api/admin/users/${userId.value}`,
    )
    user.value = detail.user
    counts.value = detail.counts
    const [subs, chans, dels] = await Promise.all([
      api.get<{ subscriptions: Subscription[] }>(`/api/admin/users/${userId.value}/subscriptions`),
      api.get<{ channels: Channel[] }>(`/api/admin/users/${userId.value}/channels`),
      api.get<{ deliveries: Delivery[] }>(`/api/admin/users/${userId.value}/deliveries?limit=50`),
    ])
    subscriptions.value = subs.subscriptions
    channels.value = chans.channels
    deliveries.value = dels.deliveries
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

async function toggleSubscription(row: Subscription): Promise<void> {
  err.value = ''
  notice.value = ''
  const next = !row.enabled
  try {
    await api.patch(`/api/admin/users/${userId.value}/subscriptions/${row.id}`, { enabled: next })
    row.enabled = next
    notice.value = `订阅 #${row.id} 已${next ? '启用' : '停用'}`
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function toggleChannel(row: Channel): Promise<void> {
  err.value = ''
  notice.value = ''
  const next = !row.enabled
  try {
    await api.patch(`/api/admin/users/${userId.value}/channels/${row.id}`, { enabled: next })
    row.enabled = next
    notice.value = `通知方式 #${row.id} 已${next ? '启用' : '停用'}`
  } catch (e) {
    err.value = errorMessage(e)
  }
}
</script>

<template>
  <PageHeader
    :title="`用户详情 · ${user?.username ?? userId}`"
    description="管理员只能查看数据并停用订阅或通知，不能修改内容，也看不到任何凭据。"
  >
    <RouterLink to="/admin/users" class="ghost" style="padding: .5rem .9rem; border: 1px solid var(--border); border-radius: 7px">返回列表</RouterLink>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="notice" />

  <div class="banner">
    管理员不持有订阅与通知渠道。若要为某个站点配置订阅，请创建一个普通用户并用该身份登录；这里的开关只用于止损。
  </div>

  <div class="stat-grid" style="margin-bottom: 1.2rem">
    <div class="stat"><span class="label">订阅</span><span class="value">{{ counts.subscriptions ?? 0 }}</span></div>
    <div class="stat"><span class="label">通知渠道</span><span class="value">{{ counts.channels ?? 0 }}</span></div>
    <div class="stat"><span class="label">投递记录</span><span class="value">{{ counts.deliveries ?? 0 }}</span></div>
    <div class="stat"><span class="label">待投递</span><span class="value">{{ counts.pending ?? 0 }}</span></div>
    <div class="stat"><span class="label">已放弃</span><span class="value">{{ counts.abandoned ?? 0 }}</span></div>
  </div>

  <div class="card">
    <h2>订阅</h2>
    <p class="card-sub">
      只读，URL 中的凭据已脱敏。通知形式由账号统一设置：
      <span class="mono">{{ user?.notify_mode === 'per_feed' ? 'per_feed（每个项目一条）' : 'digest（汇总一条）' }}</span>
    </p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>ID</th><th>URL</th><th>名称</th><th>策略</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="row in subscriptions" :key="row.id">
            <td class="mono">{{ row.id }}</td>
            <td class="mono cell-url">{{ row.url }}</td>
            <td>{{ row.display_name || '—' }}</td>
            <td>{{ row.notify_policy }}</td>
            <td><span class="badge" :class="row.enabled ? 'badge-on' : 'badge-off'">{{ row.enabled ? '启用' : '停用' }}</span></td>
            <td><button class="small" type="button" @click="toggleSubscription(row)">{{ row.enabled ? '停用' : '启用' }}</button></td>
          </tr>
          <tr v-if="!subscriptions.length"><td colspan="6" class="empty">该用户没有订阅。</td></tr>
        </tbody>
      </table>
    </div>
  </div>

  <div class="card">
    <h2>通知渠道</h2>
    <p class="card-sub">凭据只显示是否已配置，值与密文都不会返回。</p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>ID</th><th>类型 / 名称</th><th>凭据</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="row in channels" :key="row.id">
            <td class="mono">{{ row.id }}</td>
            <td>
              <span class="badge">{{ row.type }}</span>
              <div class="muted" style="margin-top: .3rem">{{ row.name || '—' }}</div>
            </td>
            <td>
              <div class="secret-tags">
                <span v-for="key in row.secret_fields" :key="key" class="tag">{{ key }} ✓</span>
                <span v-if="!row.secret_fields.length" class="tag">未设置</span>
              </div>
              <div class="hint">{{ stateText[row.secret_state] }}</div>
            </td>
            <td><span class="badge" :class="row.enabled ? 'badge-on' : 'badge-off'">{{ row.enabled ? '启用' : '停用' }}</span></td>
            <td><button class="small" type="button" :disabled="row.enabled && enabledChannelCount <= 1" :title="row.enabled && enabledChannelCount <= 1 ? '请至少保留一个启用的通知渠道' : ''" @click="toggleChannel(row)">{{ row.enabled ? '停用' : '启用' }}</button></td>
          </tr>
          <tr v-if="!channels.length"><td colspan="5" class="empty">该用户没有通知渠道。</td></tr>
        </tbody>
      </table>
    </div>
  </div>

  <div class="card">
    <h2>最近投递</h2>
    <p class="card-sub">错误信息已脱敏，最多显示 50 条。</p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>ID</th><th>类型</th><th>渠道</th><th>状态</th><th>尝试</th><th>发送时间</th><th>错误</th></tr></thead>
        <tbody>
          <tr v-for="row in deliveries" :key="row.id">
            <td class="mono">{{ row.id }}</td>
            <td>{{ row.channel_type || '—' }}</td>
            <td>{{ row.channel_name || '—' }}</td>
            <td><span class="badge">{{ row.status }}</span></td>
            <td>{{ row.attempts }}</td>
            <td class="mono">{{ formatTime(row.sent_at) }}</td>
            <td class="mono cell-url">{{ row.last_error || '—' }}</td>
          </tr>
          <tr v-if="!deliveries.length"><td colspan="7" class="empty">暂无投递记录。</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, errorMessage } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'

interface Bucket { key: string; count: number; reset_at: string }
interface RateLimitView {
  poll: Record<string, string>
  notify: { send_spacing: string; gate_granularity: string; note: string }
  http_limiter: {
    login_per_minute: number
    writes_per_minute: number
    active_buckets: number
    buckets: Bucket[]
  }
  delivery_groups_by_channel: Record<string, Record<string, number>>
  applies: string
}

const data = ref<RateLimitView | null>(null)
const err = ref('')

async function load(): Promise<void> {
  err.value = ''
  try {
    data.value = await api.get<RateLimitView>('/api/admin/ratelimit')
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

const pollLabels: Array<[string, string]> = [
  ['interval', '轮询间隔'], ['timeout', '单轮超时'], ['concurrency', '抓取并发'], ['request_spacing', '抓取最小间隔'],
]
</script>

<template>
  <PageHeader title="限速状态" description="查看三层限速参数、按渠道类型的投递分组以及 HTTP 接口的实时限流桶。">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />

  <div class="grid-2">
    <div class="card">
      <h2>抓取与轮询</h2>
      <table>
        <tbody>
          <tr v-for="[key, label] in pollLabels" :key="key">
            <th>{{ label }}</th><td class="mono">{{ data?.poll?.[key] ?? '—' }}</td>
          </tr>
        </tbody>
      </table>
      <p class="hint">参数来自数据库，修改后重启进程生效。</p>
    </div>

    <div class="card">
      <h2>通知投递</h2>
      <table>
        <tbody>
          <tr><th>发送最小间隔</th><td class="mono">{{ data?.notify?.send_spacing ?? '—' }}</td></tr>
          <tr><th>限速粒度</th><td class="mono">{{ data?.notify?.gate_granularity ?? '—' }}</td></tr>
        </tbody>
      </table>
      <p class="hint">{{ data?.notify?.note || '每种渠道类型一个独立限速门，不同服务互不阻塞，不同用户独立请求。' }}</p>
    </div>
  </div>

  <div class="card">
    <h2>按渠道类型的投递组</h2>
    <div v-if="data && Object.keys(data.delivery_groups_by_channel).length" class="table-wrap">
      <table>
        <thead><tr><th>渠道类型</th><th>状态分布</th></tr></thead>
        <tbody>
          <tr v-for="(statuses, type) in data.delivery_groups_by_channel" :key="type">
            <td><span class="badge">{{ type }}</span></td>
            <td>
              <span v-for="(n, status) in statuses" :key="status" class="tag" style="margin-right: .4rem">{{ status }}: {{ n }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else class="empty">暂无投递组</p>
  </div>

  <div class="card">
    <h2>HTTP 限流桶（{{ data?.http_limiter?.active_buckets ?? 0 }}）</h2>
    <p class="card-sub">
      登录每分钟 {{ data?.http_limiter?.login_per_minute ?? 10 }} 次，写操作每分钟 {{ data?.http_limiter?.writes_per_minute ?? 60 }} 次；
      桶 key 中的客户端 IP 已掩码。
    </p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>端点</th><th>当前计数</th><th>重置时间</th></tr></thead>
        <tbody>
          <tr v-for="b in data?.http_limiter?.buckets ?? []" :key="b.key">
            <td class="mono">{{ b.key }}</td><td>{{ b.count }}</td><td class="mono">{{ b.reset_at }}</td>
          </tr>
          <tr v-if="!data?.http_limiter?.buckets?.length"><td colspan="3" class="empty">当前没有活跃限流桶</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

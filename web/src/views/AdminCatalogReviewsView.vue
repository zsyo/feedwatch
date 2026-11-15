<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '../api'
import { formatTime } from '../time'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'
import { copyText, kindLabel, statusBadge, statusLabel } from '../catalog'
import type { CatalogSubmission } from '../catalog'
import { fetchCatalog, fetchReviews, useNotice } from '../catalogAdmin'

const { err, success, clear, clearError, fail, done } = useNotice()
const submissions = ref<CatalogSubmission[]>([])
const pendingCount = ref(0)
const reviewFilter = ref<'pending' | 'all'>('pending')
const expanded = ref<Set<number>>(new Set())
const rejectReasons = ref<Record<number, string>>({})

async function load(): Promise<void> {
  clearError()
  try {
    // Both GETs are safe methods; the server serializes them on its single
    // SQLite connection, so there is no need to sequence them here.
    const [catalog, review] = await Promise.all([
      fetchCatalog(),
      fetchReviews(reviewFilter.value),
    ])
    pendingCount.value = catalog.pending_count
    submissions.value = review.submissions
    const reasons: Record<number, string> = {}
    for (const row of review.submissions) reasons[row.id] = rejectReasons.value[row.id] ?? ''
    rejectReasons.value = reasons
  } catch (e) {
    fail(e)
  }
}
onMounted(load)

async function switchReview(filter: 'pending' | 'all'): Promise<void> {
  reviewFilter.value = filter
  success.value = ''
  await load()
}

function toggleDetail(id: number): void {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

async function approve(row: CatalogSubmission): Promise<void> {
  const what = kindLabel[row.kind] ?? row.kind
  if (!window.confirm(`通过「${row.name}」的${what}？审核通过会立即写入或更新目录。`)) return
  clear()
  try {
    await api.post(`/api/admin/catalog/submissions/${row.id}/approve`, {})
    done(`已通过「${row.name}」，目录已更新。`)
    await load()
  } catch (e) {
    fail(e)
  }
}

async function reject(row: CatalogSubmission): Promise<void> {
  const reason = (rejectReasons.value[row.id] ?? '').trim()
  if (!reason) {
    err.value = '驳回需要填写理由，提交者会在「我的提交」里看到。'
    return
  }
  clear()
  try {
    await api.post(`/api/admin/catalog/submissions/${row.id}/reject`, { reason })
    const next = { ...rejectReasons.value }
    delete next[row.id]
    rejectReasons.value = next
    done(`已驳回「${row.name}」。`)
    await load()
  } catch (e) {
    fail(e)
  }
}

async function copyURL(row: CatalogSubmission): Promise<void> {
  const ok = await copyText(row.url)
  if (ok) done('已复制链接。')
  else fail(new Error('浏览器拒绝了剪贴板访问，请手动选中复制。'))
}
</script>

<template>
  <PageHeader title="审核队列" description="用户提交的新增与更新申请都要在这里处理，通过后才写入目录；管理员自己在「目录条目」页添加或编辑不经过审核。驳回必须填写理由，提交者会看到。">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="card">
    <h2>提交申请（待审 {{ pendingCount }}）</h2>
    <p class="card-sub">「对比」展开改前 / 改后内容；审核中的链接仍可按「复制链接」手动取用，但 FeedWatch 不验证其内容与安全性。</p>
    <div class="catalog-toolbar">
      <div class="toolbar-field">
        <span class="toolbar-label">显示范围</span>
        <div class="form-actions">
          <button class="small" :class="{ 'is-active': reviewFilter === 'pending' }" type="button" @click="switchReview('pending')">仅待审</button>
          <button class="small" :class="{ 'is-active': reviewFilter === 'all' }" type="button" @click="switchReview('all')">全部历史</button>
        </div>
      </div>
    </div>
    <div class="table-wrap">
      <table>
        <thead><tr><th>类型</th><th>名称</th><th>分类</th><th>提交者</th><th>状态</th><th>时间</th><th>操作</th></tr></thead>
        <tbody>
          <template v-for="row in submissions" :key="row.id">
            <tr>
              <td><span class="tag">{{ kindLabel[row.kind] ?? row.kind }}</span></td>
              <td>{{ row.name }}</td>
              <td>{{ row.category_name }}</td>
              <td>{{ row.submitted_by_name || '—' }}</td>
              <td><span class="badge" :class="statusBadge(row.status)">{{ statusLabel[row.status] ?? row.status }}</span></td>
              <td class="mono">{{ formatTime(row.created_at) }}</td>
              <td>
                <div class="cell-actions">
                  <button class="small" type="button" @click="toggleDetail(row.id)">{{ expanded.has(row.id) ? '收起' : '对比' }}</button>
                  <button class="small" type="button" @click="copyURL(row)">复制链接</button>
                  <template v-if="row.status === 'pending'">
                    <button class="small" type="button" @click="approve(row)">通过</button>
                    <button class="small" type="button" @click="reject(row)">驳回</button>
                  </template>
                </div>
                <input
                  v-if="row.status === 'pending'"
                  v-model="rejectReasons[row.id]"
                  class="reject-input"
                  placeholder="驳回理由（必填）"
                  :maxlength="500"
                />
              </td>
            </tr>
            <tr v-if="expanded.has(row.id)">
              <td colspan="7" class="catalog-detail-cell">
                <div class="compare-grid">
                  <div v-if="row.before_json" class="diff-cell diff-old">
                    <strong>修改前</strong>
                    <div>名称：{{ row.before_json.name }}</div>
                    <div>分类：{{ row.before_json.category_name }}</div>
                    <div class="mono" style="word-break: break-all">链接：{{ row.before_json.url }}</div>
                    <div>简介：{{ row.before_json.description || '—' }}</div>
                    <div v-if="row.before_json.homepage">主页：{{ row.before_json.homepage }}</div>
                  </div>
                  <div class="diff-cell diff-new">
                    <strong>{{ row.before_json ? '拟改为' : '拟新增' }}</strong>
                    <div>名称：{{ row.name }}</div>
                    <div>分类：{{ row.category_name }}</div>
                    <div class="mono" style="word-break: break-all">链接：{{ row.url }}</div>
                    <div>简介：{{ row.description || '—' }}</div>
                    <div v-if="row.homepage">主页：{{ row.homepage }}</div>
                    <div class="form-actions">
                      <button class="small" type="button" @click="copyURL(row)">复制链接</button>
                      <a :href="row.url" target="_blank" rel="noopener noreferrer" class="link">在新标签打开</a>
                    </div>
                  </div>
                </div>
                <p v-if="row.reject_reason" class="hint">驳回理由：{{ row.reject_reason }}</p>
                <p v-if="row.reviewed_at" class="hint">审核人：{{ row.reviewed_by_name || '—' }} · {{ formatTime(row.reviewed_at) }}</p>
              </td>
            </tr>
          </template>
          <tr v-if="!submissions.length"><td colspan="7" class="empty">{{ reviewFilter === 'pending' ? '当前没有待审核的申请。' : '还没有任何审核记录。' }}</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

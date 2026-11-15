<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api, errorMessage } from '../api'
import { formatTime } from '../time'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'
import { copyText, kindLabel } from '../catalog'
import type { CatalogCategory, CatalogMeta, CatalogSubmission } from '../catalog'

interface PendingResponse {
  submissions: CatalogSubmission[]
  categories: CatalogCategory[]
  meta: CatalogMeta
}

const items = ref<CatalogSubmission[]>([])
const categories = ref<CatalogCategory[]>([])
const err = ref('')
const success = ref('')
const keyword = ref('')
const categoryFilter = ref('all')
const expanded = ref<Set<number>>(new Set())

const filtered = computed(() => {
  const needle = keyword.value.trim().toLowerCase()
  return items.value.filter(row => {
    if (needle) {
      const haystack = [row.name, row.url, row.description, row.category_name].join(' ').toLowerCase()
      if (!haystack.includes(needle)) return false
    }
    if (categoryFilter.value === 'none' && row.category_id !== null) return false
    if (categoryFilter.value !== 'all' && categoryFilter.value !== 'none' &&
        String(row.category_id) !== categoryFilter.value) return false
    return true
  })
})

async function load(): Promise<void> {
  err.value = ''
  try {
    const out = await api.get<PendingResponse>('/api/catalog/pending')
    items.value = out.submissions
    categories.value = out.categories
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

function toggleDetail(id: number): void {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

async function copyInfo(row: CatalogSubmission): Promise<void> {
  err.value = ''
  success.value = ''
  const text = [
    `名称：${row.name}`,
    `订阅链接：${row.url}`,
    `分类：${row.category_name}`,
    `简介：${row.description || '—'}`,
    row.homepage ? `项目主页：${row.homepage}` : '',
    '（该链接处于审核状态，请自行斟酌风险）',
  ].filter(Boolean).join('\n')
  const ok = await copyText(text)
  if (ok) success.value = '已复制，可前往「我的订阅」手动添加。链接尚未审核，请自行斟酌风险。'
  else err.value = '浏览器拒绝了剪贴板访问，请手动选中下方链接复制。'
}
</script>

<template>
  <PageHeader title="审核中的链接" description="这里汇总了所有待审核的新增申请与信息更新申请，供所有用户查看。">
    <RouterLink to="/catalog" class="link-button">返回订阅中心</RouterLink>
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>

  <div class="banner banner-warn">
    <strong>链接处于审核状态，需自行斟酌风险。</strong>
    以下条目尚未经过管理员审核，<strong>不能直接订阅</strong>。如确需使用，请自行复制信息到「我的订阅」里手动添加；FeedWatch 未验证这些链接的内容、可用性与安全性，由此产生的风险由你自行承担。
  </div>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="catalog-toolbar">
    <label class="toolbar-field">关键字
      <input v-model="keyword" placeholder="搜索名称、链接、简介或分类" />
    </label>
    <label class="toolbar-field-narrow">分类
      <select v-model="categoryFilter">
        <option value="all">全部分类</option>
        <option v-for="c in categories" :key="c.id" :value="c.id === 0 ? 'none' : String(c.id)">{{ c.name }}</option>
      </select>
    </label>
  </div>

  <div class="card">
    <h2>待审列表</h2>
    <p class="card-sub">「新增申请」是尚未上架的链接；「更新申请」是对已有订阅条目的修改建议，通过后才会生效。</p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>类型</th><th>名称</th><th>分类</th><th>链接</th><th>提交者</th><th>时间</th><th>操作</th></tr></thead>
        <tbody>
          <template v-for="row in filtered" :key="row.id">
            <tr>
              <td><span class="tag">{{ kindLabel[row.kind] ?? row.kind }}</span></td>
              <td>{{ row.name }}</td>
              <td>{{ row.category_name }}</td>
              <td class="mono cell-url">{{ row.url }}</td>
              <td>{{ row.submitted_by_name || '—' }}</td>
              <td class="mono">{{ formatTime(row.created_at) }}</td>
              <td>
                <div class="cell-actions">
                  <button class="small" type="button" @click="copyInfo(row)">复制信息</button>
                  <button class="small" type="button" @click="toggleDetail(row.id)">{{ expanded.has(row.id) ? '收起' : '详情' }}</button>
                </div>
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
                  </div>
                </div>
                <p class="hint">该信息尚未审核通过，复制链接手动添加前请自行确认来源可信。</p>
              </td>
            </tr>
          </template>
          <tr v-if="!filtered.length"><td colspan="7" class="empty">当前没有待审核的申请。</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api, errorMessage } from '../api'
import { formatTime } from '../time'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'
import { copyText, kindLabel, statusBadge, statusLabel } from '../catalog'
import type { CatalogCategory, CatalogEntry, CatalogMeta, CatalogSubmission } from '../catalog'

interface CatalogResponse {
  entries: CatalogEntry[]
  categories: CatalogCategory[]
  total: number
  meta: CatalogMeta
}

const entries = ref<CatalogEntry[]>([])
const categories = ref<CatalogCategory[]>([])
const mine = ref<CatalogSubmission[]>([])
const meta = ref<CatalogMeta | null>(null)
const err = ref('')
const success = ref('')
const busy = ref(false)
const keyword = ref('')
const categoryFilter = ref('all')
const sortBy = ref<'hot' | 'name' | 'time'>('hot')
const sortDesc = ref(true)

// id 0 is the backend's synthetic "uncategorized" bucket; it is not a real
// category and cannot be chosen when filing a proposal.
const realCategories = computed(() => categories.value.filter(c => c.id !== 0))

const filtered = computed(() => {
  const needle = keyword.value.trim().toLowerCase()
  let rows = entries.value.filter(row => {
    if (needle) {
      // Category is deliberately not searchable: the filter control right next
      // to this box already covers it, and matching it here made a keyword
      // like "工具" return the whole category instead of name/URL matches.
      const haystack = [row.name, row.url, row.description].join(' ').toLowerCase()
      if (!haystack.includes(needle)) return false
    }
    if (categoryFilter.value === 'none' && row.category_id !== null) return false
    if (categoryFilter.value !== 'all' && categoryFilter.value !== 'none' &&
        String(row.category_id) !== categoryFilter.value) return false
    return true
  })
  // Array.sort puts a before b for a negative result, so descending needs -1.
  const dir = sortDesc.value ? -1 : 1
  rows = [...rows].sort((a, b) => {
    if (sortBy.value === 'name') return dir * a.name.localeCompare(b.name, 'zh-CN') || a.id - b.id
    // created_at is RFC 3339 with the same precision, so string order is
    // chronological order — no Date parsing needed.
    if (sortBy.value === 'time') return dir * a.created_at.localeCompare(b.created_at) || a.id - b.id
    return dir * (a.subscriber_count - b.subscriber_count) || a.id - b.id
  })
  return rows
})

async function load(): Promise<void> {
  err.value = ''
  try {
    const [catalog, submissions] = await Promise.all([
      api.get<CatalogResponse>('/api/catalog'),
      api.get<{ submissions: CatalogSubmission[] }>('/api/catalog/submissions/mine'),
    ])
    entries.value = catalog.entries
    categories.value = catalog.categories
    meta.value = catalog.meta
    mine.value = submissions.submissions
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

const draft = ref({ name: '', url: '', category_id: 0, description: '', homepage: '' })

async function submitNew(): Promise<void> {
  err.value = ''
  success.value = ''
  if (!draft.value.category_id) {
    err.value = '请选择一个分类。'
    return
  }
  busy.value = true
  try {
    await api.post('/api/catalog/submissions', { kind: 'new', ...draft.value })
    draft.value = { name: '', url: '', category_id: 0, description: '', homepage: '' }
    success.value = '已提交，等待管理员审核；可在「审核中」页面查看。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

const editing = ref<number | null>(null)
const updateDraft = ref({ entry_id: 0, name: '', url: '', category_id: 0, description: '', homepage: '' })

function startUpdate(row: CatalogEntry): void {
  err.value = ''
  success.value = ''
  editing.value = editing.value === row.id ? null : row.id
  updateDraft.value = {
    entry_id: row.id, name: row.name, url: row.url,
    category_id: row.category_id ?? 0, description: row.description, homepage: row.homepage,
  }
}

async function submitUpdate(): Promise<void> {
  err.value = ''
  success.value = ''
  if (!updateDraft.value.category_id) {
    err.value = '请选择一个分类。'
    return
  }
  busy.value = true
  try {
    await api.post('/api/catalog/submissions', { kind: 'update', ...updateDraft.value })
    editing.value = null
    success.value = '更新申请已提交，管理员审核通过后才会生效。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

async function subscribe(row: CatalogEntry): Promise<void> {
  err.value = ''
  success.value = ''
  try {
    await api.post(`/api/catalog/${row.id}/subscribe`, {})
    success.value = `已订阅「${row.name}」，从当前基线开始，不会补发历史版本。`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function cancel(row: CatalogEntry): Promise<void> {
  if (row.subscription_id === null) return
  if (!window.confirm(`确定取消订阅「${row.name}」？订阅条目会保留。`)) return
  err.value = ''
  success.value = ''
  try {
    await api.del(`/api/subscriptions/${row.subscription_id}`)
    success.value = '已取消订阅。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function withdraw(row: CatalogSubmission): Promise<void> {
  if (!window.confirm(`确定撤回「${row.name}」这条待审申请？`)) return
  err.value = ''
  success.value = ''
  try {
    await api.del(`/api/catalog/submissions/${row.id}`)
    success.value = '已撤回。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

const expanded = ref<Set<number>>(new Set())

function toggleDetail(id: number): void {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

async function copyProposal(row: CatalogSubmission): Promise<void> {
  const text = [row.name, row.url, row.category_name, row.description].filter(Boolean).join('\n')
  const ok = await copyText(text)
  if (ok) success.value = '已复制申请内容。'
  else err.value = '浏览器拒绝了剪贴板访问，请手动选中复制。'
}
</script>

<template>
  <PageHeader title="订阅中心" description="从已审核的订阅目录中挑选项目，一键复制到「我的订阅」；目录只是模板，停用或删除不会影响你已经订阅的项目。">
    <RouterLink to="/catalog/pending" class="link-button">审核中的链接</RouterLink>
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="catalog-toolbar">
    <label class="toolbar-field">关键字
      <input v-model="keyword" placeholder="搜索名称、链接或简介" />
    </label>
    <label class="toolbar-field-narrow">分类
      <select v-model="categoryFilter">
        <option value="all">全部分类</option>
        <option v-for="c in categories" :key="c.id" :value="c.id === 0 ? 'none' : String(c.id)">
          {{ c.name }}（{{ c.entry_count }}）
        </option>
      </select>
    </label>
    <label class="toolbar-field-narrow">排序
      <select v-model="sortBy">
        <option value="hot">订阅人数</option>
        <option value="time">收录时间</option>
        <option value="name">名称</option>
      </select>
    </label>
    <button class="ghost" type="button" @click="sortDesc = !sortDesc">{{ sortDesc ? '降序 ↓' : '升序 ↑' }}</button>
  </div>

  <div class="catalog-grid">
    <div v-for="row in filtered" :key="row.id" class="catalog-card">
      <div class="catalog-card-head">
        <h3>{{ row.name }}</h3>
        <span class="tag">{{ row.category_name }}</span>
      </div>
      <p class="catalog-desc">{{ row.description || '暂无简介' }}</p>
      <div class="catalog-meta">
        <span>{{ row.subscriber_count }} 人订阅</span> · <span>收录于 {{ formatTime(row.created_at) }}</span>
      </div>
      <div class="catalog-meta mono catalog-url" :title="row.url">{{ row.url }}</div>
      <div class="catalog-home">
        <a v-if="row.homepage" :href="row.homepage" target="_blank" rel="noopener noreferrer" class="link">项目主页</a>
        <span v-else class="muted">暂无项目主页</span>
      </div>
      <div class="catalog-actions">
        <button v-if="!row.subscribed" class="primary" type="button" @click="subscribe(row)">一键订阅</button>
        <template v-else>
          <span class="badge badge-on">已订阅</span>
          <button class="danger" type="button" @click="cancel(row)">取消订阅</button>
        </template>
        <button class="small" type="button" @click="startUpdate(row)">{{ editing === row.id ? '收起' : '更新信息' }}</button>
      </div>
      <form v-if="editing === row.id" class="catalog-edit" @submit.prevent="submitUpdate">
        <p class="card-sub">提交后进入审核，管理员通过后才会更新目录；不影响你已订阅的链接。</p>
        <div class="form-row">
          <label>名称<input v-model="updateDraft.name" required :maxlength="meta?.max_name_len" /></label>
          <label>订阅 URL<input v-model="updateDraft.url" type="url" required :maxlength="meta?.max_url_len" /></label>
          <label>分类
            <select v-model.number="updateDraft.category_id" required>
              <option :value="0" disabled>请选择分类</option>
              <option v-for="c in realCategories" :key="c.id" :value="c.id">{{ c.name }}</option>
            </select>
          </label>
          <label>项目主页<input v-model="updateDraft.homepage" :maxlength="meta?.max_homepage_len" /></label>
        </div>
        <label>简介<textarea v-model="updateDraft.description" :maxlength="meta?.max_description_len" /></label>
        <div class="form-actions">
          <button class="primary" type="submit" :disabled="busy">提交更新申请</button>
          <button class="small" type="button" @click="editing = null">取消</button>
        </div>
      </form>
    </div>
    <p v-if="!filtered.length" class="empty">没有匹配的订阅项目。换个关键字，或到「审核中的链接」看看候选内容。</p>
  </div>

  <div class="card">
    <h2>提交新订阅</h2>
    <p class="card-sub">找不到想要的项目？提交信息后由管理员审核，通过才会出现在上面的目录里。链接只允许 http/https，且不能带账号密码或令牌。</p>
    <form @submit.prevent="submitNew">
      <div class="form-row">
        <label>名称<input v-model="draft.name" required placeholder="例如：Node.js" :maxlength="meta?.max_name_len" /></label>
        <label>订阅 URL<input v-model="draft.url" type="url" required placeholder="https://github.com/owner/repo/releases.atom" :maxlength="meta?.max_url_len" /></label>
        <label>分类
          <select v-model.number="draft.category_id" required>
            <option :value="0" disabled>请选择分类</option>
            <option v-for="c in realCategories" :key="c.id" :value="c.id">{{ c.name }}</option>
          </select>
        </label>
        <label>项目主页<input v-model="draft.homepage" placeholder="选填" :maxlength="meta?.max_homepage_len" /></label>
      </div>
      <label>简介<textarea v-model="draft.description" placeholder="一句话说明这个订阅源" :maxlength="meta?.max_description_len" /></label>
      <div class="form-actions">
        <button class="primary" type="submit" :disabled="busy">提交审核</button>
        <span class="hint">每人最多 {{ meta?.max_pending_per_user ?? 10 }} 条待审申请</span>
      </div>
    </form>
  </div>

  <div class="card">
    <h2>我的提交</h2>
    <p class="card-sub">审核通过、驳回或撤回的记录都会保留在这里。驳回理由由管理员填写。</p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>类型</th><th>名称</th><th>分类</th><th>状态</th><th>提交时间</th><th>操作</th></tr></thead>
        <tbody>
          <template v-for="row in mine" :key="row.id">
            <tr>
              <td><span class="tag">{{ kindLabel[row.kind] ?? row.kind }}</span></td>
              <td>{{ row.name }}</td>
              <td>{{ row.category_name }}</td>
              <td><span class="badge" :class="statusBadge(row.status)">{{ statusLabel[row.status] ?? row.status }}</span></td>
              <td class="mono">{{ formatTime(row.created_at) }}</td>
              <td>
                <div class="cell-actions">
                  <button class="small" type="button" @click="toggleDetail(row.id)">{{ expanded.has(row.id) ? '收起' : '详情' }}</button>
                  <button class="small" type="button" @click="copyProposal(row)">复制</button>
                  <button v-if="row.status === 'pending'" class="small" type="button" @click="withdraw(row)">撤回</button>
                </div>
              </td>
            </tr>
            <tr v-if="expanded.has(row.id)">
              <td colspan="6" class="catalog-detail-cell">
                <div class="compare-grid">
                  <div v-if="row.before_json" class="diff-cell diff-old">
                    <strong>修改前</strong>
                    <div>名称：{{ row.before_json.name }}</div>
                    <div>分类：{{ row.before_json.category_name }}</div>
                    <div class="mono" style="word-break: break-all">链接：{{ row.before_json.url }}</div>
                    <div>简介：{{ row.before_json.description || '—' }}</div>
                  </div>
                  <div class="diff-cell diff-new">
                    <strong>{{ row.before_json ? '修改后（拟）' : '拟新增内容' }}</strong>
                    <div>名称：{{ row.name }}</div>
                    <div>分类：{{ row.category_name }}</div>
                    <div class="mono" style="word-break: break-all">链接：{{ row.url }}</div>
                    <div>简介：{{ row.description || '—' }}</div>
                    <div v-if="row.homepage">主页：{{ row.homepage }}</div>
                  </div>
                </div>
                <p v-if="row.reject_reason" class="hint">驳回理由：{{ row.reject_reason }}</p>
                <p v-if="row.reviewed_at" class="hint">审核时间：{{ formatTime(row.reviewed_at) }}</p>
              </td>
            </tr>
          </template>
          <tr v-if="!mine.length"><td colspan="6" class="empty">还没有提交过订阅信息。</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

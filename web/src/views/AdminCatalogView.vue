<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, errorMessage } from '../api'
import { formatTime } from '../time'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'
import { copyText, kindLabel, statusBadge, statusLabel } from '../catalog'
import type { CatalogCategory, CatalogEntry, CatalogMeta, CatalogSubmission } from '../catalog'
import type { PresetSyncResult } from '../presets'

interface AdminCatalogResponse {
  entries: CatalogEntry[]
  categories: CatalogCategory[]
  pending_count: number
  meta: CatalogMeta
}
interface ReviewResponse {
  submissions: CatalogSubmission[]
  categories: CatalogCategory[]
}
interface ImportResult {
  ok: boolean
  created: number
  updated: number
  skipped: number
}
// Only metadata travels between installations. The backend validates every
// field again, so this shape is checked loosely here and strictly in Go.
interface CatalogDocument {
  format: string
  version: number
  exported_at?: string
  categories: Array<{ name: string }>
  entries: Array<{
    name: string
    url: string
    category: string
    description: string
    homepage: string
    enabled: boolean
  }>
  strategy?: string
}

const entries = ref<CatalogEntry[]>([])
const categories = ref<CatalogCategory[]>([])
const meta = ref<CatalogMeta | null>(null)
const pendingCount = ref(0)
const submissions = ref<CatalogSubmission[]>([])
const reviewFilter = ref<'pending' | 'all'>('pending')
const err = ref('')
const success = ref('')
const busy = ref(false)
const expanded = ref<Set<number>>(new Set())
const rejectReasons = ref<Record<number, string>>({})

const uncategorized = computed(() => meta.value?.uncategorized_label ?? '未分类')

async function load(): Promise<void> {
  err.value = ''
  try {
    // Both GETs are safe methods; the server serializes them on its single
    // SQLite connection, so there is no need to sequence them here.
    const [catalog, review] = await Promise.all([
      api.get<AdminCatalogResponse>('/api/admin/catalog'),
      api.get<ReviewResponse>(`/api/admin/catalog/submissions?status=${reviewFilter.value}`),
    ])
    entries.value = catalog.entries
    categories.value = catalog.categories
    meta.value = catalog.meta
    pendingCount.value = catalog.pending_count
    submissions.value = review.submissions
    const reasons: Record<number, string> = {}
    for (const row of review.submissions) reasons[row.id] = rejectReasons.value[row.id] ?? ''
    rejectReasons.value = reasons
  } catch (e) {
    err.value = errorMessage(e)
  }
}
onMounted(load)

// ---- directory entries: administrators edit directly, without review ----
function blankEntry() {
  return { name: '', url: '', category_id: 0, description: '', homepage: '', enabled: true }
}
const createDraft = ref(blankEntry())

async function createEntry(): Promise<void> {
  err.value = ''
  success.value = ''
  if (!createDraft.value.category_id) {
    err.value = '请选择一个分类。'
    return
  }
  busy.value = true
  try {
    await api.post('/api/admin/catalog', createDraft.value)
    createDraft.value = blankEntry()
    success.value = '目录条目已添加。管理员的添加与编辑不经过审核，立即生效。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

const editing = ref<number | null>(null)
const editDraft = ref({ name: '', url: '', category_id: 0, description: '', homepage: '', enabled: true })

function startEdit(row: CatalogEntry): void {
  err.value = ''
  success.value = ''
  editing.value = editing.value === row.id ? null : row.id
  editDraft.value = {
    name: row.name, url: row.url, category_id: row.category_id ?? 0,
    description: row.description, homepage: row.homepage, enabled: row.enabled,
  }
}

async function saveEdit(row: CatalogEntry): Promise<void> {
  err.value = ''
  success.value = ''
  busy.value = true
  try {
    // category_id=0 is the "uncategorized" sentinel; the backend accepts an
    // explicit null to keep an entry uncategorized after its category was removed.
    await api.patch(`/api/admin/catalog/${row.id}`, {
      name: editDraft.value.name,
      url: editDraft.value.url,
      description: editDraft.value.description,
      homepage: editDraft.value.homepage,
      category_id: editDraft.value.category_id === 0 ? null : editDraft.value.category_id,
      enabled: editDraft.value.enabled,
    })
    editing.value = null
    success.value = `「${row.name}」已更新。已订阅用户仍使用自己订阅里的链接，不受影响。`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

async function toggleEnabled(row: CatalogEntry): Promise<void> {
  err.value = ''
  success.value = ''
  try {
    await api.patch(`/api/admin/catalog/${row.id}`, { enabled: !row.enabled })
    success.value = row.enabled
      ? `「${row.name}」已停用：不再出现在订阅中心，但仍占用该 URL，已订阅用户不受影响。`
      : `「${row.name}」已重新启用。`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function removeEntry(row: CatalogEntry): Promise<void> {
  if (!window.confirm(`删除目录条目「${row.name}」？只会删除这个模板并释放该 URL，已经订阅的用户不受影响。`)) return
  err.value = ''
  success.value = ''
  try {
    await api.del(`/api/admin/catalog/${row.id}`)
    success.value = '目录条目已删除。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

// ---- review queue ----
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
  err.value = ''
  success.value = ''
  try {
    await api.post(`/api/admin/catalog/submissions/${row.id}/approve`, {})
    success.value = `已通过「${row.name}」，目录已更新。`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function reject(row: CatalogSubmission): Promise<void> {
  const reason = (rejectReasons.value[row.id] ?? '').trim()
  if (!reason) {
    err.value = '驳回需要填写理由，提交者会在「我的提交」里看到。'
    return
  }
  err.value = ''
  success.value = ''
  try {
    await api.post(`/api/admin/catalog/submissions/${row.id}/reject`, { reason })
    const next = { ...rejectReasons.value }
    delete next[row.id]
    rejectReasons.value = next
    success.value = `已驳回「${row.name}」。`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function copyURL(row: CatalogSubmission): Promise<void> {
  const ok = await copyText(row.url)
  if (ok) success.value = '已复制链接。'
  else err.value = '浏览器拒绝了剪贴板访问，请手动选中复制。'
}

// ---- categories ----
const newCategory = ref('')

async function addCategory(): Promise<void> {
  err.value = ''
  success.value = ''
  const name = newCategory.value.trim()
  if (!name) {
    err.value = '请填写分类名称。'
    return
  }
  try {
    await api.post('/api/admin/catalog/categories', { name })
    newCategory.value = ''
    success.value = `分类「${name}」已添加。`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function renameCategory(row: CatalogCategory): Promise<void> {
  err.value = ''
  success.value = ''
  try {
    await api.patch(`/api/admin/catalog/categories/${row.id}`, { name: row.name })
    success.value = '分类已重命名。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

async function deleteCategory(row: CatalogCategory): Promise<void> {
  if (!window.confirm(
    `删除分类「${row.name}」？该分类下的 ${row.entry_count} 个目录条目以及相关申请会统一改为「${uncategorized.value}」。`,
  )) return
  err.value = ''
  success.value = ''
  try {
    const out = await api.del<{ ok: boolean; reassigned: number }>(`/api/admin/catalog/categories/${row.id}`)
    success.value = `已删除分类，${out.reassigned} 条记录改为「${uncategorized.value}」。`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}

// ---- import / export ----
const importStrategy = ref<'skip' | 'overwrite' | 'error'>('skip')
const importFile = ref<File | null>(null)
const importResult = ref('')
const fileInput = ref<HTMLInputElement | null>(null)

async function exportCatalog(): Promise<void> {
  err.value = ''
  success.value = ''
  try {
    const doc = await api.get<CatalogDocument>('/api/admin/catalog/export')
    const blob = new Blob([JSON.stringify(doc, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `feedwatch-catalog-${new Date().toISOString().slice(0, 10)}.json`
    document.body.appendChild(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(url)
    success.value = `已导出 ${doc.entries.length} 个条目、${doc.categories.length} 个分类；文件不含订阅、渠道凭据或投递状态。`
  } catch (e) {
    err.value = errorMessage(e)
  }
}

function pickFile(event: Event): void {
  const input = event.target as HTMLInputElement
  importFile.value = input.files?.[0] ?? null
  importResult.value = ''
}

async function importCatalog(): Promise<void> {
  err.value = ''
  success.value = ''
  importResult.value = ''
  const file = importFile.value
  if (!file) {
    err.value = '请先选择导出的 JSON 文件。'
    return
  }
  let doc: CatalogDocument
  try {
    doc = JSON.parse(await file.text()) as CatalogDocument
  } catch {
    err.value = '文件不是合法的 JSON。'
    return
  }
  if (!doc || doc.format !== 'feedwatch-catalog' || doc.version !== 1 || !Array.isArray(doc.entries)) {
    err.value = '不是受支持的 feedwatch-catalog v1 文档。'
    return
  }
  busy.value = true
  try {
    const out = await api.post<ImportResult>('/api/admin/catalog/import', {
      ...doc, strategy: importStrategy.value,
    })
    importResult.value = `新增 ${out.created}、更新 ${out.updated}、跳过 ${out.skipped}。`
    importFile.value = null
    if (fileInput.value) fileInput.value.value = ''
    success.value = '导入完成。导入只影响目录，不会创建任何订阅。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}

// ---- remote presets ----
const syncing = ref(false)
const syncResult = ref<PresetSyncResult | null>(null)

async function syncRemote(): Promise<void> {
  err.value = ''
  success.value = ''
  syncResult.value = null
  syncing.value = true
  try {
    const out = await api.post<PresetSyncResult>('/api/admin/catalog/presets/sync', {})
    syncResult.value = out
    success.value = `远程预设同步完成：新增 ${out.created}、更新 ${out.updated}、跳过 ${out.skipped}、受保护 ${out.protected}、认领 ${out.adopted}。`
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  } finally {
    syncing.value = false
  }
}

async function acceptRemote(row: CatalogEntry): Promise<void> {
  if (!window.confirm(
    `「${row.name}」被标记为本地已编辑，远程同步不会覆盖它。解除保护后，下次同步会用更高版本覆盖你的修改。继续？`,
  )) return
  err.value = ''
  success.value = ''
  try {
    await api.post(`/api/admin/catalog/${row.id}/accept-remote`, {})
    success.value = '已解除保护，下次「获取远程预设数据」会应用更新的版本。'
    await load()
  } catch (e) {
    err.value = errorMessage(e)
  }
}
</script>

<template>
  <PageHeader title="目录管理" description="维护订阅中心的可用链接、审核用户提交、管理分类，并可导入导出目录。目录只是模板：编辑、停用或删除都不会改动用户已经订阅的链接。">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="card">
    <h2>审核队列（待审 {{ pendingCount }}）</h2>
    <p class="card-sub">用户提交的新增与更新申请都要在这里处理，通过后才写入目录；管理员自己添加或编辑条目不经过审核。</p>
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

  <div class="card">
    <h2>手动添加目录条目</h2>
    <p class="card-sub">管理员是可信角色，这里的添加立即生效，不产生待审申请。链接只允许 http/https，且不能带账号密码或令牌类参数。</p>
    <form @submit.prevent="createEntry">
      <div class="form-row">
        <label>名称<input v-model="createDraft.name" required :maxlength="meta?.max_name_len" /></label>
        <label>订阅 URL<input v-model="createDraft.url" type="url" required placeholder="https://github.com/owner/repo/releases.atom" :maxlength="meta?.max_url_len" /></label>
        <label>分类
          <select v-model.number="createDraft.category_id" required>
            <option :value="0" disabled>请选择分类</option>
            <option v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}</option>
          </select>
        </label>
        <label>项目主页<input v-model="createDraft.homepage" placeholder="选填" :maxlength="meta?.max_homepage_len" /></label>
      </div>
      <label>简介<textarea v-model="createDraft.description" :maxlength="meta?.max_description_len" /></label>
      <div class="form-actions">
        <label class="inline-check"><input v-model="createDraft.enabled" type="checkbox" /> 启用（可在订阅中心展示并订阅）</label>
        <button class="primary" type="submit" :disabled="busy">添加条目</button>
      </div>
    </form>
  </div>

  <div class="card">
    <h2>目录条目（{{ entries.length }}）</h2>
    <p class="card-sub">停用只是从订阅中心隐藏，仍占用该 URL；删除才会释放 URL。两者都不会改动用户已经复制过去的订阅。</p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>ID</th><th>名称</th><th>分类</th><th>链接</th><th>订阅人数</th><th>状态</th><th>预设状态</th><th>更新时间</th><th>操作</th></tr></thead>
        <tbody>
          <template v-for="row in entries" :key="row.id">
            <tr>
              <td class="mono">{{ row.id }}</td>
              <td>{{ row.name }}</td>
              <td>{{ row.category_name }}</td>
              <td class="mono cell-url">{{ row.url }}</td>
              <td>{{ row.subscriber_count }}</td>
              <td><span class="badge" :class="row.enabled ? 'badge-on' : 'badge-off'">{{ row.enabled ? '启用' : '停用' }}</span></td>
              <td>
                <span v-if="!row.preset_key" class="muted">手动</span>
                <span v-else class="preset-state">
                  <span class="mono">v{{ row.preset_version }}</span>
                  <span v-if="row.preset_edited" class="badge badge-warn">已保护</span>
                  <span v-else class="badge badge-on">可更新</span>
                </span>
              </td>
              <td class="mono">{{ formatTime(row.updated_at) }}</td>
              <td>
                <div class="cell-actions">
                  <button class="small" type="button" @click="startEdit(row)">{{ editing === row.id ? '收起' : '编辑' }}</button>
                  <button class="small" type="button" @click="toggleEnabled(row)">{{ row.enabled ? '停用' : '启用' }}</button>
                  <button class="small" type="button" @click="removeEntry(row)">删除</button>
                  <button v-if="row.preset_key && row.preset_edited" class="small" type="button" @click="acceptRemote(row)">接受远程更新</button>
                </div>
              </td>
            </tr>
            <tr v-if="editing === row.id">
              <td colspan="9" class="catalog-detail-cell">
                <form class="catalog-edit" @submit.prevent="saveEdit(row)">
                  <div class="form-row">
                    <label>名称<input v-model="editDraft.name" required :maxlength="meta?.max_name_len" /></label>
                    <label>订阅 URL<input v-model="editDraft.url" type="url" required :maxlength="meta?.max_url_len" /></label>
                    <label>分类
                      <select v-model.number="editDraft.category_id">
                        <option :value="0">{{ uncategorized }}</option>
                        <option v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}</option>
                      </select>
                    </label>
                    <label>项目主页<input v-model="editDraft.homepage" :maxlength="meta?.max_homepage_len" /></label>
                  </div>
                  <label>简介<textarea v-model="editDraft.description" :maxlength="meta?.max_description_len" /></label>
                  <div class="form-actions">
                    <label class="inline-check"><input v-model="editDraft.enabled" type="checkbox" /> 启用</label>
                    <button class="primary" type="submit" :disabled="busy">保存修改</button>
                    <button class="small" type="button" @click="editing = null">取消</button>
                  </div>
                </form>
              </td>
            </tr>
          </template>
          <tr v-if="!entries.length"><td colspan="9" class="empty">目录还是空的，可在上方手动添加，或在下方导入。</td></tr>
        </tbody>
      </table>
    </div>
  </div>

  <div class="card">
    <h2>分类管理</h2>
    <p class="card-sub">用户提交和管理员新增都必须选择分类。删除分类时，该分类下的目录条目与申请会自动改为「{{ uncategorized }}」。</p>
    <div class="catalog-toolbar">
      <label class="toolbar-field">新建分类名称
        <input v-model="newCategory" placeholder="例如：数据库" :maxlength="meta?.max_category_len" @keyup.enter="addCategory" />
      </label>
      <button class="primary" type="button" @click="addCategory">添加分类</button>
    </div>
    <div class="table-wrap">
      <table>
        <thead><tr><th>ID</th><th>名称</th><th>目录条目数</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="row in categories" :key="row.id">
            <td class="mono">{{ row.id }}</td>
            <td><input v-model="row.name" class="category-name" :maxlength="meta?.max_category_len" /></td>
            <td>{{ row.entry_count }}</td>
            <td>
              <div class="cell-actions">
                <button class="small" type="button" @click="renameCategory(row)">保存重命名</button>
                <button class="small" type="button" @click="deleteCategory(row)">删除</button>
              </div>
            </td>
          </tr>
          <tr v-if="!categories.length"><td colspan="4" class="empty">还没有分类，先添加一个再添加目录条目。</td></tr>
        </tbody>
      </table>
    </div>
  </div>

  <div class="card">
    <h2>导入、导出与远程预设</h2>
    <p class="card-sub">导出只含目录元数据（分类按名称匹配），不含 id、用户订阅、渠道密钥或投递状态；导入与远程预设都只写目录表，不会给任何用户创建订阅。</p>
    <div class="grid-3">
      <div>
        <h3 class="section-title">导出</h3>
        <p class="card-sub">把当前目录保存成 JSON，可用于备份、迁移或在老库里载入内置示例。</p>
        <button class="ghost" type="button" @click="exportCatalog">下载目录 JSON</button>
      </div>
      <div>
        <h3 class="section-title">导入</h3>
        <div class="form-row">
          <label>冲突策略
            <select v-model="importStrategy">
              <option value="skip">跳过（保留现有条目）</option>
              <option value="overwrite">覆盖（用文件里的信息更新）</option>
              <option value="error">报错（遇到重复则整体回滚）</option>
            </select>
          </label>
          <label>选择 JSON 文件
            <input ref="fileInput" type="file" accept=".json,application/json" @change="pickFile" />
          </label>
        </div>
        <div class="form-actions">
          <button class="primary" type="button" :disabled="busy" @click="importCatalog">开始导入</button>
          <span v-if="importFile" class="hint">{{ importFile.name }}</span>
        </div>
        <p v-if="importResult" class="hint">导入结果：{{ importResult }}</p>
      </div>
      <div>
        <h3 class="section-title">远程预设</h3>
        <p class="card-sub">从「全局设置」的地址拉取 manifest 与分类 JSON。只应用版本更高的条目；你手动编辑过的会标为「已保护」并保留。</p>
        <div class="form-actions">
          <button class="primary" type="button" :disabled="busy || syncing" @click="syncRemote">{{ syncing ? '同步中…' : '获取远程预设数据' }}</button>
        </div>
        <p class="hint">地址可在「全局设置 → presets.base_url」改成镜像；拉取失败时也可在左侧「导入」手动上传同一份 JSON。设置改动需重启进程生效。</p>
        <div v-if="syncResult" class="preset-summary">
          <p class="hint">新增 {{ syncResult.created }}、更新 {{ syncResult.updated }}、跳过 {{ syncResult.skipped }}、受保护 {{ syncResult.protected }}、认领 {{ syncResult.adopted }}。</p>
          <div class="table-wrap">
            <table>
              <thead><tr><th>文件</th><th>状态</th><th>新增</th><th>更新</th><th>跳过</th><th>保护</th></tr></thead>
              <tbody>
                <tr v-for="f in syncResult.files" :key="f.name">
                  <td class="mono">{{ f.name }}</td>
                  <td>
                    <span class="badge" :class="f.status === 'ok' ? 'badge-on' : 'badge-danger'">{{ f.status === 'ok' ? '成功' : '失败' }}</span>
                    <span v-if="f.reason" class="hint preset-reason" :title="f.reason">{{ f.reason }}</span>
                  </td>
                  <td>{{ f.created }}</td>
                  <td>{{ f.updated }}</td>
                  <td>{{ f.skipped }}</td>
                  <td>{{ f.protected }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../api'
import { formatTime } from '../time'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'
import type { CatalogCategory, CatalogEntry, CatalogMeta } from '../catalog'
import { fetchCatalog, useNotice } from '../catalogAdmin'

const { err, success, clear, clearError, fail, done } = useNotice()
const entries = ref<CatalogEntry[]>([])
const categories = ref<CatalogCategory[]>([])
const meta = ref<CatalogMeta | null>(null)
const busy = ref(false)
const uncategorized = computed(() => meta.value?.uncategorized_label ?? '未分类')

async function load(): Promise<void> {
  clearError()
  try {
    const catalog = await fetchCatalog()
    entries.value = catalog.entries
    categories.value = catalog.categories
    meta.value = catalog.meta
  } catch (e) {
    fail(e)
  }
}
onMounted(load)

// ---- directory entries: administrators edit directly, without review ----
function blankEntry() {
  return { name: '', url: '', category_id: 0, description: '', homepage: '', enabled: true }
}
const createDraft = ref(blankEntry())

async function createEntry(): Promise<void> {
  clear()
  if (!createDraft.value.category_id) {
    err.value = '请选择一个分类。'
    return
  }
  busy.value = true
  try {
    await api.post('/api/admin/catalog', createDraft.value)
    createDraft.value = blankEntry()
    done('订阅条目已添加。管理员的添加与编辑不经过审核，立即生效。')
    await load()
  } catch (e) {
    fail(e)
  } finally {
    busy.value = false
  }
}

const editing = ref<number | null>(null)
const editDraft = ref({ name: '', url: '', category_id: 0, description: '', homepage: '', enabled: true })

function startEdit(row: CatalogEntry): void {
  clear()
  editing.value = editing.value === row.id ? null : row.id
  editDraft.value = {
    name: row.name, url: row.url, category_id: row.category_id ?? 0,
    description: row.description, homepage: row.homepage, enabled: row.enabled,
  }
}

async function saveEdit(row: CatalogEntry): Promise<void> {
  clear()
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
    done(`「${row.name}」已更新。已订阅用户仍使用自己订阅里的链接，不受影响。`)
    await load()
  } catch (e) {
    fail(e)
  } finally {
    busy.value = false
  }
}

async function toggleEnabled(row: CatalogEntry): Promise<void> {
  clear()
  try {
    await api.patch(`/api/admin/catalog/${row.id}`, { enabled: !row.enabled })
    done(row.enabled
      ? `「${row.name}」已停用：不再出现在订阅中心，但仍占用该 URL，已订阅用户不受影响。`
      : `「${row.name}」已重新启用。`)
    await load()
  } catch (e) {
    fail(e)
  }
}

async function removeEntry(row: CatalogEntry): Promise<void> {
  if (!window.confirm(`删除订阅条目「${row.name}」？只会删除这个模板并释放该 URL，已经订阅的用户不受影响。`)) return
  clear()
  try {
    await api.del(`/api/admin/catalog/${row.id}`)
    done('订阅条目已删除。')
    await load()
  } catch (e) {
    fail(e)
  }
}

async function acceptRemote(row: CatalogEntry): Promise<void> {
  if (!window.confirm(
    `「${row.name}」已被你手动改过，处于「已保护」状态，远程同步会跳过它。解除保护后，下次同步若远程版本更高，会覆盖你的修改。继续？`,
  )) return
  clear()
  try {
    await api.post(`/api/admin/catalog/${row.id}/accept-remote`, {})
    done('已解除保护，恢复「未锁定」；下次同步若远程版本更高，会覆盖你的修改。')
    await load()
  } catch (e) {
    fail(e)
  }
}
</script>

<template>
  <PageHeader title="订阅条目" description="维护订阅中心里可用的链接模板（不是用户已订阅的记录）。管理员的添加与编辑立即生效、不经过审核；编辑、停用或删除都不会改动用户已经复制过去的订阅。">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="card">
    <h2>手动添加订阅条目</h2>
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
    <h2>订阅条目（{{ entries.length }}）</h2>
    <p class="card-sub">停用只是从订阅中心隐藏，仍占用该 URL；删除才会释放 URL。「预设状态」里的 vN 是本地已应用的预设版本，<strong>不代表远程已有新版本</strong>；「未锁定」表示没手动改过、允许被更高版本覆盖，「已保护」表示你改过、同步会跳过，可点「解除保护」恢复。</p>
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
                  <span v-else class="badge">未锁定</span>
                </span>
              </td>
              <td class="mono">{{ formatTime(row.updated_at) }}</td>
              <td>
                <div class="cell-actions">
                  <button class="small" type="button" @click="startEdit(row)">{{ editing === row.id ? '收起' : '编辑' }}</button>
                  <button class="small" type="button" @click="toggleEnabled(row)">{{ row.enabled ? '停用' : '启用' }}</button>
                  <button class="small" type="button" @click="removeEntry(row)">删除</button>
                  <button v-if="row.preset_key && row.preset_edited" class="small" type="button" @click="acceptRemote(row)">解除保护</button>
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
          <tr v-if="!entries.length"><td colspan="9" class="empty">订阅目录还是空的，可在上方手动添加，或在「导入导出」页载入备份、在「远程预设」页拉取预设。</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

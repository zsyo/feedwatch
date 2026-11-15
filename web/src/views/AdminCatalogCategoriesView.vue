<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'
import type { CatalogCategory, CatalogMeta } from '../catalog'
import { fetchCatalog, useNotice } from '../catalogAdmin'

const { err, success, clear, clearError, fail, done } = useNotice()
const categories = ref<CatalogCategory[]>([])
const meta = ref<CatalogMeta | null>(null)
const newCategory = ref('')
const uncategorized = computed(() => meta.value?.uncategorized_label ?? '未分类')
const description = computed(() =>
  `用户提交和管理员新增都必须选择分类。删除分类时，该分类下的目录条目与申请会自动改为「${uncategorized.value}」，条目本身不会被删除。`)

async function load(): Promise<void> {
  clearError()
  try {
    const catalog = await fetchCatalog()
    categories.value = catalog.categories
    meta.value = catalog.meta
  } catch (e) {
    fail(e)
  }
}
onMounted(load)

async function addCategory(): Promise<void> {
  clear()
  const name = newCategory.value.trim()
  if (!name) {
    err.value = '请填写分类名称。'
    return
  }
  try {
    await api.post('/api/admin/catalog/categories', { name })
    newCategory.value = ''
    done(`分类「${name}」已添加。`)
    await load()
  } catch (e) {
    fail(e)
  }
}

async function renameCategory(row: CatalogCategory): Promise<void> {
  clear()
  try {
    await api.patch(`/api/admin/catalog/categories/${row.id}`, { name: row.name })
    done('分类已重命名。')
    await load()
  } catch (e) {
    fail(e)
  }
}

async function deleteCategory(row: CatalogCategory): Promise<void> {
  if (!window.confirm(
    `删除分类「${row.name}」？该分类下的 ${row.entry_count} 个目录条目以及相关申请会统一改为「${uncategorized.value}」。`,
  )) return
  clear()
  try {
    const out = await api.del<{ ok: boolean; reassigned: number }>(`/api/admin/catalog/categories/${row.id}`)
    done(`已删除分类，${out.reassigned} 条记录改为「${uncategorized.value}」。`)
    await load()
  } catch (e) {
    fail(e)
  }
}
</script>

<template>
  <PageHeader title="分类管理" :description="description">
    <button class="ghost" type="button" @click="load">刷新</button>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="card">
    <h2>分类（{{ categories.length }}）</h2>
    <p class="card-sub">直接改名称后点「保存重命名」。目录条目数只统计启用与停用的模板，不代表实际订阅人数。</p>
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
          <tr v-if="!categories.length"><td colspan="4" class="empty">还没有分类，先添加一个再到「目录条目」页添加链接。</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

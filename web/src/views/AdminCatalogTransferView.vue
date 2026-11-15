<script setup lang="ts">
import { ref } from 'vue'
import { api } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'
import type { CatalogDocument, ImportResult } from '../catalogAdmin'
import { useNotice } from '../catalogAdmin'

const { err, success, clear, fail, done } = useNotice()
const importStrategy = ref<'skip' | 'overwrite' | 'error'>('skip')
const importFile = ref<File | null>(null)
const importResult = ref('')
const fileInput = ref<HTMLInputElement | null>(null)
const busy = ref(false)

async function exportCatalog(): Promise<void> {
  clear()
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
    done(`已导出 ${doc.entries.length} 个条目、${doc.categories.length} 个分类；文件不含订阅、渠道凭据或投递状态。`)
  } catch (e) {
    fail(e)
  }
}

function pickFile(event: Event): void {
  const input = event.target as HTMLInputElement
  importFile.value = input.files?.[0] ?? null
  importResult.value = ''
}

async function importCatalog(): Promise<void> {
  clear()
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
    done('导入完成。导入只影响目录，不会创建任何订阅。')
  } catch (e) {
    fail(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageHeader title="导入导出" description="只搬运目录元数据：分类按名称匹配，不含 id、用户订阅、渠道密钥或投递状态。导入与远程预设都只写目录表，不会给任何用户创建订阅。" />
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="card">
    <h2>目录数据</h2>
    <p class="card-sub">导出可用于备份、迁移，或在老库里载入内置示例；导入按冲突策略处理同名条目。</p>
    <div class="grid-2">
      <div>
        <h3 class="section-title">导出</h3>
        <p class="card-sub">把当前目录保存成 JSON 文件下载到本地。</p>
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
        <p class="hint">只接受本页「导出」生成的 feedwatch-catalog v1 文件；「远程预设」的分类 JSON 是另一种结构，不能直接在这里上传。</p>
      </div>
    </div>
  </div>
</template>

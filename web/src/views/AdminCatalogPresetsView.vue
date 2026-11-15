<script setup lang="ts">
import { ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api } from '../api'
import Notice from '../components/Notice.vue'
import PageHeader from '../components/PageHeader.vue'
import type { PresetSyncResult } from '../presets'
import { useNotice } from '../catalogAdmin'

const { err, success, clear, fail, done } = useNotice()
const syncing = ref(false)
const syncResult = ref<PresetSyncResult | null>(null)

async function syncRemote(): Promise<void> {
  clear()
  syncResult.value = null
  syncing.value = true
  try {
    const out = await api.post<PresetSyncResult>('/api/admin/catalog/presets/sync', {})
    syncResult.value = out
    done(`远程预设同步完成：新增 ${out.created}、更新 ${out.updated}、跳过 ${out.skipped}、受保护 ${out.protected}、认领 ${out.adopted}。`)
  } catch (e) {
    fail(e)
  } finally {
    syncing.value = false
  }
}
</script>

<template>
  <PageHeader title="远程预设" description="从「全局设置」配置的基址拉取 manifest 与分类 JSON，全量校验后在一个事务里写入。只应用版本更高的条目；本地缺失的会新增，你手动编辑过的会标为「已保护」并跳过。">
    <RouterLink to="/admin/catalog/entries" class="link-button">查看目录条目</RouterLink>
  </PageHeader>
  <Notice kind="error" :message="err" />
  <Notice kind="success" :message="success" />

  <div class="card">
    <h2>获取远程预设数据</h2>
    <p class="card-sub">任一文件拉取失败或校验不通过都会整体回滚，目录保持原样；下方按文件列出本次结果。同步只会改动目录表，不会给任何用户创建订阅。</p>
    <div class="form-actions">
      <button class="primary" type="button" :disabled="syncing" @click="syncRemote">
        {{ syncing ? '同步中…' : '获取远程预设数据' }}
      </button>
    </div>
    <p class="hint">地址可在「全局设置 → presets.base_url」改成可访问的镜像；设置改动需重启进程生效。离线兜底时，请把分类 JSON 整理成「导入导出」页使用的 feedwatch-catalog v1 格式再上传——两种 JSON 结构不同，不能直接导入。</p>

    <div v-if="syncResult" class="preset-summary">
      <h3 class="section-title">本次结果</h3>
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
      <p class="hint">同步后的版本号与保护状态在「目录条目」页的「预设状态」列查看；要解除保护也在那一页操作。</p>
    </div>
  </div>

  <div class="card">
    <h2>同步规则</h2>
    <p class="card-sub">预设文件由仓库维护，可能随时更新；下面的规则决定一次同步会动到哪些行。</p>
    <ul class="rule-list">
      <li>每条预设用稳定的 <code>preset_key</code> 标识，只有远程 <code>version</code> 大于本地版本才覆盖；相同版本或更旧版本一律跳过，不会把改过的配置倒退回去。</li>
      <li>管理员手动编辑过、或用户更新申请审核通过的条目会标为「已保护」，同步跳过；在「目录条目」页点「接受远程更新」可解除保护。</li>
      <li>本地已存在相同 URL 且从未编辑过的条目会被认领，随后按版本号正常升级，不会生成重复行。</li>
      <li>管理员删除过的预设会留下墓碑，之后的同步不会再把它加回来。</li>
      <li>所有文件先全量校验再一次性写入；任一文件失败或校验不通过，整批回滚，目录保持原样。</li>
    </ul>
  </div>
</template>

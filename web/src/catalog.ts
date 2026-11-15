// Shared types and small helpers for the subscription center. The backend is
// the single source of truth for field limits (GET /api/catalog -> meta), so
// this file deliberately contains no validation constants.

export interface CatalogCategory {
  id: number
  name: string
  entry_count: number
  created_at: string
  updated_at: string
}

export interface CatalogEntry {
  id: number
  name: string
  url: string
  url_key: string
  category_id: number | null
  category_name: string
  description: string
  homepage: string
  source_type: string
  enabled: boolean
  subscriber_count: number
  subscribed: boolean
  subscription_id: number | null
  created_at: string
  updated_at: string
  preset_key: string
  preset_version: number
  preset_edited: boolean
}

export interface CatalogSnapshot {
  name: string
  url: string
  url_key: string
  category_id: number | null
  category_name: string
  description: string
  homepage: string
  enabled: boolean
}

export interface CatalogSubmission {
  id: number
  kind: 'new' | 'update'
  status: 'pending' | 'approved' | 'rejected' | 'withdrawn'
  entry_id: number | null
  name: string
  url: string
  url_key: string
  category_id: number | null
  category_name: string
  description: string
  homepage: string
  before_json: CatalogSnapshot | null
  submitted_by: number | null
  submitted_by_name: string
  reject_reason: string
  reviewed_by: number | null
  reviewed_by_name: string
  reviewed_at: string
  created_at: string
  updated_at: string
}

export interface CatalogMeta {
  max_name_len: number
  max_description_len: number
  max_homepage_len: number
  max_url_len: number
  max_category_len: number
  max_pending_per_user: number
  uncategorized_label: string
}

export const kindLabel: Record<string, string> = {
  new: '新增申请',
  update: '更新申请',
}

export const statusLabel: Record<string, string> = {
  pending: '待审核',
  approved: '已通过',
  rejected: '已驳回',
  withdrawn: '已撤回',
}

export function statusBadge(status: string): string {
  switch (status) {
    case 'approved': return 'badge-on'
    case 'rejected': return 'badge-danger'
    case 'withdrawn': return 'badge-off'
    default: return 'badge-warn'
  }
}

// copyText is used by the "pending review" page, where copying is the only way
// to use an unreviewed link. Clipboard API first, legacy execCommand fallback
// for plain-HTTP origins where navigator.clipboard is unavailable.
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // fall through to the legacy path
  }
  try {
    const area = document.createElement('textarea')
    area.value = text
    area.style.position = 'fixed'
    area.style.opacity = '0'
    document.body.appendChild(area)
    area.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(area)
    return ok
  } catch {
    return false
  }
}

// Shared types, fetch helpers and navigation metadata for the admin catalog
// subpages. The backend is still the single source of truth for field limits
// (GET /api/admin/catalog -> meta); this file only describes response shapes.
import { ref } from 'vue'
import { api, errorMessage } from './api'
import type { CatalogCategory, CatalogEntry, CatalogMeta, CatalogSubmission } from './catalog'

export const catalogAdminPrefix = '/admin/catalog'
export const catalogAdminHome = '/admin/catalog/entries'

export interface CatalogAdminPage {
  to: string
  label: string
}

// One sidebar entry per subpage. App.vue renders these as an indented group
// only while the user is somewhere under /admin/catalog.
export const catalogAdminPages: CatalogAdminPage[] = [
  { to: '/admin/catalog/entries', label: '目录条目' },
  { to: '/admin/catalog/reviews', label: '审核队列' },
  { to: '/admin/catalog/categories', label: '分类管理' },
  { to: '/admin/catalog/transfer', label: '导入导出' },
  { to: '/admin/catalog/presets', label: '远程预设' },
]

export interface AdminCatalogResponse {
  entries: CatalogEntry[]
  categories: CatalogCategory[]
  pending_count: number
  meta: CatalogMeta
}

export interface ReviewResponse {
  submissions: CatalogSubmission[]
  categories: CatalogCategory[]
}

export interface ImportResult {
  ok: boolean
  created: number
  updated: number
  skipped: number
}

// Only metadata travels between installations. The backend validates every
// field again, so this shape is checked loosely here and strictly in Go.
export interface CatalogDocument {
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

export function fetchCatalog(): Promise<AdminCatalogResponse> {
  return api.get<AdminCatalogResponse>('/api/admin/catalog')
}

export function fetchReviews(status: 'pending' | 'all'): Promise<ReviewResponse> {
  return api.get<ReviewResponse>(`/api/admin/catalog/submissions?status=${status}`)
}

// Each subpage owns its own banner state, so a failed action on one page can
// never leave a stale message on another. Destructure the refs at the top of
// <script setup> to keep the template's automatic unwrapping intact.
export function useNotice() {
  const err = ref('')
  const success = ref('')
  return {
    err,
    success,
    clear(): void {
      err.value = ''
      success.value = ''
    },
    fail(e: unknown): void {
      success.value = ''
      err.value = errorMessage(e)
    },
    done(message: string): void {
      err.value = ''
      success.value = message
    },
  }
}

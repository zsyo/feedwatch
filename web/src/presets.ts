// Types for POST /api/admin/catalog/presets/sync. The endpoint is atomic: a
// failed document rolls the whole transaction back, so `files` on a successful
// response is always all-ok. On failure the server returns an error envelope
// (plus per-file states for API clients), and the page shows its message.

export interface PresetFileStatus {
  name: string
  status: 'ok' | 'failed'
  reason?: string
  created: number
  updated: number
  skipped: number
  protected: number
  adopted: number
}

export interface PresetSyncResult {
  ok: boolean
  created: number
  updated: number
  skipped: number
  protected: number
  adopted: number
  files: PresetFileStatus[]
  synced_at: string
}

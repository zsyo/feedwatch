// Display-only date formatting. The server always stores and returns RFC3339 in
// UTC; the zone a page shows it in belongs to the account, so every timestamp is
// formatted here against the current account's choice.
//
// Both refs are reactive on purpose: templates call formatTime during render, so
// changing the zone re-renders every list that shows a time.

import { ref } from 'vue'

// IANA name chosen by the account; empty means "follow the server".
export const timeZone = ref('')

// Server zone as reported by GET /api/meta. It is the default, not a substitute
// for the account setting: a user travelling to another device still sees the
// same times.
export const serverTimeZone = ref('')

export function setTimeZone(tz: string | null | undefined): void {
  timeZone.value = (tz ?? '').trim()
}

export function setServerTimeZone(tz: string | null | undefined): void {
  serverTimeZone.value = (tz ?? '').trim()
}

export interface FormatOptions {
  /** Append ".123". Off by default: these times mark whole events. */
  ms?: boolean
  /** Keep only the date part, "YYYY-MM-DD". */
  dateOnly?: boolean
}

const formatterCache = new Map<string, Intl.DateTimeFormat>()

function formatterFor(zone: string | undefined, dateOnly: boolean): Intl.DateTimeFormat {
  const key = `${zone ?? 'browser'}|${dateOnly ? 'date' : 'datetime'}`
  const cached = formatterCache.get(key)
  if (cached) return cached
  const options: Intl.DateTimeFormatOptions = dateOnly
    ? { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit' }
    : {
        timeZone: zone,
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        // h23 avoids the "24:00:00" that h24 can produce at midnight.
        hourCycle: 'h23',
      }
  const fmt = new Intl.DateTimeFormat('en-US', options)
  formatterCache.set(key, fmt)
  return fmt
}

// "Local" means the server could not name its zone (no TZ, no named
// /etc/localtime). Intl rejects it, so fall back to the browser's zone, which is
// almost always the same machine's setting for a self-hosted deployment.
function zoneArg(zone: string): string | undefined {
  if (!zone || zone === 'Local') return undefined
  return zone
}

function effectiveZone(): string | undefined {
  return zoneArg(timeZone.value || serverTimeZone.value)
}

function formatWith(zone: string | undefined, d: Date, opts: FormatOptions): string {
  const parts = formatterFor(zone, opts.dateOnly === true).formatToParts(d)
  const pick = (type: string): string => parts.find((p) => p.type === type)?.value ?? ''

  const date = `${pick('year')}-${pick('month')}-${pick('day')}`
  if (opts.dateOnly) return date
  let out = `${date} ${pick('hour')}:${pick('minute')}:${pick('second')}`
  if (opts.ms) {
    // Milliseconds are zone-independent, so they come from the Date directly.
    out += `.${String(d.getMilliseconds()).padStart(3, '0')}`
  }
  return out
}

/**
 * formatTime renders an RFC3339 timestamp as "2006-01-02 15:04:05" in the
 * account's zone. Empty values become an em dash and unparseable ones are passed
 * through untouched, so a malformed server value is never hidden.
 */
export function formatTime(value: string | null | undefined, opts: FormatOptions = {}): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return formatWith(effectiveZone(), d, opts)
}

/**
 * previewAt shows "now" in an arbitrary zone so the picker can prove what the
 * choice means before it is saved. "" and "Local" both mean the server default.
 */
export function previewAt(zone: string, at: Date = new Date()): string {
  return formatWith(zoneArg(zone || serverTimeZone.value), at, {})
}

/**
 * zoneLabel describes a zone for a picker: "Asia/Shanghai (UTC+08:00)". The
 * offset is computed for right now, which is what a reader expects to see.
 */
export function zoneLabel(zone: string, at: Date = new Date()): string {
  const offsetOf = (tz?: string): string => {
    const raw =
      new Intl.DateTimeFormat('en-US', { timeZone: tz, timeZoneName: 'longOffset' })
        .formatToParts(at)
        .find((p) => p.type === 'timeZoneName')?.value ?? 'UTC'
    // Intl spells it "GMT+08:00"; readers of this UI expect UTC.
    return raw.replace(/^GMT/, 'UTC')
  }
  if (zone === 'Local') return `服务器本地时区（${offsetOf()}）`
  return `${zone}（${offsetOf(zone)}）`
}

// The picker is driven by whatever zones the browser knows. Older engines do not
// implement supportedValuesOf, so a small hand-picked list keeps the feature
// usable there instead of showing an empty dropdown.
const fallbackZones = [
  'UTC',
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Asia/Singapore',
  'Asia/Kolkata',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Chicago',
  'America/Los_Angeles',
  'Australia/Sydney',
]

export function supportedZones(current: string): string[] {
  const all = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : fallbackZones
  // Whatever is already stored must stay selectable even if this browser does
  // not list it (e.g. a zone valid on the server but unknown here).
  if (current && current !== 'Local' && !all.includes(current)) return [current, ...all]
  return [...all]
}

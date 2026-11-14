import { computed, ref } from 'vue'
import { api, setCsrfToken } from './api'
import { setTimeZone } from './time'

export interface User {
  id: number
  username: string
  role: 'admin' | 'user'
  must_change_password: boolean
  /** IANA zone for display; empty means "follow the server". */
  timezone: string
  /** How a cycle's new releases are grouped: one digest, or one per project. */
  notify_mode: NotifyMode
}

/** digest = 汇总一条，per_feed = 每个项目一条。 */
export type NotifyMode = 'digest' | 'per_feed'

interface MeResponse {
  user: User
  csrf_token: string
  must_change_password: boolean
}

export const user = ref<User | null>(null)
export const mustChangePassword = ref(false)
export const ready = ref(false)
export const isAdmin = computed(() => user.value?.role === 'admin')

function apply(me: MeResponse): void {
  user.value = me.user
  mustChangePassword.value = me.must_change_password
  setCsrfToken(me.csrf_token)
  // The display zone travels with the account, so a new device picks it up here.
  setTimeZone(me.user.timezone)
}

function clear(): void {
  user.value = null
  mustChangePassword.value = false
  setCsrfToken('')
  setTimeZone('')
}

// expireSession is called by the API layer when a request comes back 401: the
// cookie is already gone, so drop the local state. Returns true when a user was
// signed in, which lets the caller avoid a redundant redirect.
export function expireSession(): boolean {
  const wasSignedIn = user.value !== null
  clear()
  return wasSignedIn
}

// A 401 on /me simply means "not logged in"; any other transport failure still
// leaves the app on the login screen instead of a blank page.
export async function bootstrap(): Promise<void> {
  try {
    apply(await api.get<MeResponse>('/api/auth/me'))
  } catch {
    clear()
  } finally {
    ready.value = true
  }
}

export async function login(username: string, password: string): Promise<void> {
  apply(await api.post<MeResponse>('/api/auth/login', { username, password }))
}

export async function logout(): Promise<void> {
  try {
    await api.post('/api/auth/logout')
  } finally {
    clear()
  }
}

// Changing the password invalidates every session, including the current one,
// so the caller must send the user back to the login screen.
export async function changePassword(oldPassword: string, newPassword: string): Promise<void> {
  await api.post('/api/auth/password', {
    old_password: oldPassword,
    new_password: newPassword,
  })
  clear()
}

// saveTimezone persists the display zone on the account and applies it to the
// current page immediately. Passing "" restores "follow the server".
export async function saveTimezone(tz: string): Promise<string> {
  const res = await api.patch<{ timezone: string }>('/api/me', { timezone: tz })
  if (user.value) user.value = { ...user.value, timezone: res.timezone }
  setTimeZone(res.timezone)
  return res.timezone
}

// saveNotifyMode stores the account-wide notification shape. It is account
// state, not per subscription, so one call covers every feed the user follows.
export async function saveNotifyMode(mode: NotifyMode): Promise<NotifyMode> {
  const res = await api.patch<{ notify_mode: NotifyMode }>('/api/me', { notify_mode: mode })
  if (user.value) user.value = { ...user.value, notify_mode: res.notify_mode }
  return res.notify_mode
}

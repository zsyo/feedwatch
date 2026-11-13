import { computed, ref } from 'vue'
import { api, setCsrfToken } from './api'

export interface User {
  id: number
  username: string
  role: 'admin' | 'user'
  must_change_password: boolean
}

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
}

function clear(): void {
  user.value = null
  mustChangePassword.value = false
  setCsrfToken('')
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

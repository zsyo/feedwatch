import { ref } from 'vue'
import { api } from './api'

/** Sign-in capabilities the login and sign-up pages need before anyone is in. */
export interface AuthMeta {
  /** True when the site accepts new accounts, by form or by provider. */
  registration_enabled: boolean
  /** provider key → configured and enabled. Only true providers are offered. */
  oauth: Record<string, boolean>
}

/** The provider keys the server knows, in the order it lists them. */
export const oauthProviderLabels: Record<string, string> = {
  github: 'GitHub',
  linuxdo: 'linux.do',
}

export const authMeta = ref<AuthMeta>({ registration_enabled: false, oauth: {} })

let pending: Promise<AuthMeta> | null = null

// loadAuthMeta caches the answer for the page load: both /login and /register
// ask for it, and the values only change when an administrator edits them.
export function loadAuthMeta(): Promise<AuthMeta> {
  if (!pending) {
    pending = api
      .get<{ registration_enabled?: boolean; oauth?: Record<string, boolean> }>('/api/meta')
      .then((meta) => {
        authMeta.value = {
          registration_enabled: meta.registration_enabled === true,
          oauth: meta.oauth ?? {},
        }
        return authMeta.value
      })
      .catch(() => {
        // A failed probe must not stick: the next mount retries.
        pending = null
        return authMeta.value
      })
  }
  return pending
}

import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { api, setUnauthorizedHandler } from './api'
import { expireSession } from './session'
import { setServerTimeZone } from './time'
import './style.css'

// A session that expires mid-session must not leave the page showing stale data
// next to a "login required" error: clear it and bounce to the login screen.
setUnauthorizedHandler(() => {
  if (!expireSession()) return
  const current = router.currentRoute.value
  if (current.name === 'login') return
  void router.replace({ name: 'login', query: { redirect: current.fullPath } })
})

// The server's zone is the default for accounts that never picked one, so learn
// it before the first list renders. The ref is reactive: when this resolves late
// the timestamps simply re-render. A failure is cosmetic (browser zone shows).
void api
  .get<{ server_timezone?: string }>('/api/meta')
  .then((meta) => setServerTimeZone(meta.server_timezone ?? ''))
  .catch(() => {})

createApp(App).use(router).mount('#app')

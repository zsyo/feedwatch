import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { setUnauthorizedHandler } from './api'
import { expireSession } from './session'
import './style.css'

// A session that expires mid-session must not leave the page showing stale data
// next to a "login required" error: clear it and bounce to the login screen.
setUnauthorizedHandler(() => {
  if (!expireSession()) return
  const current = router.currentRoute.value
  if (current.name === 'login') return
  void router.replace({ name: 'login', query: { redirect: current.fullPath } })
})

createApp(App).use(router).mount('#app')

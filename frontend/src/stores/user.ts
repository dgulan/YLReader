import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { getSession, setSession, sessionEvents, request, jsonBody } from '@/api/client'
import { api } from '@/api/reader'
import { clearUserCache } from '@/db/chapters'
export const useUser = defineStore('user', () => {
  const user = ref(getSession()?.user ?? null)
  sessionEvents.addEventListener('change', () => { user.value = getSession()?.user ?? null })
  const signedIn = computed(() => user.value !== null)
  async function login(username: string, password: string) { setSession(await api.login(username, password)) }
  async function logout() {
    const prior = getSession()
    // Remove credentials immediately even if the logout endpoint is unavailable.
    setSession(null)
    if (prior) {
      try { await request('/auth/logout', { method: 'POST', ...jsonBody({ refresh_token: prior.refresh_token }), headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${prior.access_token}` } }) } catch { /* Local sign-out still succeeds. */ }
    }
    if (prior) await clearUserCache(prior.user.id).catch(() => undefined)
  }
  return { user, signedIn, login, logout }
})

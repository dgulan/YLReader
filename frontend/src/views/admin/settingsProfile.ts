import { onScopeDispose, ref } from 'vue'
import { api } from '@/api/reader'
import { errorText, getSession, type Model } from '@/api/client'

export function useAccountProfile() {
  const profile = ref<Model<'User'>>()
  const loading = ref(false)
  const error = ref('')

  let generation = 0
  let controller = new AbortController()

  function stop() {
    generation++
    controller.abort()
    controller = new AbortController()
    loading.value = false
  }

  onScopeDispose(stop)

  async function load() {
    const owner = getSession()?.user.id
    if (!owner) {
      stop()
      profile.value = undefined
      return
    }
    stop()
    const token = generation
    const signal = controller.signal
    loading.value = true
    error.value = ''
    try {
      const me = await api.me(signal)
      if (!signal.aborted && token === generation && getSession()?.user.id === owner) {
        profile.value = me
      }
    } catch (e) {
      if (!signal.aborted && token === generation && getSession()?.user.id === owner) {
        error.value = errorText(e)
      }
    } finally {
      if (token === generation) {
        loading.value = false
      }
    }
  }

  return { profile, loading, error, load }
}

import { computed, onScopeDispose, ref, toRaw, watch, type Ref } from 'vue'
import { api } from '@/api/reader'
import { ApiError, errorText, getSession, type Model } from '@/api/client'
import { db, bookKey } from '@/db/chapters'
export const jobLabels: Record<Model<'Job'>['status'], string> = { queued: '排队中', running: '正在缓存', succeeded: '已完成', failed: '失败', canceled: '已取消' }
export const isActiveJob = (job?: Model<'Job'>) => job?.status === 'queued' || job?.status === 'running'
export function cacheRequest(first: number, last: number, total: number, source: string): Model<'CacheBookRequest'> {
  if (!source || !Number.isInteger(first) || !Number.isInteger(last) || first < 1 || last < first || last > total) throw new Error('请选择有效书源和章节范围，起始章不能晚于结束章')
  return { source_id: source, from_chapter: first - 1, to_chapter: last - 1 }
}
export function useCacheTask(bookId: Ref<string>) {
  const owner = getSession()!.user.id
  const job = ref<Model<'Job'>>(), busy = ref(false), error = ref(''), notice = ref(''), missing = ref(false)
  const active = computed(() => !missing.value && isActiveJob(job.value))
  let generation = 0, timer: ReturnType<typeof setTimeout> | undefined
  const valid = (token: number) => generation === token && getSession()?.user.id === owner
  function stop() { clearTimeout(timer) }
  function reset() { stop(); generation++; job.value = undefined; busy.value = false; error.value = ''; notice.value = ''; missing.value = false }
  watch(bookId, reset, { flush: 'sync' }); onScopeDispose(reset)
  async function remember(token: number) {
    if (!valid(token) || !job.value) return
    try { await db.jobs.put({ key: bookKey(owner, bookId.value), owner, bookId: bookId.value, job: toRaw(job.value) }) }
    catch { if (valid(token)) notice.value = '任务已提交，但本机记录保存失败，请记下任务编号' }
  }
  function schedule() { stop(); if (active.value && !error.value) timer = setTimeout(() => { void refresh() }, 2000) }
  async function refresh() {
    if (!job.value || busy.value) return
    const token = generation, id = job.value.id
    stop(); busy.value = true; error.value = ''
    try { const result = await api.job(id); if (!valid(token)) return; job.value = result; missing.value = false; await remember(token) }
    catch (e) { if (valid(token)) { missing.value = e instanceof ApiError && e.status === 404; error.value = missing.value ? '任务已不存在，请核对记录或重新提交' : e instanceof TypeError ? '暂时无法连接，已暂停刷新；联网后请手动刷新任务' : errorText(e) } }
    finally { if (valid(token)) { busy.value = false; schedule() } }
  }
  async function load() {
    reset(); const token = generation; busy.value = true
    try { const row = await db.jobs.get(bookKey(owner, bookId.value)); if (!valid(token)) return; job.value = row?.job }
    catch { if (valid(token)) notice.value = '无法读取本机任务记录，新任务仍可提交' }
    finally { if (valid(token)) busy.value = false }
    if (valid(token) && job.value) await refresh()
  }
  async function submit(data: Model<'CacheBookRequest'>) {
    if (busy.value || active.value) return
    const token = generation
    stop(); busy.value = true; error.value = ''; notice.value = ''
    try { const result = await api.cacheBook(bookId.value, data); if (!valid(token)) return; job.value = result; missing.value = false; await remember(token) }
    catch (e) { if (valid(token)) error.value = e instanceof ApiError && e.status === 409 ? '该书源已有进行中的任务，请稍后重试；已有本机记录可刷新查看' : e instanceof TypeError ? '请求结果尚未确认，重新提交可能提示任务已存在' : errorText(e) }
    finally { if (valid(token)) { busy.value = false; schedule() } }
  }
  async function cancel() {
    if (!job.value || !active.value || busy.value) return
    const token = generation, id = job.value.id
    stop(); busy.value = true; error.value = ''; notice.value = ''
    try { await api.cancelJob(id); if (valid(token)) notice.value = '取消请求已发送，正在核对任务状态' }
    catch (e) { if (valid(token)) { if (e instanceof ApiError && e.status === 409) notice.value = '任务状态已变化，正在读取最新状态'; else { error.value = errorText(e); return } } }
    finally { if (valid(token)) busy.value = false }
    if (valid(token)) await refresh()
  }
  return { job, busy, error, notice, missing, active, load, refresh, submit, cancel, stop }
}

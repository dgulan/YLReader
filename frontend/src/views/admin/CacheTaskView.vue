<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ElAlert, ElButton, ElForm, ElFormItem, ElInputNumber, ElSelectV2, ElProgress, ElMessageBox } from 'element-plus'
import { api } from '@/api/reader'
import { errorText, type Model } from '@/api/client'
import { cacheRequest, jobLabels, useCacheTask } from './cacheTask'
const route = useRoute(), bookId = computed(() => String(route.params.bookId)), tracker = useCacheTask(bookId)
const { job, busy, active, error, notice, missing } = tracker
const book = ref<Model<'Book'>>(), mappings = ref<Model<'BookSourceMapping'>[]>([]), source = ref(''), total = ref(0), first = ref<number | undefined>(1), last = ref<number | undefined>(1), loading = ref(false), directoryLoading = ref(false), problem = ref('')
const formDisabled = computed(() => loading.value || busy.value || active.value)
let generation = 0, directoryGeneration = 0
const options = computed(() => mappings.value.map(m => ({ value: m.source_id, label: m.source_name || m.book_url })))
const sourceName = computed(() => mappings.value.find(m => m.source_id === job.value?.target_source_id)?.source_name || job.value?.target_source_id || '未知书源')
async function directory() {
  const token = ++directoryGeneration, id = bookId.value, target = source.value
  total.value = 0; problem.value = ''; directoryLoading.value = true
  try { const toc = await api.chapters(id, false, target); if (token !== directoryGeneration) return; total.value = toc.total; first.value = 1; last.value = toc.total; if (!toc.total) problem.value = '目标书源没有可读章节，请先同步目录' }
  catch (e) { if (token === directoryGeneration) problem.value = errorText(e) }
  finally { if (token === directoryGeneration) directoryLoading.value = false }
}
async function boot() {
  const token = ++generation; directoryGeneration++; book.value = undefined; mappings.value = []; source.value = ''; total.value = 0; problem.value = ''; loading.value = true
  void tracker.load()
  try {
    const value = await api.book(bookId.value); if (token !== generation) return; book.value = value
    if (value.book_type === 'local') return
    const list = await api.mappings(bookId.value); if (token !== generation) return
    mappings.value = list
    source.value = list.find(m => m.source_id === value.active_source_id)?.source_id || list[0]?.source_id || ''
    if (source.value) await directory()
  } catch (e) { if (token === generation) problem.value = errorText(e) }
  finally { if (token === generation) loading.value = false }
}
async function submit() { try { const request = cacheRequest(first.value ?? NaN, last.value ?? NaN, total.value, source.value); problem.value = ''; await tracker.submit(request) } catch (e) { problem.value = errorText(e) } }
async function cancel() { try { await ElMessageBox.confirm('取消剩余章节的缓存？已完成的服务端缓存将保留。', '取消缓存任务', { confirmButtonText: '确认取消任务', cancelButtonText: '继续缓存', type: 'warning' }); await tracker.cancel() } catch { /* Canceled. */ } }
watch(bookId, boot, { immediate: true })
onBeforeUnmount(() => { generation++; directoryGeneration++; tracker.stop() })
</script>
<template>
  <main class="page-width"><RouterLink to="/bookshelf" class="muted">← 返回书架</RouterLink><div class="page-heading"><div><span class="eyebrow">提前准备下一段旅程</span><h1>后台缓存<span class="heading-dot">.</span></h1><p>{{ book?.name || '正在打开书籍…' }}</p><p class="muted">将在线章节预热到服务端，便于后续读取。此操作不会把整本书下载到浏览器，离线阅读仍使用已打开章节的本机缓存。</p></div></div>
    <ElAlert v-if="problem || error" :title="problem || error" type="error" :closable="false" /><ElAlert v-if="notice" :title="notice" type="info" :closable="false" />
    <section class="surface"><h2>新建缓存任务</h2><p v-if="book?.book_type === 'local'">本地书无需后台网络缓存，可直接阅读。</p><template v-else><ElForm label-position="top" :disabled="formDisabled"><ElFormItem label="目标书源"><ElSelectV2 v-model="source" aria-label="缓存书源" :options="options" :disabled="formDisabled || directoryLoading" @change="directory" /></ElFormItem><p v-if="!loading && !options.length">没有已绑定的在线书源，请先从书源搜索并加入本书。</p><p class="muted">{{ directoryLoading ? '正在读取目标目录…' : `目标目录 ${total} 章，以下章节编号从 1 开始。` }}</p><ElFormItem label="起始章"><ElInputNumber v-model="first" aria-label="缓存起始章" :disabled="formDisabled" :min="1" :max="Math.max(1,total)" :precision="0" /></ElFormItem><ElFormItem label="结束章"><ElInputNumber v-model="last" aria-label="缓存结束章" :disabled="formDisabled" :min="1" :max="Math.max(1,total)" :precision="0" /></ElFormItem><ElButton type="primary" :disabled="formDisabled || !total || directoryLoading || !book" :loading="busy" @click="submit">提交后台缓存</ElButton></ElForm><ElButton :disabled="!source || loading || directoryLoading || busy" @click="directory">重新读取目标目录</ElButton></template></section>
    <section class="surface" aria-label="缓存任务状态"><div class="section-heading"><h2>本机最近任务</h2><ElButton :disabled="!job || busy" @click="tracker.refresh">刷新任务</ElButton></div><p class="muted">保留本账户在此设备为本书提交的最近一次任务。页面关闭后服务端任务仍会继续；返回页面时读取最新状态。</p><template v-if="job"><p class="job-id">任务编号：{{ job.id }}</p><p>书源：{{ sourceName }}</p><p role="status">{{ missing ? '任务记录不可用' : jobLabels[job.status] }}</p><ElProgress :percentage="Math.max(0,Math.min(100,job.progress))" /><p v-if="job.error_code || job.error_message" class="error-note">{{ job.error_code }} {{ job.error_message }}</p><p v-if="job.result">成功 {{ job.result.succeeded ?? '—' }} / {{ job.result.total ?? '—' }} 章；已尝试 {{ job.result.attempted ?? '—' }} 章</p><ElButton v-if="active" :disabled="busy" type="danger" plain @click="cancel">取消缓存任务</ElButton></template><p v-else>本机尚无任务记录。</p></section>
  </main>
</template>
<style scoped>
.el-select-v2 { width: min(100%,480px); }h2 { font-size: 18px; font-weight: 500; }.job-id { overflow-wrap: anywhere; font-size: 13px; }.el-form { margin-bottom: 16px; }.error-note { overflow-wrap: anywhere; }
</style>

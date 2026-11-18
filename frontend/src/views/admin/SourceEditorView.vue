<script setup lang="ts">
import 'element-plus/dist/index.css'
import { computed, h, ref, watch } from 'vue'
import { useRoute, useRouter, onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { ElButton, ElInput, ElAlert, ElForm, ElFormItem, ElSwitch, ElSelect, ElOption, ElMessageBox, ElTableV2, ElAutoResizer } from 'element-plus'
import { api } from '@/api/reader'
import { ApiError, errorText, type Model } from '@/api/client'
import { sourcePatch } from './sourceDraft'
const route = useRoute(), router = useRouter(), source = ref<Model<'Source'>>()
const name = ref(''), group = ref(''), enabled = ref(true), rules = ref('{}'), header = ref('{}'), error = ref(''), notice = ref(''), busy = ref(false), stale = ref(false)
const ruleType = ref<Model<'DebugRequest'>['rule_type']>('content'), rule = ref(''), input = ref(''), url = ref(''), debug = ref<Model<'DebugResult'>>(), debugError = ref(''), debugging = ref(false)
const sourceId = computed(() => String(route.params.sourceId)), readonly = computed(() => source.value?.book_source_type === 3)
const draftSignature = () => JSON.stringify([name.value, group.value, enabled.value, rules.value, header.value])
const savedDraft = ref('')
let deleted = false
async function confirmLeave() {
  if (deleted || !source.value || readonly.value || draftSignature() === savedDraft.value) return true
  if (busy.value) return false
  try { await ElMessageBox.confirm('书源修改尚未保存，离开会丢弃草稿。', '未保存的书源', { confirmButtonText: '丢弃草稿并离开', cancelButtonText: '继续编辑' }); return true } catch { return false }
}
onBeforeRouteLeave(confirmLeave)
onBeforeRouteUpdate(confirmLeave)
const columns = [{ key: 'index', dataKey: 'index', title: '步骤', width: 70 }, { key: 'mode', dataKey: 'mode', title: '模式', width: 85 }, { key: 'expr', dataKey: 'expr', title: '表达式', width: 260 }, { key: 'output_preview', dataKey: 'output_preview', title: '输出预览', width: 300 }, { key: 'error_code', dataKey: 'error_code', title: '错误', width: 180 }]
async function load() { busy.value = true; try { const s = await api.source(sourceId.value); source.value = s; name.value = s.book_source_name; group.value = s.book_source_group; enabled.value = s.enabled; rules.value = JSON.stringify(s.rules, null, 2); header.value = JSON.stringify(s.header ?? {}, null, 2); savedDraft.value = draftSignature(); stale.value = false; error.value = '' } catch (e) { error.value = errorText(e) } finally { busy.value = false } }
async function syncVersionKeepDraft() {
  if (!source.value || readonly.value) return
  busy.value = true
  try {
    const latest = await api.source(sourceId.value)
    source.value = latest
    stale.value = false
    error.value = ''
    notice.value = `已同步服务端版本 v${latest.version}（保留当前草稿，可重新保存）`
  } catch (e) { error.value = errorText(e) }
  finally { busy.value = false }
}
async function reload() { try { await ElMessageBox.confirm('重新加载会丢弃当前未保存的修改。', '重新加载书源', { confirmButtonText: '丢弃草稿并重新加载', cancelButtonText: '保留草稿' }); await load() } catch { /* Canceled. */ } }
async function save() { if (!source.value || readonly.value) return; busy.value = true; error.value = ''; notice.value = ''; try { const patch = sourcePatch(source.value.version, name.value, group.value, enabled.value, rules.value, header.value); source.value = await api.updateSource(sourceId.value, patch); savedDraft.value = draftSignature(); notice.value = '书源已保存'; stale.value = false } catch (e) { stale.value = e instanceof ApiError && e.status === 409; error.value = stale.value ? '书源已被其他操作修改，草稿已保留；请重新加载后合并修改' : errorText(e) } finally { busy.value = false } }
async function remove() { if (!source.value || readonly.value) return; try { await ElMessageBox.confirm(`删除《${source.value.book_source_name}》后，关联的在线书可能需要重新选源。`, '删除书源', { confirmButtonText: '确认删除', cancelButtonText: '取消', type: 'warning' }) } catch { return }; busy.value = true; try { await api.deleteSource(sourceId.value); deleted = true; await router.push('/admin') } catch (e) { error.value = errorText(e) } finally { busy.value = false } }
async function runDebug() { if (!rule.value.trim()) { debugError.value = '请输入规则'; return }; if (!input.value.trim() && !url.value.trim()) { debugError.value = '请提供输入样本或请求 URL'; return }; debugging.value = true; debugError.value = ''; debug.value = undefined; try { debug.value = await api.debugSource(sourceId.value, { rule_type: ruleType.value, rule: rule.value, ...(url.value.trim() ? { url: url.value.trim() } : {}), variables: input.value ? { html: input.value } : {}, max_preview_bytes: 4096 }) } catch (e) { debugError.value = errorText(e) } finally { debugging.value = false } }
watch(sourceId, () => { debug.value = undefined; void load() }, { immediate: true })
</script>
<template>
  <main class="page-width"><RouterLink to="/admin">← 书源列表</RouterLink><div class="page-heading"><div><span class="eyebrow">书源工作台</span><h1>{{ source?.book_source_name ?? '加载书源' }}</h1></div></div>
    <ElAlert v-if="error" :title="error" type="error" :closable="false" /><div v-if="stale" class="button-row"><ElButton :disabled="busy" @click="syncVersionKeepDraft">同步最新版本号（保留草稿）</ElButton><ElButton :disabled="busy" @click="reload">重新加载服务端配置</ElButton></div><ElAlert v-if="notice" :title="notice" type="success" /><ElAlert v-if="readonly" title="本地书库虚拟源不可在此修改" type="info" />
    <p v-if="source" class="muted">健康度：失败 {{ source.failure_count ?? 0 }} 次 · 平均响应 {{ source.respond_time ?? 0 }} ms · 版本 v{{ source.version }}</p>
    <section class="surface"><h2>编辑书源</h2><ElForm label-position="top" :disabled="busy || readonly || !source" @submit.prevent="save"><ElFormItem label="名称"><ElInput v-model="name" aria-label="书源名称" maxlength="128" /></ElFormItem><ElFormItem label="分组"><ElInput v-model="group" aria-label="书源分组" maxlength="64" /></ElFormItem><ElFormItem label="地址"><ElInput :model-value="source?.book_source_url" disabled /></ElFormItem><ElFormItem label="启用"><ElSwitch v-model="enabled" aria-label="启用书源" /></ElFormItem><ElFormItem label="解析规则 JSON"><ElInput v-model="rules" aria-label="解析规则 JSON" type="textarea" :rows="10" /></ElFormItem><ElFormItem label="请求头 JSON"><ElInput v-model="header" aria-label="请求头 JSON" type="textarea" :rows="3" /></ElFormItem><ElButton type="primary" native-type="submit" :disabled="stale">保存修改</ElButton><ElButton native-type="button" @click="reload">重新加载</ElButton><ElButton type="danger" plain native-type="button" @click="remove">删除书源</ElButton></ElForm></section>
    <section class="surface"><h2>规则调试</h2><p class="muted">使用已保存书源。填入样本时直接解析样本；没有样本时才请求指定 URL。</p><ElAlert v-if="debugError" :title="debugError" type="error" :closable="false" /><ElForm label-position="top"><ElFormItem label="规则类型"><ElSelect v-model="ruleType" aria-label="规则类型"><ElOption v-for="kind in ['search','explore','book_info','toc','content']" :key="kind" :value="kind" :label="kind" /></ElSelect></ElFormItem><ElFormItem label="规则"><ElInput v-model="rule" aria-label="调试规则" placeholder="例如 h1@text" /></ElFormItem><ElFormItem label="输入样本"><ElInput v-model="input" aria-label="输入样本" type="textarea" :rows="5" placeholder="粘贴 HTML、JSON 或文本" /></ElFormItem><ElFormItem label="请求 URL（无样本时使用）"><ElInput v-model="url" aria-label="调试 URL" /></ElFormItem><ElButton type="primary" :loading="debugging" :disabled="!source || readonly" @click="runDebug">运行调试</ElButton></ElForm><template v-if="debug"><p class="muted">Trace {{ debug.trace_id }} · {{ debug.total_elapsed_ms }} ms</p><ElAlert v-if="debug.steps.some(step => step.error_code)" title="规则执行中有失败步骤，请检查下方追踪" type="warning" :closable="false" /><pre class="debug-output">{{ debug.result_preview ?? '（无输出）' }}</pre><div style="height: 300px"><ElAutoResizer><template #default="{ height, width }"><ElTableV2 :columns="columns" :data="debug.steps" :height="height" :width="width" row-key="index" /></template></ElAutoResizer></div></template></section>
  </main>
</template>
<style scoped>.debug-output { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 240px; overflow: auto; background: #eef1e9; padding: 16px; border-radius: 6px; }</style>

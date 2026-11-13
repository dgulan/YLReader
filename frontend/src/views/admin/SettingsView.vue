<script setup lang="ts">
import { ElAlert, ElButton, ElForm, ElFormItem, ElInputNumber, ElOption, ElSelect, ElMessageBox } from 'element-plus'
import { useReaderConfig } from '@/stores/readerConfig'
const config = useReaderConfig()
async function reset() { try { await ElMessageBox.confirm('恢复默认主题和字号？其他设置保持原样。', '恢复阅读默认设置', { confirmButtonText: '恢复默认', cancelButtonText: '取消', type: 'warning' }); config.restoreDefaults() } catch { /* Canceled. */ } }
</script>
<template>
  <main class="page-width"><div class="page-heading"><div><span class="eyebrow">舒适阅读，从这里开始</span><h1>阅读设置<span class="heading-dot">.</span></h1><p class="muted">主题和字号按账户保存。修改后自动同步，重新打开仍可沿用。</p></div></div>
    <p><RouterLink to="/settings/replace-rules">管理替换净化规则 →</RouterLink></p>
    <ElAlert v-if="config.error" :title="config.error" type="warning" :closable="false" />
    <section class="surface"><div class="section-heading"><h2>阅读外观</h2><ElButton :disabled="config.loading || config.saving || config.dirty" @click="config.reload">重新读取设置</ElButton></div>
      <ElForm label-position="top" :disabled="config.loading"><ElFormItem label="阅读主题"><ElSelect :model-value="config.theme" aria-label="阅读主题" @update:model-value="config.setTheme"><ElOption label="日间" value="day" /><ElOption label="夜间" value="night" /><ElOption label="羊皮纸" value="paper" /><ElOption label="护眼" value="green" /></ElSelect></ElFormItem><ElFormItem label="正文字号"><ElInputNumber :model-value="config.fontSize" aria-label="正文字号" :min="14" :max="32" :step="1" :precision="0" @update:model-value="value => value != null && config.setFont(value)" /></ElFormItem></ElForm>
      <p role="status" class="muted">{{ config.loading ? '正在读取设置…' : config.saving ? '正在同步设置…' : config.dirty ? '设置已保存在本机，等待同步' : config.error ? '暂未确认云端设置' : '阅读设置已同步' }}</p><div class="button-row"><ElButton v-if="config.error || config.dirty" :disabled="config.loading || config.saving" @click="config.retry">重试设置同步</ElButton><ElButton :disabled="config.loading || config.saving" @click="reset">恢复默认阅读设置</ElButton></div>
    </section><section class="appearance-preview" :data-theme="config.theme" aria-label="阅读外观预览"><p>外观预览</p><h2>山间来信</h2><p :style="{ fontSize: `${config.fontSize}px` }">翻开书页，让文字按照你喜欢的方式呈现。风穿过山林，故事在下一页继续。</p></section>
  </main>
</template>
<style scoped>
.el-select { width: min(100%,320px); }.appearance-preview { border: 1px solid #8884; border-radius: 8px; padding: 28px; background: #f2ead7; color: #423c30; line-height: 2; font-family: 'Noto Serif SC',SimSun,serif; }.appearance-preview[data-theme=day] { background: #fbfbf7; color: #303930; }.appearance-preview[data-theme=night] { background: #1d2422; color: #c6cfc7; }.appearance-preview[data-theme=green] { background: #dce8d8; color: #334831; }.appearance-preview h2 { font-size: 26px; font-weight: 500; }
</style>

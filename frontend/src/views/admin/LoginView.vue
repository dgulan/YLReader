<script setup lang="ts">
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElForm, ElFormItem, ElInput, ElButton, ElAlert } from 'element-plus'
import { useUser } from '@/stores/user'
import { errorText, ApiError } from '@/api/client'
const user = useUser(), router = useRouter(), route = useRoute()
const username = ref(''), password = ref(''), busy = ref(false), error = ref('')
async function submit() {
  if (!username.value.trim() || !password.value) { error.value = '请输入用户名和密码'; return }
  busy.value = true; error.value = ''
  try { await user.login(username.value.trim(), password.value); const target = String(route.query.redirect ?? '/bookshelf'); await router.replace(target.startsWith('/') && !target.startsWith('//') && !target.startsWith('/login') ? target : '/bookshelf') }
  catch (e) { error.value = e instanceof ApiError && e.status === 401 ? '用户名或密码不正确' : errorText(e) } finally { busy.value = false }
}
</script>
<template>
  <main class="login-layout">
    <section class="login-intro"><span class="eyebrow">YREADER · 私人书房</span><h1>把时间，<br>留给好故事。</h1><p>你的书架、你的书源，<br>还有随时可以继续的一页。</p></section>
    <section class="login-panel"><span class="eyebrow">欢迎回来</span><h2>走进你的书房</h2><p class="muted">使用自托管实例的账户登录</p>
      <ElAlert v-if="error" :title="error" type="error" :closable="false" show-icon />
      <ElForm label-position="top" @submit.prevent="submit">
        <ElFormItem label="用户名"><ElInput v-model="username" aria-label="用户名" autocomplete="username" /></ElFormItem>
        <ElFormItem label="密码"><ElInput v-model="password" aria-label="密码" type="password" show-password autocomplete="current-password" @keyup.enter="submit" /></ElFormItem>
        <ElButton type="primary" native-type="submit" :loading="busy" class="login-submit">登录书房</ElButton>
      </ElForm>
    </section>
  </main>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useUser } from '@/stores/user'
import { useReaderConfig } from '@/stores/readerConfig'
const route = useRoute(), router = useRouter(), user = useUser()
const readerConfig = useReaderConfig()
watch(() => user.user?.id, id => { if (id) void readerConfig.initialize(id) }, { immediate: true })
const shell = computed(() => route.name !== 'reader' && route.name !== 'login')
watch(() => user.signedIn, (signedIn) => { if (!signedIn && route.name !== 'login') void router.replace('/login') })
async function logout() { await user.logout(); await router.replace('/login') }
</script>
<template>
  <header v-if="shell" class="site-header"><RouterLink class="brand" to="/bookshelf"><span class="brand-mark">阅</span> YReader</RouterLink><nav aria-label="主导航"><RouterLink to="/bookshelf">书架</RouterLink><RouterLink to="/admin">书源</RouterLink><RouterLink to="/settings">设置</RouterLink></nav><div class="account"><span>{{ user.user?.display_name || user.user?.username }}</span><button class="text-button" @click="logout">退出</button></div></header>
  <RouterView />
</template>

<script setup lang="ts">
const title = 'YReader'
</script>

<template>
  <main class="app">
    <h1>{{ title }}</h1>
    <router-view />
  </main>
</template>

<style scoped>
.app {
  font-family: system-ui, -apple-system, sans-serif;
  padding: 2rem;
}
</style>

<script setup lang="ts">
import { onMounted } from 'vue'
import { useLiveStore } from './stores/live'
import { useUiPrefs } from './stores/uiPrefs'

const live = useLiveStore()
const prefs = useUiPrefs()

onMounted(() => {
  prefs.load()
  void live.load()
})
</script>

<template>
  <div class="dh-shell">
    <main class="dh-main">
      <router-view />
    </main>
  </div>
</template>

<style scoped>
.dh-shell {
  display: flex;
  flex-direction: column;
  height: 100%;
}
/* 页面级 .page 自带 padding，这里不再叠加，避免双层留白。 */
.dh-main {
  flex: 1;
  min-height: 0;
  overflow: auto;
}
</style>

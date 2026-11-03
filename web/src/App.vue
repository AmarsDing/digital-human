<script setup lang="ts">
import { computed, onMounted } from 'vue'
import NtStatusPill from './components/ui/NtStatusPill.vue'
import { useLiveStore } from './stores/live'
import { useEventsStore } from './stores/events'
import { useUiPrefs } from './stores/uiPrefs'

// 顶层只保留一层头部：状态由 live/events store 供给（原 stage.js 已删除）。
const live = useLiveStore()
const events = useEventsStore()
const prefs = useUiPrefs()

onMounted(() => {
  prefs.load()
  void live.load()
})

const pillState = computed(() => (events.degraded ? 'warn' : live.phase === 'live' ? 'live' : 'idle'))
const pillLabel = computed(() => (events.degraded ? '降级运行' : live.phase))
</script>

<template>
  <div class="dh-shell">
    <header class="dh-top">
      <NtStatusPill :state="pillState" :label="pillLabel" />
      <div class="dh-spacer" />
      <span class="dh-meta">v0.1.0-m3</span>
    </header>
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
.dh-top {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 20px;
  border-bottom: 1px solid var(--nt-border-1);
  background: var(--nt-chrome);
}
.dh-spacer {
  flex: 1;
}
.dh-meta {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
/* 页面级 .page 自带 padding，这里不再叠加，避免双层留白。 */
.dh-main {
  flex: 1;
  min-height: 0;
  overflow: auto;
}
</style>

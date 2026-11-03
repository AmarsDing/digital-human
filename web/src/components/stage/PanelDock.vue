<script setup lang="ts">
import type { PanelId } from '../../stores/uiPrefs'

const props = defineProps<{ open: Record<PanelId, boolean>; rightOpen: boolean }>()
const emit = defineEmits<{
  (e: 'toggle', id: PanelId): void
  (e: 'toggle-right'): void
}>()

const ITEMS: { id: PanelId; label: string }[] = [
  { id: 'voice', label: '声音' },
  { id: 'command', label: '动作' },
  { id: 'danmaku', label: '弹幕' },
  { id: 'koubo', label: '口播' },
  { id: 'refs', label: '参考图' },
  { id: 'songs', label: '歌曲' },
  { id: 'archive', label: '复盘' }
]
</script>

<template>
  <div class="dock">
    <button
      v-for="it in ITEMS"
      :key="it.id"
      type="button"
      :class="{ 'dock__btn--on': props.open[it.id] }"
      :aria-pressed="props.open[it.id]"
      @click="emit('toggle', it.id)"
    >
      {{ it.label }}
    </button>
    <button type="button" :class="{ 'dock__btn--on': rightOpen }" :aria-pressed="rightOpen" @click="emit('toggle-right')">字幕/事件</button>
  </div>
</template>

<style scoped>
.dock {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.dock button {
  height: 28px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: transparent;
  color: var(--nt-text-3);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.dock__btn--on {
  border-color: var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
}
</style>

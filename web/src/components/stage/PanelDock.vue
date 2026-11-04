<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import type { WorkspacePanelId } from '../../stores/uiPrefs'

const props = defineProps<{ active: WorkspacePanelId; pendingCount: number; rightOpen: boolean }>()
const emit = defineEmits<{
  (e: 'select', id: WorkspacePanelId): void
  (e: 'toggle-right'): void
}>()

const ITEMS: { id: WorkspacePanelId; label: string }[] = [
  { id: 'voice', label: '声音' },
  { id: 'command', label: '动作' },
  { id: 'danmaku', label: '弹幕' },
  { id: 'koubo', label: '口播' },
  { id: 'refs', label: '参考图' },
  { id: 'songs', label: '歌曲' }
]

const focused = ref<WorkspacePanelId>(props.active)
watch(
  () => props.active,
  (active) => {
    focused.value = active
  }
)

async function moveFocus(event: KeyboardEvent, id: WorkspacePanelId): Promise<void> {
  const index = ITEMS.findIndex((item) => item.id === id)
  let next = index
  if (event.key === 'ArrowRight') next = (index + 1) % ITEMS.length
  else if (event.key === 'ArrowLeft') next = (index - 1 + ITEMS.length) % ITEMS.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = ITEMS.length - 1
  else return
  event.preventDefault()
  const current = event.currentTarget as HTMLElement
  focused.value = ITEMS[next].id
  await nextTick()
  const tabs = current.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')
  tabs?.[next]?.focus()
}
</script>

<template>
  <div class="dock">
    <div class="dock__tabs" role="tablist" aria-label="直播工作区">
      <button
        v-for="it in ITEMS"
        :id="`workspace-tab-${it.id}`"
        :key="it.id"
        type="button"
        role="tab"
        :aria-selected="active === it.id"
        :aria-controls="`workspace-panel-${it.id}`"
        :tabindex="focused === it.id ? 0 : -1"
        :class="{ 'dock__btn--on': active === it.id }"
        @focus="focused = it.id"
        @click="emit('select', it.id)"
        @keydown="moveFocus($event, it.id)"
        @keydown.enter.prevent="emit('select', it.id)"
        @keydown.space.prevent="emit('select', it.id)"
      >
        {{ it.label }}
        <span v-if="it.id === 'danmaku'" class="dock__badge" :aria-label="`待确认 ${pendingCount} 条`">{{ pendingCount }}</span>
      </button>
    </div>
    <button type="button" class="dock__transcript" :aria-pressed="rightOpen" @click="emit('toggle-right')">字幕/事件</button>
  </div>
</template>

<style scoped>
.dock,
.dock__tabs {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.dock {
  justify-content: space-between;
}
.dock__tabs {
  min-width: 0;
}
.dock button {
  min-width: 44px;
  min-height: 44px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: transparent;
  color: var(--nt-text-3);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.dock button:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}
.dock__btn--on {
  border-color: var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
}
.dock__badge {
  margin-left: 4px;
  font-family: var(--nt-font-mono);
  font-variant-numeric: tabular-nums;
}
</style>

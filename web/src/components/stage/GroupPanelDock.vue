<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { WorkspacePanelId } from '../../stores/uiPrefs'

const ITEMS: { id: WorkspacePanelId; label: string }[] = [
  { id: 'voice', label: '声音' },
  { id: 'command', label: '动作' },
  { id: 'danmaku', label: '弹幕' },
  { id: 'koubo', label: '口播' },
  { id: 'refs', label: '参考图' },
  { id: 'songs', label: '歌曲' }
]

const props = defineProps<{
  active: WorkspacePanelId
  items: WorkspacePanelId[]
  pendingCount: number
}>()
const emit = defineEmits<{
  (e: 'select', id: WorkspacePanelId): void
}>()
const visibleItems = computed(() => ITEMS.filter((item) => props.items.includes(item.id)))

const focused = ref<WorkspacePanelId>(props.active)
watch(
  () => props.active,
  (active) => {
    focused.value = active
  }
)

async function moveFocus(event: KeyboardEvent, id: WorkspacePanelId): Promise<void> {
  const items = visibleItems.value
  const index = items.findIndex((item) => item.id === id)
  let next = index
  if (event.key === 'ArrowRight') next = (index + 1) % items.length
  else if (event.key === 'ArrowLeft') next = (index - 1 + items.length) % items.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = items.length - 1
  else return
  event.preventDefault()
  const current = event.currentTarget as HTMLElement
  focused.value = items[next].id
  await nextTick()
  const tabs = current.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')
  tabs?.[next]?.focus()
}
</script>

<template>
  <div class="dock">
    <div class="dock__tabs" role="tablist" aria-label="直播工作区">
      <button
        v-for="it in visibleItems"
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
  </div>
</template>

<style scoped>
.dock,
.dock__tabs {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
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

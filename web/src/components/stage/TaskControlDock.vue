<script setup lang="ts">
import { useId } from 'vue'

export interface TaskQuickAction {
  id: string
  label: string
}

export interface TaskGroup {
  id: string
  label: string
  summary?: string
}

defineProps<{
  groups: TaskGroup[]
  quickActions: TaskQuickAction[]
}>()

const expandedId = defineModel<string>('expandedId', { default: '' })
const emit = defineEmits<{
  'quick-action': [id: string]
}>()
const baseId = `task-control-${useId()}`

function toggleGroup(id: string) {
  expandedId.value = expandedId.value === id ? '' : id
}
</script>

<template>
  <div class="task-control-dock">
    <nav class="task-control-dock__quick" aria-label="直播快捷操作">
      <button
        v-for="action in quickActions"
        :key="action.id"
        type="button"
        :aria-label="action.label"
        @click="emit('quick-action', action.id)"
      >
        {{ action.label }}
      </button>
    </nav>

    <div class="task-control-dock__groups">
      <section v-for="group in groups" :key="group.id" class="task-control-dock__group">
        <button
          class="task-control-dock__group-trigger"
          type="button"
          :id="`${baseId}-trigger-${group.id}`"
          :aria-expanded="expandedId === group.id"
          :aria-controls="`${baseId}-${group.id}`"
          @click="toggleGroup(group.id)"
        >
          <span>{{ group.label }}</span>
          <small v-if="group.summary">{{ group.summary }}</small>
          <span aria-hidden="true">{{ expandedId === group.id ? '−' : '+' }}</span>
        </button>
        <div
          v-show="expandedId === group.id"
          :id="`${baseId}-${group.id}`"
          class="task-control-dock__panel"
          role="region"
          :aria-labelledby="`${baseId}-trigger-${group.id}`"
          :data-group-panel="group.id"
        >
          <slot :name="group.id" />
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.task-control-dock {
  display: grid;
  grid-template-columns: 48px minmax(0, 1fr);
  min-width: 0;
  min-height: 0;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
  overflow: hidden;
}

.task-control-dock__quick {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  padding: 4px 2px;
  border-inline-end: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
}

.task-control-dock__quick button {
  inline-size: 44px;
  min-block-size: 44px;
  padding: 0 2px;
  border: 0;
  border-radius: var(--nt-radius-s);
  background: transparent;
  color: var(--nt-text-2);
  cursor: pointer;
  font: inherit;
  font-size: var(--nt-fs-caption);
}

.task-control-dock__quick button:hover {
  background: var(--nt-accent-hover);
  color: var(--nt-text-1);
}

.task-control-dock__groups {
  min-width: 0;
  min-height: 0;
  overflow: auto;
}

.task-control-dock__group + .task-control-dock__group {
  border-block-start: 1px solid var(--nt-border-1);
}

.task-control-dock__group-trigger {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 8px;
  inline-size: 100%;
  min-block-size: 48px;
  padding: 8px 12px;
  border: 0;
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  cursor: pointer;
  font: inherit;
  text-align: start;
}

.task-control-dock__group-trigger small {
  overflow: hidden;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-control-dock__group-trigger[aria-expanded='true'] {
  color: var(--nt-accent);
}

.task-control-dock__panel {
  min-width: 0;
  padding: 12px;
}

.task-control-dock button:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}

@media (max-width: 559px) {
  .task-control-dock {
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: 48px minmax(0, 1fr);
  }

  .task-control-dock__quick {
    flex-direction: row;
    justify-content: flex-start;
    border-inline-end: 0;
    border-block-end: 1px solid var(--nt-border-1);
    overflow-x: auto;
  }
}
</style>

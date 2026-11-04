<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import NtButton from '../ui/NtButton.vue'

export interface MetricItem {
  id: string
  label: string
  value: string
  hint?: string
}

defineProps<{ items: MetricItem[] }>()

const isOpen = ref(false)
const root = ref<HTMLElement>()
const trigger = ref<InstanceType<typeof NtButton>>()
const panelId = `metrics-popover-${useId()}`

function restoreFocus() {
  void nextTick(() => {
    const element = trigger.value?.$el
    if (element instanceof HTMLElement) element.focus()
  })
}

function close() {
  if (!isOpen.value) return
  isOpen.value = false
  restoreFocus()
}

function toggle() {
  if (isOpen.value) close()
  else isOpen.value = true
}

function onKeydown(event: KeyboardEvent) {
  if (event.altKey && event.key.toLowerCase() === 'm') {
    event.preventDefault()
    toggle()
    return
  }
  if (event.key === 'Escape') close()
}

function onPointerDown(event: PointerEvent) {
  if (!isOpen.value || root.value?.contains(event.target as Node)) return
  close()
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  document.addEventListener('pointerdown', onPointerDown)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  document.removeEventListener('pointerdown', onPointerDown)
})
</script>

<template>
  <div ref="root" class="metrics-popover">
    <NtButton
      ref="trigger"
      class="metrics-popover__trigger"
      variant="ghost"
      aria-label="查看场次统计"
      aria-haspopup="dialog"
      :aria-expanded="isOpen"
      :aria-controls="isOpen ? panelId : undefined"
      title="场次统计（Alt+M）"
      @click="toggle"
    >
      统计
    </NtButton>

    <section
      v-if="isOpen"
      :id="panelId"
      class="metrics-popover__panel"
      role="dialog"
      aria-label="场次统计"
    >
      <header class="metrics-popover__header">
        <strong>场次统计</strong>
        <span>Alt+M 收起</span>
      </header>
      <dl class="metrics-popover__grid">
        <div v-for="item in items" :key="item.id" class="metrics-popover__item">
          <dt>{{ item.label }}</dt>
          <dd>{{ item.value }}</dd>
          <small v-if="item.hint">{{ item.hint }}</small>
        </div>
      </dl>
    </section>
  </div>
</template>

<style scoped>
.metrics-popover {
  position: relative;
  display: inline-flex;
}

.metrics-popover__trigger {
  min-width: 64px;
  min-height: 44px;
}

.metrics-popover__trigger:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}

.metrics-popover__panel {
  position: absolute;
  z-index: 20;
  inset-block-start: calc(100% + 8px);
  inset-inline-end: 0;
  inline-size: 320px;
  max-inline-size: calc(100vw - 24px);
  padding: 12px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
  box-shadow: var(--nt-shadow-depth);
}

.metrics-popover__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-block-end: 12px;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-panel);
}

.metrics-popover__header span {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}

.metrics-popover__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
  margin: 0;
}

.metrics-popover__item {
  min-width: 0;
  min-height: 52px;
  padding: 8px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-raised);
}

.metrics-popover__item dt {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}

.metrics-popover__item dd {
  margin: 4px 0 0;
  overflow: hidden;
  color: var(--nt-text-1);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-body);
  font-variant-numeric: tabular-nums;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.metrics-popover__item small {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}

@media (max-width: 559px) {
  .metrics-popover__panel {
    position: fixed;
    inset: auto 12px 12px;
    inline-size: auto;
    max-block-size: 70dvh;
    overflow: auto;
  }
}
</style>

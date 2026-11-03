<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps<{
  title: string
  x: number
  y: number
  width?: number
  collapsed: boolean
  badge?: string
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'move', x: number, y: number): void
  (e: 'toggle-collapse'): void
}>()

const local = ref({ x: props.x, y: props.y })
const root = ref<HTMLElement | null>(null)
let drag: { px: number; py: number; ox: number; oy: number } | null = null

// 窄屏不夹取会把面板推到视口外；宽度也不允许超过可视区。
const cssWidth = computed(() => (props.width || 320))
const styleWidth = computed(() => `min(${cssWidth.value}px, calc(100vw - 24px))`)

// 按容器（offsetParent，绝对定位参照物）夹取，退化为视口。
function containerRect(): { w: number; h: number } {
  const parent = root.value?.offsetParent as HTMLElement | null
  if (parent) {
    const r = parent.getBoundingClientRect()
    if (r.width > 0 && r.height > 0) return { w: r.width, h: r.height }
  }
  return { w: window.innerWidth, h: window.innerHeight }
}

function clampX(x: number): number {
  const { w } = containerRect()
  const pw = Math.min(cssWidth.value, Math.max(200, w - 24))
  return Math.max(0, Math.min(x, w - pw - 8))
}

function clampY(y: number): number {
  const { h } = containerRect()
  return Math.max(0, Math.min(y, h - 64))
}

function resetPosition(): void {
  local.value = { x: clampX(props.x), y: clampY(props.y) }
  emit('move', local.value.x, local.value.y)
}

function onResize(): void {
  local.value = { x: clampX(local.value.x), y: clampY(local.value.y) }
}

function down(e: PointerEvent): void {
  if ((e.target as HTMLElement).closest('button')) return
  drag = { px: e.clientX, py: e.clientY, ox: local.value.x, oy: local.value.y }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}

function move(e: PointerEvent): void {
  if (!drag) return
  local.value = {
    x: clampX(drag.ox + e.clientX - drag.px),
    y: clampY(drag.oy + e.clientY - drag.py)
  }
}

function up(): void {
  if (drag) emit('move', local.value.x, local.value.y)
  drag = null
  window.removeEventListener('pointermove', move)
  window.removeEventListener('pointerup', up)
}

onMounted(() => {
  onResize()
  window.addEventListener('resize', onResize)
})

onBeforeUnmount(() => {
  window.removeEventListener('pointermove', move)
  window.removeEventListener('pointerup', up)
  window.removeEventListener('resize', onResize)
})

defineExpose({ local })
</script>

<template>
  <section
    ref="root"
    class="panel"
    :class="{ 'panel--collapsed': collapsed }"
    :style="{ left: `${local.x}px`, top: `${local.y}px`, width: styleWidth }"
  >
    <header class="panel__head" @pointerdown="down">
      <h4>{{ title }}</h4>
      <span v-if="badge" class="panel__badge">{{ badge }}</span>
      <div class="panel__actions">
        <button type="button" aria-label="复位位置" title="复位位置" @click="resetPosition">⌂</button>
        <button type="button" :aria-label="collapsed ? '展开' : '收起'" @click="$emit('toggle-collapse')">
          {{ collapsed ? '▸' : '▾' }}
        </button>
        <button type="button" aria-label="关闭" @click="$emit('close')">×</button>
      </div>
    </header>
    <div v-show="!collapsed" class="panel__body"><slot /></div>
  </section>
</template>

<style scoped>
.panel {
  position: absolute;
  z-index: 20;
  display: flex;
  flex-direction: column;
  max-height: 70vh;
  border-radius: var(--nt-radius-l);
  border: 1px solid var(--nt-glass-border);
  background: var(--nt-glass-bg);
  box-shadow: var(--nt-shadow-depth);
  backdrop-filter: var(--nt-glass-filter);
}
.panel__head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--nt-border-1);
  cursor: grab;
  user-select: none;
}
.panel__head:active {
  cursor: grabbing;
}
.panel__head h4 {
  margin: 0;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-panel);
  font-weight: 600;
}
.panel__badge {
  padding: 1px 6px;
  border-radius: 999px;
  border: 1px solid var(--nt-border-1);
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.panel__actions {
  display: flex;
  gap: 2px;
  margin-left: auto;
}
.panel__actions button {
  width: 24px;
  height: 24px;
  border: 1px solid transparent;
  border-radius: var(--nt-radius-s);
  background: transparent;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-body);
  cursor: pointer;
}
.panel__actions button:hover {
  border-color: var(--nt-border-1);
  color: var(--nt-text-0);
}
.panel__body {
  min-height: 0;
  overflow: auto;
  padding: 10px;
}
.panel--collapsed {
  max-height: none;
}
</style>

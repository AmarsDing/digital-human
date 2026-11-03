<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

// mode='modal'：对话框语义（role=dialog + aria-modal + 焦点捕获/回焦 + 滚动锁）；
// mode='inline'：内嵌面板（SettingsPage），不输出 dialog 语义、不做焦点/滚动干预。
const props = withDefaults(defineProps<{ open: boolean; title: string; width?: number; mode?: 'modal' | 'inline' }>(), {
  mode: 'modal'
})
const emit = defineEmits<{ (e: 'close'): void }>()

const root = ref<HTMLElement | null>(null)
let restoreFocus: HTMLElement | null = null

function onKey(e: KeyboardEvent): void {
  if (!props.open) return
  if (e.key === 'Escape') {
    emit('close')
    return
  }
  if (e.key === 'Tab' && props.mode === 'modal' && root.value) {
    const focusables = Array.from(
      root.value.querySelectorAll<HTMLElement>(
        'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'
      )
    )
    if (!focusables.length) return
    const first = focusables[0]
    const last = focusables[focusables.length - 1]
    const active = document.activeElement as HTMLElement | null
    if (e.shiftKey && (active === first || !root.value.contains(active))) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && (active === last || !root.value.contains(active))) {
      e.preventDefault()
      first.focus()
    }
  }
}

watch(
  () => props.open,
  async (open) => {
    if (typeof document === 'undefined') return
    if (open) {
      document.addEventListener('keydown', onKey)
      if (props.mode === 'modal') {
        restoreFocus = document.activeElement as HTMLElement | null
        document.body.style.overflow = 'hidden'
        await nextTick()
        root.value?.querySelector<HTMLElement>('.nt-drawer__close')?.focus()
      }
    } else {
      document.removeEventListener('keydown', onKey)
      if (props.mode === 'modal') {
        document.body.style.overflow = ''
        restoreFocus?.focus?.()
        restoreFocus = null
      }
    }
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  if (typeof document === 'undefined') return
  document.removeEventListener('keydown', onKey)
  if (props.mode === 'modal' && props.open) document.body.style.overflow = ''
})
</script>

<template>
  <component
    :is="mode === 'inline' ? 'section' : 'div'"
    ref="root"
    :class="mode === 'inline' ? 'nt-drawer nt-drawer--inline' : 'nt-drawer'"
    :style="{ width: `${width || 560}px` }"
    v-show="open"
    v-bind="mode === 'modal' ? { role: 'dialog', 'aria-modal': 'true', 'aria-label': title } : { 'aria-label': title }"
  >
    <header class="nt-drawer__head">
      <h2>{{ title }}</h2>
      <button class="nt-drawer__close" type="button" aria-label="关闭" @click="$emit('close')">×</button>
    </header>
    <div class="nt-drawer__body"><slot /></div>
    <footer v-if="$slots.footer" class="nt-drawer__foot"><slot name="footer" /></footer>
  </component>
</template>

<style scoped>
.nt-drawer {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 60;
  display: flex;
  flex-direction: column;
  max-width: 92vw;
  background: var(--nt-bg-glass);
  border-left: 1px solid var(--nt-glass-border);
  box-shadow: var(--nt-shadow-depth);
}
.nt-drawer--inline {
  position: relative;
  max-width: none;
  height: 100%;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
}
.nt-drawer__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--nt-pad-panel);
  border-bottom: 1px solid var(--nt-border-1);
}
.nt-drawer__head h2 {
  margin: 0;
  font-size: var(--nt-fs-title);
  color: var(--nt-text-1);
  font-weight: 600;
}
.nt-drawer__close {
  width: 32px;
  height: 32px;
  border-radius: var(--nt-radius-s);
  border: 1px solid transparent;
  background: transparent;
  color: var(--nt-text-2);
  font-size: 18px;
  cursor: pointer;
}
.nt-drawer__close:hover {
  border-color: var(--nt-border-1);
  color: var(--nt-text-0);
}
.nt-drawer__body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: var(--nt-pad-panel);
}
.nt-drawer__foot {
  padding: 10px var(--nt-pad-panel);
  border-top: 1px solid var(--nt-border-1);
  background: var(--nt-data-bg);
}
</style>

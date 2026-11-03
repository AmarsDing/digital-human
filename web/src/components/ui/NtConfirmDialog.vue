<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

// Nt 风格破坏性操作确认框：Esc 取消、Enter 确认、关闭后焦点回还原触发元素。
const props = defineProps<{
  open: boolean
  title: string
  message: string
  confirmText?: string
  danger?: boolean
}>()
const emit = defineEmits<{ (e: 'confirm'): void; (e: 'cancel'): void }>()

const root = ref<HTMLElement | null>(null)
const confirmBtn = ref<HTMLButtonElement | null>(null)
let restoreFocus: HTMLElement | null = null

function onKey(e: KeyboardEvent): void {
  if (!props.open) return
  if (e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation()
    emit('cancel')
  } else if (e.key === 'Enter') {
    e.preventDefault()
    e.stopPropagation()
    emit('confirm')
  } else if (e.key === 'Tab' && root.value) {
    // 简易焦点捕获：在两个按钮之间循环。
    const focusables = Array.from(root.value.querySelectorAll<HTMLElement>('button'))
    if (!focusables.length) return
    const first = focusables[0]
    const last = focusables[focusables.length - 1]
    const active = document.activeElement as HTMLElement | null
    if (e.shiftKey && active === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && active === last) {
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
      restoreFocus = document.activeElement as HTMLElement | null
      document.addEventListener('keydown', onKey, true)
      await nextTick()
      confirmBtn.value?.focus()
    } else {
      document.removeEventListener('keydown', onKey, true)
      restoreFocus?.focus?.()
      restoreFocus = null
    }
  }
)

onBeforeUnmount(() => {
  if (typeof document !== 'undefined') document.removeEventListener('keydown', onKey, true)
})
</script>

<template>
  <div v-if="open" ref="root" class="nt-confirm__mask" @click.self="emit('cancel')">
    <div class="nt-confirm" role="alertdialog" aria-modal="true" :aria-label="title" :aria-describedby="'nt-confirm-msg'">
      <h3 class="nt-confirm__title">{{ title }}</h3>
      <p id="nt-confirm-msg" class="nt-confirm__msg">{{ message }}</p>
      <div class="nt-confirm__btns">
        <button type="button" class="nt-confirm__btn" @click="emit('cancel')">取消（Esc）</button>
        <button
          ref="confirmBtn"
          type="button"
          class="nt-confirm__btn"
          :class="{ 'nt-confirm__btn--danger': danger !== false }"
          @click="emit('confirm')"
        >
          {{ confirmText || '确认' }}（Enter）
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.nt-confirm__mask {
  position: fixed;
  inset: 0;
  z-index: 90;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--nt-scrim);
}
.nt-confirm {
  width: 360px;
  max-width: 90vw;
  padding: 16px;
  border: 1px solid var(--nt-glass-border);
  border-radius: var(--nt-radius-l);
  background: var(--nt-glass-bg);
  box-shadow: var(--nt-shadow-depth);
  backdrop-filter: var(--nt-glass-filter);
}
.nt-confirm__title {
  margin: 0 0 8px;
  color: var(--nt-text-0);
  font-size: var(--nt-fs-title);
}
.nt-confirm__msg {
  margin: 0 0 14px;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
  line-height: 1.6;
}
.nt-confirm__btns {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.nt-confirm__btn {
  height: 32px;
  padding: 0 14px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.nt-confirm__btn--danger {
  border-color: var(--nt-danger-border);
  background: var(--nt-danger-soft);
  color: var(--nt-danger);
}
.nt-confirm__btn:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
}
</style>

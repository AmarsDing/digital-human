<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

// Nt 风格确认框。dangerous=true 启用危险操作合同（下播/停推/危险删除）：
// 初始焦点落取消、Enter 只执行当前聚焦按钮的原生激活、Esc 否定关闭、busy 防重复。
// 非危险确认保持既有行为：初始焦点在确认按钮、任意 Enter 确认。
const props = defineProps<{
  open: boolean
  title: string
  message: string
  confirmText?: string
  danger?: boolean
  dangerous?: boolean
  busy?: boolean
  error?: string
}>()
const emit = defineEmits<{ (e: 'confirm'): void; (e: 'cancel'): void }>()

const root = ref<HTMLElement | null>(null)
const confirmBtn = ref<HTMLButtonElement | null>(null)
const cancelBtn = ref<HTMLButtonElement | null>(null)
let restoreFocus: HTMLElement | null = null

function onKey(e: KeyboardEvent): void {
  if (!props.open) return
  if (e.key === 'Escape') {
    // 请求进行中忽略重复按键，不触发第二次业务操作。
    if (props.busy) return
    e.preventDefault()
    e.stopPropagation()
    emit('cancel')
    return
  }
  if (e.key === 'Enter') {
    // 危险合同：不做全局 Enter 确认，只保留当前聚焦按钮的原生激活。
    if (props.dangerous) return
    if (props.busy) return
    e.preventDefault()
    e.stopPropagation()
    emit('confirm')
    return
  }
  if (e.key === 'Tab' && root.value) {
    // 简易焦点捕获：在两个按钮之间循环。
    const focusables = Array.from(root.value.querySelectorAll<HTMLElement>('button:not(:disabled)'))
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

function onConfirm(): void {
  // busy 防重复：双击/重复激活只产生一次确认。
  if (props.busy) return
  emit('confirm')
}

function onCancel(): void {
  if (props.busy) return
  emit('cancel')
}

watch(
  () => props.open,
  async (open) => {
    if (typeof document === 'undefined') return
    if (open) {
      restoreFocus = document.activeElement as HTMLElement | null
      document.addEventListener('keydown', onKey, true)
      await nextTick()
      // 危险合同：默认焦点落「取消/继续直播」，不聚焦危险确认按钮。
      if (props.dangerous) cancelBtn.value?.focus()
      else confirmBtn.value?.focus()
    } else {
      document.removeEventListener('keydown', onKey, true)
      restoreFocus?.focus?.()
      restoreFocus = null
    }
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  if (typeof document !== 'undefined') document.removeEventListener('keydown', onKey, true)
})
</script>

<template>
  <div v-if="open" ref="root" class="nt-confirm__mask" @click.self="onCancel">
    <div
      class="nt-confirm"
      role="alertdialog"
      aria-modal="true"
      :aria-label="title"
      :aria-describedby="'nt-confirm-msg'"
      :aria-busy="busy || undefined"
    >
      <h3 class="nt-confirm__title">{{ title }}</h3>
      <p id="nt-confirm-msg" class="nt-confirm__msg">{{ message }}</p>
      <p v-if="error" class="nt-confirm__error" role="alert">{{ error }}</p>
      <div class="nt-confirm__btns">
        <button ref="cancelBtn" type="button" class="nt-confirm__btn" :disabled="busy" @click="onCancel">
          取消（Esc）
        </button>
        <button
          ref="confirmBtn"
          type="button"
          class="nt-confirm__btn"
          :class="{ 'nt-confirm__btn--danger': dangerous || danger !== false }"
          :disabled="busy"
          @click="onConfirm"
        >
          {{ busy ? '处理中…' : `${confirmText || '确认'}${dangerous ? '' : '（Enter）'}` }}
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
.nt-confirm__error {
  margin: 0 0 12px;
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
  line-height: 1.5;
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
.nt-confirm__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.nt-confirm__btn:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
}
</style>

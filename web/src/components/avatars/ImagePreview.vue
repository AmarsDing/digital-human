<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

// 大图预览弹层：同屏仅 1 个（父级用固定 pool key 保证），关闭即 revoke；
// Esc 关闭并回焦（AGENTS.md §6-11）。
const props = defineProps<{ open: boolean; url: string; alt?: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const closeBtn = ref<HTMLButtonElement | null>(null)
let restoreFocus: HTMLElement | null = null

function onKey(e: KeyboardEvent): void {
  if (!props.open) return
  if (e.key === 'Escape') {
    e.preventDefault()
    e.stopPropagation()
    emit('close')
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
      closeBtn.value?.focus()
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
  <div v-if="open" class="preview__mask" role="dialog" aria-modal="true" :aria-label="alt || '图片预览'" @click.self="emit('close')">
    <div class="preview__box">
      <img :src="url" :alt="alt || '图片预览'" class="preview__img" />
      <button ref="closeBtn" type="button" class="preview__close" aria-label="关闭预览" @click="emit('close')">×</button>
    </div>
  </div>
</template>

<style scoped>
.preview__mask {
  position: fixed;
  inset: 0;
  z-index: 90;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--nt-scrim);
}
.preview__box {
  position: relative;
  max-width: 86vw;
  max-height: 86vh;
  border: 1px solid var(--nt-glass-border);
  border-radius: var(--nt-radius-l);
  background: var(--nt-glass-bg);
  box-shadow: var(--nt-shadow-depth);
  overflow: hidden;
}
.preview__img {
  display: block;
  max-width: 86vw;
  max-height: 80vh;
  object-fit: contain;
}
.preview__close {
  position: absolute;
  top: 8px;
  right: 8px;
  width: 44px;
  height: 44px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-2);
  font-size: 20px;
  cursor: pointer;
}
.preview__close:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
}
</style>

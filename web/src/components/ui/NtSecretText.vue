<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ value?: string; label?: string }>()
const shown = computed(() => {
  const v = (props.value ?? '').trim()
  if (!v) return '—'
  return `***${v.slice(-8)}`
})
</script>

<template>
  <span class="nt-secret" :title="label || '密钥仅显示尾号'">{{ shown }}</span>
</template>

<style scoped>
.nt-secret {
  display: inline-flex;
  align-items: center;
  min-height: 26px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px dashed var(--nt-border-1);
  color: var(--nt-text-2);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-caption);
  letter-spacing: 0.04em;
}
</style>

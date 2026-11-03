<script setup lang="ts">
// 状态胶囊：999px 胶囊，颜色只走语义 token；文案变化经 aria-live 播报。
defineProps<{
  variant: 'neutral' | 'info' | 'success' | 'warn' | 'danger'
  label: string
}>()
</script>

<template>
  <span class="capsule" :class="`capsule--${variant}`" aria-live="polite">{{ label }}</span>
</template>

<style scoped>
.capsule {
  display: inline-flex;
  align-items: center;
  height: 26px;
  padding: 0 12px;
  border-radius: 999px;
  border: 1px solid var(--nt-border-1);
  font-size: var(--nt-fs-caption);
  white-space: nowrap;
}
.capsule--neutral {
  color: var(--nt-text-3);
  border-color: var(--nt-border-1);
}
.capsule--info {
  color: var(--nt-info);
  border-color: var(--nt-info);
}
.capsule--success {
  color: var(--nt-success);
  border-color: var(--nt-success-border);
}
.capsule--warn {
  color: var(--nt-warn);
  border-color: var(--nt-warn-border);
}
.capsule--danger {
  color: var(--nt-danger);
  border-color: var(--nt-danger-border);
}
</style>

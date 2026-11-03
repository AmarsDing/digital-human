<script setup lang="ts">
export interface StatItem {
  id: string
  label: string
  value: string
  hint?: string
}

defineProps<{ items: StatItem[] }>()
</script>

<template>
  <div class="stats">
    <div v-for="it in items" :key="it.id" class="stats__item">
      <span class="stats__label">{{ it.label }}</span>
      <span class="stats__value">{{ it.value }}</span>
      <span v-if="it.hint" class="stats__hint">{{ it.hint }}</span>
    </div>
  </div>
</template>

<style scoped>
.stats {
  display: flex;
  align-items: stretch;
  gap: 1px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-raised);
  overflow: hidden;
}
.stats__item {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 6px 10px;
  background: var(--nt-data-bg);
}
.stats__label {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.stats__value {
  color: var(--nt-text-0);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-num-lg);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.stats__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
</style>

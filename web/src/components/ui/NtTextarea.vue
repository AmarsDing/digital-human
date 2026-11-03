<script setup lang="ts">
defineProps<{ label: string; modelValue: string; hint?: string; rows?: number }>()
defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>

<template>
  <label class="nt-ta">
    <span class="nt-ta__label">{{ label }}</span>
    <textarea
      class="nt-ta__input"
      :rows="rows || 3"
      :value="modelValue"
      @input="$emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
    />
    <span v-if="hint" class="nt-ta__hint">{{ hint }}</span>
  </label>
</template>

<style scoped>
.nt-ta {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.nt-ta__label {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.nt-ta__input {
  padding: 8px 12px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
  resize: vertical;
}
.nt-ta__input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
  box-shadow: 0 0 0 2px var(--nt-focus-ring);
}
.nt-ta__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
</style>

<script setup lang="ts">
defineProps<{
  label: string
  modelValue: string | number
  options: { label: string; value: string }[]
  disabled?: boolean
}>()
defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>

<template>
  <label class="nt-select">
    <span class="nt-select__label">{{ label }}</span>
    <select
      class="nt-select__input"
      :value="String(modelValue)"
      :disabled="disabled"
      @change="$emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
    >
      <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option>
    </select>
  </label>
</template>

<style scoped>
.nt-select {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.nt-select__label {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.nt-select__input {
  height: 44px;
  padding: 0 10px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.nt-select__input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
  box-shadow: 0 0 0 2px var(--nt-focus-ring);
}
.nt-select__input:disabled {
  opacity: 0.5;
}
</style>

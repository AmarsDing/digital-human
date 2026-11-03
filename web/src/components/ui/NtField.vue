<script setup>
defineProps({
  label: { type: String, required: true },
  hint: { type: String, default: '' },
  modelValue: { type: [String, Number], default: '' }
})
defineEmits(['update:modelValue'])
</script>

<template>
  <label class="nt-field">
    <span class="nt-field__label">{{ label }}</span>
    <input
      class="nt-field__input"
      :value="modelValue"
      type="text"
      @input="$emit('update:modelValue', $event.target.value)"
    />
    <span v-if="hint" class="nt-field__hint">{{ hint }}</span>
  </label>
</template>

<style scoped>
.nt-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.nt-field__label {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.nt-field__input {
  height: 42px;
  padding: 0 12px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.nt-field__input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
  box-shadow: 0 0 0 2px var(--nt-focus-ring);
}
.nt-field__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
</style>

<script setup lang="ts">
defineProps<{ label: string; modelValue: boolean; hint?: string; disabled?: boolean }>()
defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()
</script>

<template>
  <label class="nt-toggle" :class="{ 'nt-toggle--disabled': disabled }">
    <input
      class="nt-toggle__input"
      type="checkbox"
      :checked="modelValue"
      :disabled="disabled"
      @change="$emit('update:modelValue', ($event.target as HTMLInputElement).checked)"
    />
    <span class="nt-toggle__track" aria-hidden="true"><i /></span>
    <span class="nt-toggle__text">
      {{ label }}
      <small v-if="hint">{{ hint }}</small>
    </span>
  </label>
</template>

<style scoped>
.nt-toggle {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 42px;
  cursor: pointer;
}
.nt-toggle--disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.nt-toggle__input {
  position: absolute;
  opacity: 0;
  width: 1px;
  height: 1px;
}
.nt-toggle__track {
  position: relative;
  width: 38px;
  height: 20px;
  flex: none;
  border-radius: 999px;
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  transition: background var(--nt-transition), border-color var(--nt-transition);
}
.nt-toggle__track i {
  position: absolute;
  top: 3px;
  left: 3px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: var(--nt-text-3);
  transition: transform var(--nt-transition), background var(--nt-transition);
}
.nt-toggle__input:checked + .nt-toggle__track {
  background: var(--nt-accent-soft);
  border-color: var(--nt-accent-border);
}
.nt-toggle__input:checked + .nt-toggle__track i {
  transform: translateX(18px);
  background: var(--nt-accent);
}
.nt-toggle__input:focus-visible + .nt-toggle__track {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
}
.nt-toggle__text {
  color: var(--nt-text-2);
  font-size: var(--nt-fs-body);
}
.nt-toggle__text small {
  display: block;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
</style>

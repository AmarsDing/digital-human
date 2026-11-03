<script setup>
defineProps({
  variant: { type: String, default: 'default' }, // default | primary | danger | ghost
  size: { type: String, default: 'md' }, // md: 42px, sm: 36px
  disabled: { type: Boolean, default: false }
})
</script>

<template>
  <button
    class="nt-btn"
    :class="[`nt-btn--${variant}`, `nt-btn--${size}`]"
    :disabled="disabled"
    type="button"
  >
    <slot />
  </button>
</template>

<style scoped>
.nt-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  min-width: 88px;
  padding: 0 14px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-bg-2);
  color: var(--nt-text-1);
  font: inherit;
  cursor: pointer;
  transition: background var(--nt-transition), border-color var(--nt-transition);
}
.nt-btn--md {
  height: 42px;
}
.nt-btn--sm {
  height: 36px;
  font-size: var(--nt-fs-caption);
}
.nt-btn:hover {
  background: var(--nt-bg-3);
  border-color: var(--nt-border-2);
}
.nt-btn:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
}
.nt-btn--primary {
  background: var(--nt-accent-soft);
  border-color: var(--nt-accent-border);
  color: var(--nt-text-0);
}
.nt-btn--danger {
  background: var(--nt-danger-soft);
  border-color: var(--nt-danger-border);
  color: var(--nt-danger);
}
.nt-btn--ghost {
  background: transparent;
}
.nt-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
</style>

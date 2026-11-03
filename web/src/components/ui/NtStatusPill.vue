<script setup>
defineProps({
  state: { type: String, default: 'idle' }, // idle | live | warn | error
  label: { type: String, required: true }
})
</script>

<template>
  <span class="nt-pill" :class="`nt-pill--${state}`">
    <i class="nt-pill__dot" aria-hidden="true" />
    {{ label }}
  </span>
</template>

<style scoped>
.nt-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 10px;
  border-radius: 999px;
  border: 1px solid var(--nt-border-1);
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
  white-space: nowrap;
}
.nt-pill__dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}
.nt-pill--live {
  color: var(--nt-success);
  border-color: var(--nt-success-border);
}
.nt-pill--warn {
  color: var(--nt-warn);
  border-color: var(--nt-warn-border);
}
.nt-pill--error {
  color: var(--nt-danger);
  border-color: var(--nt-danger-border);
}
.nt-pill--live .nt-pill__dot {
  animation: nt-pulse 1.6s ease-in-out infinite;
}
@keyframes nt-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
@media (prefers-reduced-motion: reduce) {
  .nt-pill__dot { animation: none; }
}
</style>

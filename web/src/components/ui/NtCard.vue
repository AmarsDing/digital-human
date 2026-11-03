<script setup>
defineProps({
  title: { type: String, default: '' },
  tone: { type: String, default: 'default' } // default | object
})
</script>

<template>
  <section class="nt-card" :class="`nt-card--${tone}`">
    <header v-if="title || $slots.actions" class="nt-card__head">
      <h2 class="nt-card__title">{{ title }}</h2>
      <div class="nt-card__actions"><slot name="actions" /></div>
    </header>
    <div class="nt-card__body"><slot /></div>
  </section>
</template>

<style scoped>
.nt-card {
  background: var(--nt-glass-bg);
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  box-shadow: var(--nt-shadow-depth);
  backdrop-filter: var(--nt-glass-filter);
  padding: var(--nt-pad-panel);
  min-width: 0;
}
.nt-card--object {
  border-color: var(--nt-glass-border);
  background: var(--nt-glass-raised);
}
.nt-card__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}
.nt-card__title {
  margin: 0;
  font-size: var(--nt-fs-title);
  font-weight: 600;
  color: var(--nt-text-0);
}
.nt-card__body {
  color: var(--nt-text-2);
}
</style>

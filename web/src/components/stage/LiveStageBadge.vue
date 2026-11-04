<script setup lang="ts">
import { computed } from 'vue'

type LiveBadgeStatus = 'live' | 'degraded' | 'error'

const props = withDefaults(defineProps<{
  status: LiveBadgeStatus
  elapsedSeconds?: number
}>(), {
  elapsedSeconds: 0
})

const duration = computed(() => {
  const total = Math.max(0, Math.floor(props.elapsedSeconds))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const seconds = total % 60
  return [hours, minutes, seconds].map((part) => String(part).padStart(2, '0')).join(':')
})

const label = computed(() => {
  if (props.status === 'error') return '直播异常'
  if (props.status === 'degraded') return `LIVE · 降级 ${duration.value}`
  return `LIVE ${duration.value}`
})

const accessibleLabel = computed(() => {
  if (props.status === 'error') return '直播异常'
  if (props.status === 'degraded') return `直播降级，已直播 ${duration.value}`
  return `直播中，已直播 ${duration.value}`
})
</script>

<template>
  <div
    class="live-stage-badge"
    :class="`live-stage-badge--${status}`"
    role="status"
    aria-live="polite"
    :aria-label="accessibleLabel"
    :data-state="status"
  >
    <span class="live-stage-badge__dot" aria-hidden="true" />
    <span>{{ label }}</span>
  </div>
</template>

<style scoped>
.live-stage-badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 28px;
  padding: 4px 10px;
  border: 1px solid var(--nt-success-border);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-bg);
  color: var(--nt-success);
  box-shadow: var(--nt-shadow-depth);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-caption);
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  line-height: 1;
  white-space: nowrap;
}

.live-stage-badge--degraded {
  border-color: var(--nt-warn-border);
  color: var(--nt-warn);
}

.live-stage-badge--error {
  border-color: var(--nt-danger-border);
  color: var(--nt-danger);
}

.live-stage-badge__dot {
  inline-size: 6px;
  block-size: 6px;
  flex: 0 0 auto;
  border-radius: 999px;
  background: currentColor;
}
</style>

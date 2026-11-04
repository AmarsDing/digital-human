<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useVoiceInteraction } from '../../services/voice/useVoiceInteraction'

const props = withDefaults(defineProps<{
  sessionId?: string
  epoch?: number
  canSpeak: boolean
}>(), {
  sessionId: '',
  epoch: 0
})

const voice = useVoiceInteraction({
  sessionId: () => props.sessionId,
  epoch: () => props.epoch,
  canSpeak: () => props.canSpeak
})
const holding = ref(false)
const continuousStarted = ref(false)
const continuousActive = computed(() => continuousStarted.value || ['connecting', 'listening', 'thinking', 'speaking'].includes(voice.phase.value))
const phaseLabel = computed(() => ({
  connecting: '连',
  listening: '听',
  thinking: '想',
  speaking: '说'
} as Record<string, string>)[voice.phase.value] || '')

onMounted(() => {
  void voice.refresh()
  window.addEventListener('pointerup', endHold)
  window.addEventListener('pointercancel', endHold)
})
onBeforeUnmount(() => {
  window.removeEventListener('pointerup', endHold)
  window.removeEventListener('pointercancel', endHold)
})
watch(
  () => [props.sessionId, props.epoch],
  async () => {
    continuousStarted.value = false
    await voice.cancel()
    await voice.refresh()
  }
)

async function toggleMode(): Promise<void> {
  if (continuousActive.value) await voice.cancel()
  continuousStarted.value = false
  voice.mode.value = voice.mode.value === 'ptt' ? 'continuous' : 'ptt'
}

async function toggleContinuous(): Promise<void> {
  if (continuousActive.value) {
    continuousStarted.value = false
    await voice.cancel()
    return
  }
  continuousStarted.value = true
  await voice.press()
}

function startHold(): void {
  if (holding.value) return
  holding.value = true
  void voice.press()
}

function endHold(): void {
  if (!holding.value) return
  holding.value = false
  void voice.release()
}

function holdKeydown(event: KeyboardEvent): void {
  if (event.repeat) return
  event.preventDefault()
  startHold()
}

function holdKeyup(event: KeyboardEvent): void {
  event.preventDefault()
  endHold()
}
</script>

<template>
  <div class="voice-control" :class="{ 'voice-control--disabled': voice.micDisabled.value }">
    <button
      v-if="voice.mode.value === 'ptt'"
      type="button"
      class="voice-control__button"
      aria-label="按住说话"
      :aria-disabled="voice.micDisabled.value"
      title="按住说话；不可用时点击查看原因"
      @pointerdown.prevent="startHold"
      @pointerup.prevent="endHold"
      @pointercancel="endHold"
      @keydown.space="holdKeydown"
      @keydown.enter="holdKeydown"
      @keyup.space="holdKeyup"
      @keyup.enter="holdKeyup"
      @blur="endHold"
    >
      语音
    </button>
    <button
      v-else
      type="button"
      class="voice-control__button"
      :aria-label="continuousActive ? '停止连续对话' : '开始连续对话'"
      :aria-disabled="voice.micDisabled.value && !continuousActive"
      @click="toggleContinuous"
    >
      {{ continuousActive ? '停止' : '连续' }}
    </button>
    <button
      type="button"
      class="voice-control__mode"
      :aria-label="voice.mode.value === 'ptt' ? '切换连续对话' : '切换按住说话'"
      :title="voice.mode.value === 'ptt' ? '切换连续对话' : '切换按住说话'"
      @click="toggleMode"
    >
      {{ voice.mode.value === 'ptt' ? '连' : '按' }}
    </button>
    <span class="voice-control__status" aria-live="polite">{{ phaseLabel }}</span>
    <span v-if="voice.sourceText.value" class="voice-control__meta">{{ voice.sourceText.value }}</span>
    <span v-if="voice.latencyText.value" class="voice-control__meta voice-control__latency">{{ voice.latencyText.value }}</span>
    <span v-if="voice.error.value" class="voice-control__error" role="alert">{{ voice.error.value }}</span>
  </div>
</template>

<style scoped>
.voice-control {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}
.voice-control button {
  min-width: 44px;
  min-height: 44px;
  padding: 0 8px;
  border: 1px solid var(--nt-accent-border);
  border-radius: var(--nt-radius-m);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.voice-control button:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}
.voice-control__mode {
  padding: 0 4px !important;
}
.voice-control--disabled .voice-control__button {
  opacity: 0.65;
}
.voice-control__status {
  min-width: 1em;
  color: var(--nt-accent);
  font-size: var(--nt-fs-caption);
}
.voice-control__meta,
.voice-control__error {
  max-width: 180px;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
  line-height: 1.3;
}
.voice-control__latency {
  font-family: var(--nt-font-mono);
  font-variant-numeric: tabular-nums;
}
.voice-control__error {
  color: var(--nt-danger);
}
@media (max-width: 799px) {
  .voice-control__meta,
  .voice-control__error {
    max-width: 120px;
  }
}
</style>

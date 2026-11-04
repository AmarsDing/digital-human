<script setup lang="ts">
import NtButton from '../ui/NtButton.vue'

const props = withDefaults(defineProps<{
  canSpeak?: boolean
  busy?: boolean
  canInterrupt?: boolean
}>(), {
  canSpeak: true,
  busy: false,
  canInterrupt: false
})

const model = defineModel<string>({ default: '' })
const emit = defineEmits<{
  send: [text: string]
  interrupt: []
  expand: []
  more: []
}>()

function send() {
  const text = model.value.trim()
  if (!props.canSpeak || props.busy || !text) return
  emit('send', text)
  model.value = ''
}
</script>

<template>
  <form class="compact-composer" aria-label="直播互动输入" @submit.prevent="send">
    <NtButton
      class="compact-composer__voice"
      variant="primary"
      disabled
      aria-label="语音交互待接入"
      title="语音交互待接入"
    >
      语音 · 待接入
    </NtButton>

    <label class="compact-composer__field">
      <span class="sr-only">对数字人说</span>
      <textarea
        v-model="model"
        rows="1"
        :disabled="!canSpeak"
        :placeholder="canSpeak ? '对数字人说…' : '开播后可发言'"
        @keydown.enter.exact.prevent="send"
      />
    </label>

    <div class="compact-composer__actions">
      <NtButton
        class="compact-composer__action"
        variant="primary"
        :disabled="!canSpeak || busy || !model.trim()"
        aria-label="发送文字"
        @click="send"
      >
        发送
      </NtButton>
      <NtButton
        class="compact-composer__action"
        :disabled="!canInterrupt"
        aria-label="打断当前播报"
        @click="emit('interrupt')"
      >
        打断
      </NtButton>
      <NtButton
        class="compact-composer__action compact-composer__secondary"
        variant="ghost"
        aria-label="展开长文输入"
        @click="emit('expand')"
      >
        长文
      </NtButton>
      <NtButton
        class="compact-composer__action compact-composer__secondary"
        variant="ghost"
        aria-label="更多输入设置"
        @click="emit('more')"
      >
        更多
      </NtButton>
    </div>
  </form>
</template>

<style scoped>
.compact-composer {
  display: grid;
  grid-template-columns: auto minmax(40px, 1fr) auto;
  align-items: center;
  gap: 4px;
  min-width: 0;
  min-height: 64px;
  padding: 6px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
}

.compact-composer__voice,
.compact-composer__action {
  min-width: 44px;
  min-height: 44px;
  padding: 0 10px;
}

.compact-composer__voice {
  min-width: 112px;
}

.compact-composer__voice:focus-visible,
.compact-composer__action:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}

.compact-composer__field {
  min-width: 0;
}

.compact-composer textarea {
  display: block;
  inline-size: 100%;
  block-size: 44px;
  min-width: 0;
  padding: 11px 10px;
  resize: none;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-inset);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-body);
  line-height: 20px;
}

.compact-composer textarea:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}

.compact-composer__actions {
  display: flex;
  gap: 4px;
}

.sr-only {
  position: absolute;
  inline-size: 1px;
  block-size: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

@media (max-width: 559px) {
  .compact-composer textarea {
    font-size: 16px;
  }

  .compact-composer__voice {
    min-width: 88px;
    padding: 0 6px;
  }

  .compact-composer__action {
    padding: 0 6px;
  }
}

@media (max-width: 359px) {
  .compact-composer__voice {
    min-width: 76px;
    font-size: var(--nt-fs-caption);
  }
}
</style>

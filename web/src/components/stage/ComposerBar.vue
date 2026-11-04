<script setup lang="ts">
import { ref } from 'vue'
import NtSelect from '../ui/NtSelect.vue'
import VoiceControl from './VoiceControl.vue'
import { sendChat } from '../../services/api/panels'
import { classifyApiError } from '../../services/api/client'

const props = withDefaults(defineProps<{
  canSpeak: boolean
  sessionId?: string
  epoch?: number
  busy?: boolean
  error?: string
}>(), {
  sessionId: '',
  epoch: 0
})
const emit = defineEmits<{
  (e: 'say', text: string, mode: 'sentence' | 'whole'): void
  (e: 'command', text: string): void
  (e: 'stop'): void
  (e: 'interrupt'): void
}>()

const text = ref('')
const mode = ref<'sentence' | 'whole'>('sentence')
// N1 对话模式（蓝本 stage sendChat → /api/chat）：口播=直接 TTS，对话=走 LLM 后播报应答。
const channel = ref<'say' | 'chat'>('say')
const hint = ref('')
const chatBusy = ref(false)
const inputRef = ref<HTMLTextAreaElement | null>(null)

const MODES = [
  { label: '按句', value: 'sentence' },
  { label: '整段', value: 'whole' }
]
const CHANNELS = [
  { label: '口播', value: 'say' },
  { label: '对话', value: 'chat' }
]

function focusInput(): void {
  inputRef.value?.focus()
}
defineExpose({ focusInput })

async function send(): Promise<void> {
  const value = text.value.trim()
  if (!value || !props.canSpeak || props.busy || chatBusy.value) return
  // `/` 前缀走控制台命令通道（live.command），其余按当前通道发送。
  if (value.startsWith('/')) {
    emit('command', value.slice(1))
    hint.value = '已按命令发送：' + value.slice(1)
    text.value = ''
    return
  }
  if (channel.value === 'chat') {
    chatBusy.value = true
    hint.value = '思考中…'
    try {
      const r = await sendChat(value)
      const reply = [...(r.reply || '')]
      hint.value = '她答：' + (reply.length > 60 ? reply.slice(0, 60).join('') + '…' : reply.join(''))
      text.value = ''
    } catch (err) {
      hint.value = '对话失败：' + classifyApiError(err).message
    } finally {
      chatBusy.value = false
    }
    return
  }
  hint.value = ''
  emit('say', value, mode.value)
  text.value = ''
}

function keydown(e: KeyboardEvent): void {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    void send()
  }
}
</script>

<template>
  <div class="composer">
    <VoiceControl
      class="composer__voice"
      :session-id="sessionId"
      :epoch="epoch"
      :can-speak="canSpeak"
    />
    <textarea
      ref="inputRef"
      v-model="text"
      class="composer__input"
      rows="2"
      :placeholder="
        channel === 'chat'
          ? '输入要跟她说的话（走 LLM 对话，应答播报上屏），Enter 发送 / Shift+Enter 换行'
          : '输入要说的话（/ 开头发送控制台命令），Enter 发送 / Shift+Enter 换行'
      "
      :disabled="!canSpeak"
      @keydown="keydown"
    />
    <div class="composer__side">
      <NtSelect v-model="channel" label="通道" :options="CHANNELS" />
      <NtSelect v-if="channel === 'say'" v-model="mode" label="模式" :options="MODES" />
    </div>
    <div class="composer__btns">
      <button type="button" class="primary" :disabled="!canSpeak || busy || chatBusy || !text.trim()" @click="send">
        {{ chatBusy ? '对话中…' : '发送' }}
      </button>
      <button type="button" :disabled="busy || chatBusy" @click="emit('interrupt')">打断</button>
      <button type="button" class="danger" :disabled="busy" @click="emit('stop')">停止</button>
    </div>
    <p v-if="hint" class="composer__hint" aria-live="polite">{{ hint }}</p>
    <p v-if="error" class="composer__error" role="alert" aria-live="assertive">{{ error }}</p>
  </div>
</template>

<style scoped>
.composer {
  display: grid;
  grid-template-columns: auto minmax(120px, 1fr) auto auto;
  align-items: end;
  gap: 8px;
  padding: 8px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
}
.composer__voice {
  min-width: 0;
}
.composer__input {
  width: 100%;
  min-width: 0;
  min-height: 64px;
  padding: 8px 10px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
  resize: vertical;
}
.composer__input:focus-visible,
.composer__btns button:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}
.composer__input:disabled {
  opacity: 0.5;
}
.composer__side {
  display: flex;
  gap: 8px;
}
.composer__side :deep(.nt-select) {
  min-width: 84px;
}
.composer__btns {
  display: grid;
  grid-template-columns: repeat(3, minmax(44px, 1fr));
  gap: 6px;
}
.composer__btns button {
  min-width: 44px;
  min-height: 44px;
  padding: 0 6px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.composer__btns .primary {
  border-color: var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
}
.composer__btns .danger {
  border-color: var(--nt-danger);
  color: var(--nt-danger);
}
.composer__btns button:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.composer__error {
  grid-column: 1 / -1;
  margin: 0;
  color: var(--nt-danger);
  font-size: var(--nt-fs-micro);
}
.composer__hint {
  grid-column: 1 / -1;
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
@media (max-width: 799px) {
  .composer {
    grid-template-columns: auto minmax(40px, 1fr) auto;
    align-items: center;
    gap: 6px;
    padding: 6px;
  }
  .composer__voice {
    grid-column: 1;
    grid-row: 1;
  }
  .composer__input {
    grid-column: 2;
    grid-row: 1;
    font-size: 16px;
  }
  .composer__side {
    grid-column: 1 / -1;
    grid-row: 2;
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .composer__btns {
    grid-column: 3;
    grid-row: 1;
  }
  .composer__side :deep(select) {
    min-height: 44px;
    font-size: 16px;
  }
}
</style>

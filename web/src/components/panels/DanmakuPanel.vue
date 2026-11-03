<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { classifyApiError } from '../../services/api/client'
import NtSelect from '../ui/NtSelect.vue'
import { createVisibilityPolling } from '../../utils/polling'
import {
  confirmDanmaku,
  dismissDanmaku,
  getDanmakuState,
  listDanmakuPending,
  setDanmakuMode,
  type DanmakuPendingItem,
  type DanmakuState
} from '../../services/api/panels'

const state = ref<DanmakuState | null>(null)
const pending = ref<DanmakuPendingItem[]>([])
const mode = ref<'off' | 'confirm' | 'auto'>('off')
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const note = ref('')

const MODE_OPTIONS = [
  { label: '关闭', value: 'off' },
  { label: '人工确认', value: 'confirm' },
  { label: '自动播报', value: 'auto' }
]

// 待确认轮询：页面隐藏自动暂停（P2-15 同款可见性策略）。
const poll = createVisibilityPolling(() => void load(), 3000)

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const r = await getDanmakuState()
    state.value = r.state
    mode.value = (r.state.mode as 'off' | 'confirm' | 'auto') || 'off'
    pending.value = await listDanmakuPending()
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

async function applyMode(): Promise<void> {
  busy.value = true
  error.value = ''
  note.value = ''
  try {
    const r = await setDanmakuMode(mode.value)
    note.value = r.changed ? `模式已切换为 ${r.mode}` : `模式未变（${r.mode}）`
    await load()
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    busy.value = false
  }
}

async function decide(id: string, ok: boolean): Promise<void> {
  busy.value = true
  error.value = ''
  note.value = ''
  try {
    if (ok) {
      const r = await confirmDanmaku(id)
      if (!r.ok) throw new Error(r.reason || '确认失败')
      note.value = '已确认并进入播报队列'
    } else {
      const r = await dismissDanmaku(id)
      if (!r.ok) throw new Error('忽略失败')
      note.value = '已忽略该条弹幕'
    }
    pending.value = await listDanmakuPending()
  } catch (err) {
    error.value = classifyApiError(err).message
    await load().catch(() => undefined)
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  void load()
  poll.start()
})

onBeforeUnmount(() => {
  poll.stop()
})
</script>

<template>
  <div class="danmaku">
    <p v-if="loading && !state" class="note">正在读取弹幕状态…</p>
    <p v-else-if="error" class="error" role="alert" aria-live="assertive">{{ error }}</p>
    <p v-if="note" class="ok" aria-live="polite">{{ note }}</p>

    <div class="bar">
      <span class="mono">
        {{ state?.enabled ? '已开启' : '已关闭' }} · 待确认 {{ pending.length }} · 策略 {{ state?.mode || mode }}
      </span>
      <button type="button" class="mini" :disabled="loading" @click="load">刷新</button>
    </div>

    <div class="row">
      <NtSelect label="播报策略" :model-value="mode" :options="MODE_OPTIONS" @update:model-value="mode = $event as 'off' | 'confirm' | 'auto'" />
      <button type="button" class="mini" :disabled="busy" @click="applyMode">应用模式</button>
    </div>
    <p class="note">自动模式会直接进入播报队列；人工确认模式需逐条确认（采集器仍走 /api/danmaku）。</p>

    <p v-if="pending.length === 0" class="note">暂无待确认弹幕。</p>
    <ul v-else class="list">
      <li v-for="item in pending" :key="item.id">
        <span class="who">{{ item.userName || '匿名' }}</span>
        <span class="text">{{ item.text }}</span>
        <span class="btns">
          <button type="button" class="mini" :disabled="busy" @click="decide(item.id, true)">确认</button>
          <button type="button" class="mini" :disabled="busy" @click="decide(item.id, false)">忽略</button>
        </span>
      </li>
    </ul>

    <p v-if="state?.recent?.length" class="note">最近入库：{{ state.recent.join(' / ') }}</p>
    <p v-if="state?.top?.length" class="note">
      高频词：<span v-for="(w, i) in state.top" :key="`${w.text}-${i}`" class="tag">{{ w.text }}×{{ w.count }}</span>
    </p>
  </div>
</template>

<style scoped>
.danmaku {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.note,
.ok,
.error {
  margin: 0;
  font-size: var(--nt-fs-micro);
  line-height: 1.5;
  color: var(--nt-text-3);
}
.ok {
  color: var(--nt-text-1);
}
.error {
  color: var(--nt-danger);
}
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.row {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.list li {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
}
.who {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
  white-space: nowrap;
}
.text {
  overflow: hidden;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-caption);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.btns {
  display: flex;
  gap: 4px;
}
.mini {
  height: 30px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.mini:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.tag {
  display: inline-block;
  margin: 0 4px;
  padding: 0 6px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  color: var(--nt-text-2);
}
.mono {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
</style>

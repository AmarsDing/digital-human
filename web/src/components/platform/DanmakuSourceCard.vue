<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { usePlatformStore } from '../../stores/platform'
import StatusCapsule from './StatusCapsule.vue'

// 弹幕源：外部中转采集抖音房号弹幕；degraded 时提示回舞台页手动互动。
const store = usePlatformStore()
const roomId = ref('')
const savedNote = ref('')
const now = ref(Date.now())

const LABELS: Record<string, string> = {
  off: '未开启',
  connecting: '连接中',
  collecting: '采集中',
  degraded: '已降级',
  stopped: '已停止'
}
const VARIANTS: Record<string, 'neutral' | 'info' | 'success' | 'warn' | 'danger'> = {
  off: 'neutral',
  connecting: 'info',
  collecting: 'success',
  degraded: 'warn',
  stopped: 'neutral'
}

const stateLabel = computed(() => LABELS[store.collectorState] ?? store.collectorState)
const variant = computed(() => VARIANTS[store.collectorState] ?? 'neutral')
const degraded = computed(() => store.collector?.state === 'degraded')

// 每次状态刷新推进基准时间，相对时间随轮询自然更新。
watch(
  () => store.collector?.lastMsgMs,
  () => {
    now.value = Date.now()
  }
)

const lastMsgAgo = computed(() => {
  const ms = store.collector?.lastMsgMs ?? 0
  if (!ms) return '—'
  const s = Math.max(0, Math.floor((now.value - ms) / 1000))
  if (s < 60) return `${s} 秒前`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分钟前`
  return `${Math.floor(m / 60)} 小时前`
})

const canSave = computed(() => !store.collectorUnavailable && !store.roomBusy && Boolean(roomId.value.trim()))

async function save(): Promise<void> {
  if (!canSave.value) return
  savedNote.value = ''
  const ok = await store.saveRoom(roomId.value.trim())
  if (ok) savedNote.value = '已保存'
}
</script>

<template>
  <section class="pf-card" aria-label="弹幕源">
    <div class="pf-card__head">
      <h2 class="pf-card__title">弹幕源</h2>
      <StatusCapsule :variant="variant" :label="stateLabel" />
    </div>

    <p v-if="store.collectorLoading" class="pf-empty">加载中…</p>
    <div v-else-if="store.collectorError" class="pf-error" role="alert">
      <span>{{ store.collectorError }}</span>
      <button type="button" @click="void store.refreshCollector()">重试</button>
    </div>
    <template v-else>
      <div class="pf-metrics pf-mono">
        <div class="pf-metric">
          <b>{{ store.collector?.roomId || '—' }}</b>
          <span>房号</span>
        </div>
        <div class="pf-metric">
          <b>{{ lastMsgAgo }}</b>
          <span>最近消息</span>
        </div>
        <div class="pf-metric">
          <b>{{ store.collector?.msgsPerMin ?? '—' }}</b>
          <span>每分钟条数</span>
        </div>
      </div>
      <p v-if="degraded" class="pf-error" role="alert">弹幕源异常，可在舞台页手动互动</p>
    </template>

    <div class="pf-form-row">
      <label class="pf-field">
        <span>抖音房号</span>
        <input v-model="roomId" class="pf-input pf-mono" type="text" placeholder="直播间号" inputmode="numeric" @keyup.enter="save" />
      </label>
      <button type="button" class="pf-btn pf-btn--secondary" :disabled="!canSave" @click="save">
        {{ store.roomBusy ? '保存中…' : '保存' }}
      </button>
    </div>
    <p v-if="savedNote" class="pf-note">{{ savedNote }}</p>
    <p v-if="store.roomError" class="pf-error" role="alert">{{ store.roomError }}</p>
  </section>
</template>

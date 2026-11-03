<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { createRtcAdapter, RtcSdkMissingError } from './adapter'
import type { RtcAdapter, RtcCreds, RtcState } from './types'

const props = defineProps<{ creds: RtcCreds | null }>()
const emit = defineEmits<{
  (e: 'state', v: RtcState): void
  (e: 'error', message: string): void
  (e: 'autoplay-blocked'): void
  (e: 'expired'): void
}>()

const video = ref<HTMLVideoElement | null>(null)
const state = ref<RtcState>('unconfigured')
const message = ref('')
const muted = ref(true)
const remainS = ref(0)
/** G30：最后一次成功渲染的视频帧（dataURL），收播/断流时铺底防黑屏。 */
const lastFrame = ref('')
let adapter: RtcAdapter | null = null
let timer: ReturnType<typeof setInterval> | null = null

const alive = computed(() => state.value === 'joined')

function setState(next: RtcState): void {
  state.value = next
  emit('state', next)
}

/** G30：把 video 当前帧截为 dataURL；jsdom/未渲染帧时静默跳过。 */
function captureLastFrame(): void {
  const el = video.value
  if (!el || !el.videoWidth || !el.videoHeight) return
  try {
    const canvas = document.createElement('canvas')
    canvas.width = el.videoWidth
    canvas.height = el.videoHeight
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.drawImage(el, 0, 0)
    lastFrame.value = canvas.toDataURL('image/jpeg', 0.7)
  } catch {
    /* 截帧失败不阻断收播流程 */
  }
}

async function join(): Promise<void> {
  if (adapter) captureLastFrame() // G30：旧流拆除前先截住最后一帧
  await leave()
  const creds = props.creds
  if (!creds || !creds.token) {
    message.value = 'RTC 未配置（后端未下发凭据）'
    setState('unconfigured')
    return
  }
  setState('joining')
  message.value = ''
  try {
    adapter = createRtcAdapter(creds.provider)
    const el = video.value
    if (!el) throw new Error('video 元素未就绪')
    await adapter.join(creds, el)
    el.muted = muted.value
    lastFrame.value = '' // 新流就绪，清掉旧帧
    setState('joined')
    startCountdown(creds.tokenExpireAt)
  } catch (err) {
    message.value = err instanceof RtcSdkMissingError ? err.message : String(err)
    emit('error', message.value)
    setState('failed')
  }
}

async function leave(): Promise<void> {
  stopCountdown()
  if (adapter) {
    try {
      await adapter.leave()
      adapter.destroy()
    } catch {
      // 离会失败不应阻断本地清理
    }
    adapter = null
  }
}

async function enableSound(): Promise<void> {
  const el = video.value
  if (!el) return
  muted.value = false
  try {
    await el.play()
  } catch {
    emit('autoplay-blocked')
  }
}

function mute(on: boolean): void {
  muted.value = on
  if (video.value) video.value.muted = on
  adapter?.mute(on)
}

function startCountdown(expireAt: number): void {
  stopCountdown()
  if (!expireAt) return
  const tick = (): void => {
    remainS.value = Math.max(0, Math.round((expireAt - Date.now()) / 1000))
    if (remainS.value === 0) {
      stopCountdown()
      message.value = 'RTC token 已过期'
      setState('failed')
      emit('expired')
    }
  }
  tick()
  timer = setInterval(tick, 1000)
}

function stopCountdown(): void {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

watch(
  () => props.creds,
  () => void join(),
  { immediate: true, deep: true }
)

onBeforeUnmount(() => {
  void leave()
})

defineExpose({ join, leave, mute, state })
</script>

<template>
  <div class="rtc" :data-state="state">
    <video ref="video" class="rtc__video" playsinline autoplay muted />

    <!-- G30 末帧保留：收播/断流后画面停在最后一帧，状态提示叠玻璃层而非黑屏 -->
    <img v-if="lastFrame && !alive" class="rtc__lastframe" :src="lastFrame" alt="" aria-hidden="true" />

    <div v-if="!alive" class="rtc__overlay" :class="{ 'rtc__overlay--glass': !!lastFrame }">
      <div class="rtc__badge">{{ state === 'joining' ? '正在入会…' : state === 'failed' ? '入会失败' : '未入会' }}</div>
      <p class="rtc__tip">{{ message || '等待开播后下发 RTC 凭证' }}</p>
      <button v-if="state === 'failed'" type="button" class="rtc__btn" @click="join">重试</button>
    </div>

    <button v-if="alive" type="button" class="rtc__sound" @click="enableSound">
      点击启用声音
    </button>

    <div class="rtc__foot">
      <span v-if="alive && remainS > 0" class="rtc__countdown">
        token {{ Math.floor(remainS / 60) }}:{{ String(remainS % 60).padStart(2, '0') }}
      </span>
      <span v-else class="rtc__countdown">{{ state }}</span>
      <button v-if="alive" type="button" class="rtc__mute" @click="mute(!muted)">
        {{ muted ? '取消静音' : '静音' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.rtc {
  position: relative;
  width: 100%;
  height: 100%;
  min-height: 240px;
  background:
    radial-gradient(ellipse at 30% 20%, var(--nt-space-cyan), transparent 60%),
    var(--nt-bg-0);
  overflow: hidden;
}
.rtc__video {
  width: 100%;
  height: 100%;
  object-fit: contain;
  background: transparent;
}
.rtc__lastframe {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: contain;
}
.rtc__overlay--glass {
  background: var(--nt-bg-glass);
  backdrop-filter: var(--nt-glass-filter);
}
.rtc__overlay {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  background: linear-gradient(180deg, transparent, var(--nt-scrim-light));
  color: var(--nt-text-2);
}
.rtc__badge {
  padding: 4px 12px;
  border-radius: 999px;
  border: 1px solid var(--nt-border-2);
  background: var(--nt-data-bg);
  color: var(--nt-text-1);
  font-size: var(--nt-fs-caption);
}
.rtc__tip {
  margin: 0;
  max-width: 80%;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
  text-align: center;
}
.rtc__btn,
.rtc__mute,
.rtc__sound {
  height: 30px;
  padding: 0 12px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.rtc__sound {
  position: absolute;
  left: 50%;
  top: 16px;
  transform: translateX(-50%);
  border-color: var(--nt-accent-border);
  color: var(--nt-accent);
}
.rtc__foot {
  position: absolute;
  right: 12px;
  bottom: 10px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.rtc__countdown {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
</style>

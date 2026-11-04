<script setup lang="ts">
import { computed } from 'vue'
import RtcPlayer from '../rtc/RtcPlayer.vue'
import type { RtcCreds } from '../rtc/types'
import { phaseLabel as phaseText } from '../../utils/labels'
import LiveStageBadge from './LiveStageBadge.vue'

const props = defineProps<{
  phase: string
  liveId: string
  durationS: number
  creds: RtcCreds | null
  rtcNotice?: string
  wsState: string
  lagMs: number
  feedOwner: boolean
  degraded: boolean
  caption: string
  captionsEnabled: boolean
  captionFontSize: number
  captionMaxLines: number
  captionOffsetPx: number
  busy: boolean
  error: string
}>()
const emit = defineEmits<{
  (e: 'start'): void
  (e: 'stop'): void
  (e: 'open-settings'): void
  (e: 'rtc-state', state: string): void
}>()

const phaseLabel = computed(() => phaseText(props.phase))

// 连点防护：busy（请求在途）或 phase 已进入 starting 时禁按。
const canStart = computed(
  () => !props.busy && (props.phase === 'idle' || props.phase === 'ended' || props.phase === 'error')
)
const canStop = computed(() => !props.busy && (props.phase === 'starting' || props.phase === 'live'))

const badgeStatus = computed(() => (props.degraded ? 'degraded' : 'live'))
</script>

<template>
  <section class="stage">
    <header class="stage__bar">
      <div class="stage__left">
        <span v-if="phase !== 'live'" class="stage__pill" :data-phase="phase">{{ phaseLabel }}</span>
        <span v-if="liveId" class="stage__id">{{ liveId }}</span>
      </div>
      <div class="stage__right">
        <span class="stage__chip" :class="{ 'stage__chip--warn': degraded }">WS {{ wsState || '—' }}</span>
        <span class="stage__chip" :class="{ 'stage__chip--warn': lagMs > 300 }">延迟 {{ lagMs }}ms</span>
        <span class="stage__chip" :class="{ 'stage__chip--warn': !feedOwner && phase === 'live' }">
          喂流 {{ feedOwner ? '持有' : '未持有' }}
        </span>
        <button type="button" class="stage__icon" aria-label="设置" @click="emit('open-settings')">⚙</button>
      </div>
    </header>

    <div class="stage__viewport">
      <LiveStageBadge
        v-if="phase === 'live'"
        class="stage__live-badge"
        :status="badgeStatus"
        :elapsed-seconds="durationS"
        :data-phase="phase"
      />
      <RtcPlayer :creds="creds" @state="emit('rtc-state', $event)" />
      <div
        v-if="captionsEnabled && caption"
        class="stage__caption"
        aria-live="polite"
        :style="{
          bottom: `${Math.max(0, captionOffsetPx)}px`,
          fontSize: `${captionFontSize}px`,
          WebkitLineClamp: captionMaxLines
        }"
      >
        {{ caption }}
      </div>
    </div>

    <footer class="stage__foot">
      <div class="stage__actions">
        <button
          type="button"
          class="btn btn--primary"
          :disabled="!canStart"
          :aria-busy="busy"
          @click="emit('start')"
        >
          {{ busy || phase === 'starting' ? '连线中…' : '开始直播（F9）' }}
        </button>
        <button type="button" class="btn" :disabled="!canStop" @click="emit('stop')">停止直播（F10）</button>
      </div>
      <p v-if="error" class="stage__error" role="alert" aria-live="assertive">{{ error }}</p>
      <p v-if="phase === 'live' && !creds" class="stage__hint">
        {{ rtcNotice || 'RTC 未配置（后端未下发凭据），画面暂不可用。' }}
      </p>
    </footer>
  </section>
</template>

<style scoped>
.stage {
  position: relative;
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
  overflow: hidden;
}
.stage__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--nt-border-1);
}
.stage__left,
.stage__right {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  min-width: 0;
}
.stage__pill {
  padding: 3px 10px;
  border-radius: 999px;
  border: 1px solid var(--nt-border-1);
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
}
.stage__pill[data-phase='live'] {
  border-color: var(--nt-success);
  color: var(--nt-success);
}
.stage__pill[data-phase='error'] {
  border-color: var(--nt-danger);
  color: var(--nt-danger);
}
.stage__id {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
.stage__dur {
  color: var(--nt-text-1);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-num-lg);
}
.stage__chip {
  padding: 2px 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.stage__chip--warn {
  border-color: var(--nt-warn);
  color: var(--nt-warn);
}
.stage__icon {
  width: 44px;
  height: 44px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: transparent;
  color: var(--nt-text-2);
  cursor: pointer;
}
.stage__viewport {
  position: relative;
  flex: 1;
  min-height: 320px;
  background: var(--nt-bg-0);
}
.stage__live-badge {
  position: absolute;
  z-index: 3;
  top: 12px;
  left: 12px;
  pointer-events: none;
}
.stage__caption {
  position: absolute;
  left: 50%;
  transform: translateX(-50%);
  max-width: 82%;
  padding: 4px 10px;
  border-radius: var(--nt-radius-s);
  background: var(--nt-scrim);
  color: var(--nt-text-0);
  line-height: 1.45;
  display: -webkit-box;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.stage__foot {
  position: sticky;
  bottom: 0;
  z-index: 2;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  flex: none;
  gap: 12px;
  padding: 8px 12px;
  border-top: 1px solid var(--nt-border-1);
  background: var(--nt-data-bg);
}
.stage__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.btn {
  min-height: 44px;
  padding: 0 14px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.btn--primary {
  border-color: var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
}
.btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.stage__error {
  margin: 0;
  color: var(--nt-danger);
  font-size: var(--nt-fs-micro);
}
.stage__hint {
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
@media (max-width: 1023px) {
  .stage__viewport {
    min-height: 0;
  }
  .stage__foot {
    position: absolute;
    right: 0;
    bottom: 0;
    left: 0;
  }
}
@media (max-width: 767px) {
  .stage__bar {
    align-items: flex-start;
    flex-wrap: wrap;
  }
  .stage__viewport {
    min-height: 180px;
  }
}
@media (max-width: 359px) {
  .stage__viewport {
    min-height: 0;
  }
  .stage__actions {
    width: 100%;
  }
  .stage__actions .btn {
    flex: 1;
    padding: 0 8px;
  }
}
</style>

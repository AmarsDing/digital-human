<script setup lang="ts">
import { computed } from 'vue'
import { usePlatformStore } from '../../stores/platform'
import { useStopControlStore } from '../../stores/stopControl'
import StatusCapsule from './StatusCapsule.vue'
import NtConfirmDialog from '../ui/NtConfirmDialog.vue'

const store = usePlatformStore()
const stopCtl = useStopControlStore()

const LABELS: Record<string, string> = {
  idle: '未推流',
  starting: '启动中',
  pushing: '推流中',
  degraded: '推流降级',
  rejected: '被拒绝',
  stopped: '已停止'
}
const VARIANTS: Record<string, 'neutral' | 'info' | 'success' | 'warn' | 'danger'> = {
  idle: 'neutral',
  starting: 'info',
  pushing: 'success',
  degraded: 'warn',
  rejected: 'danger',
  stopped: 'neutral'
}

const stateLabel = computed(() => LABELS[store.streamState] ?? store.streamState)
const variant = computed(() => VARIANTS[store.streamState] ?? 'neutral')
// starting/pushing 禁止重复点开始；degraded 可重试。
const startDisabled = computed(() => store.controlBusy || store.streamBusy)
const stopDisabled = computed(() => store.controlBusy || !store.streamStoppable)
</script>

<template>
  <section class="pf-card" aria-label="推流控制">
    <div class="pf-card__head">
      <h2 class="pf-card__title">推流控制</h2>
      <StatusCapsule :variant="variant" :label="stateLabel" />
    </div>

    <p v-if="store.streamLoading" class="pf-empty">加载中…</p>
    <div v-else-if="store.streamError" class="pf-error" role="alert">
      <span>{{ store.streamError }}</span>
      <button type="button" @click="void store.refreshStream()">重试</button>
    </div>
    <p v-else-if="!store.stream" class="pf-empty">暂无推流数据</p>
    <template v-else>
      <div class="pf-metrics pf-mono">
        <div class="pf-metric">
          <b>{{ store.stream.bitrateKbps }}</b>
          <span>码率 kbps</span>
        </div>
        <div class="pf-metric">
          <b>{{ store.stream.fps }}</b>
          <span>帧率 fps</span>
        </div>
        <div class="pf-metric">
          <b>{{ store.stream.droppedFrames }}</b>
          <span>丢帧</span>
        </div>
      </div>
      <p v-if="store.stream.lastError" class="pf-error" role="alert">最近错误：{{ store.stream.lastError }}</p>
    </template>

    <div class="pf-actions">
      <button type="button" class="pf-btn pf-btn--primary" :disabled="startDisabled" @click="void store.start()">
        {{ store.streamState === 'starting' ? '启动中…' : '开始推流' }}
      </button>
      <button type="button" class="pf-btn pf-btn--danger" :disabled="stopDisabled" @click="stopCtl.requestStopStream('stream-card')">停止推流</button>
    </div>
    <p v-if="store.controlError" class="pf-error" role="alert">{{ store.controlError }}</p>

    <!-- 唯一停推控制器确认框（v2 §3.2）：停推不自动下播。 -->
    <NtConfirmDialog
      :open="stopCtl.streamConfirmOpen"
      dangerous
      :busy="stopCtl.streamStopping"
      :error="stopCtl.streamError"
      title="停止推流确认"
      message="将停止当前推流。停推不会自动下播，直播场次仍继续进行。确定停止推流？"
      confirm-text="确认停推"
      @confirm="void stopCtl.confirmStopStream()"
      @cancel="stopCtl.cancelStopStream()"
    />
  </section>
</template>

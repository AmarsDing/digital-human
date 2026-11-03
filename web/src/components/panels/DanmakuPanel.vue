<script setup lang="ts">
import { ref } from 'vue'
import NtSelect from '../ui/NtSelect.vue'
import { useDanmakuRuntimeStore } from '../../stores/danmakuRuntime'

// 弹幕面板只是选择器（v2 §2.2）：轮询/pending/mode 归 danmakuRuntime store，
// 组件卸载不停止轮询、不丢待确认数。本地 mode 仅为未提交的界面选择。
const rt = useDanmakuRuntimeStore()
const localMode = ref<'off' | 'confirm' | 'auto' | ''>('')

const MODE_OPTIONS = [
  { label: '关闭', value: 'off' },
  { label: '人工确认', value: 'confirm' },
  { label: '自动播报', value: 'auto' }
]

function applyMode(): void {
  const m = (localMode.value || rt.mode) as 'off' | 'confirm' | 'auto'
  void rt.applyMode(m)
  localMode.value = ''
}
</script>

<template>
  <div class="danmaku">
    <p v-if="rt.requestError" class="error" role="alert" aria-live="assertive">
      {{ rt.requestError }}<template v-if="rt.lastSuccessAt">（最后成功 {{ new Date(rt.lastSuccessAt).toLocaleTimeString() }}）</template>
    </p>
    <p v-if="rt.note" class="ok" aria-live="polite">{{ rt.note }}</p>

    <div class="bar">
      <span class="mono">
        {{ rt.enabled ? '已开启' : '已关闭' }} · 待确认 {{ rt.pendingCount }} · 策略 {{ rt.mode
        }}<template v-if="rt.modeApplying">（提交中/未应用）</template>
      </span>
      <button type="button" class="mini" @click="rt.load()">刷新</button>
    </div>

    <div class="row">
      <NtSelect
        label="播报策略"
        :model-value="localMode || rt.mode"
        :options="MODE_OPTIONS"
        @update:model-value="localMode = $event as 'off' | 'confirm' | 'auto'"
      />
      <button type="button" class="mini" :disabled="rt.busy" @click="applyMode">应用模式</button>
    </div>
    <p class="note">自动模式会直接进入播报队列；人工确认模式需逐条确认（采集器仍走 /api/danmaku）。</p>

    <p v-if="rt.pending.length === 0 && !rt.requestError" class="note">暂无待确认弹幕。</p>
    <ul v-else class="list">
      <li v-for="item in rt.pending" :key="item.id">
        <span class="who">{{ item.userName || '匿名' }}</span>
        <span class="text">{{ item.text }}</span>
        <span class="btns">
          <button type="button" class="mini" :disabled="rt.busy" @click="rt.decide(item.id, true)">确认</button>
          <button type="button" class="mini" :disabled="rt.busy" @click="rt.decide(item.id, false)">忽略</button>
        </span>
      </li>
    </ul>

    <p v-if="rt.recent.length" class="note">最近入库：{{ rt.recent.join(' / ') }}</p>
    <p v-if="rt.top.length" class="note">
      高频词：<span v-for="(w, i) in rt.top" :key="`${w.text}-${i}`" class="tag">{{ w.text }}×{{ w.count }}</span>
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

<script setup lang="ts">
import { onBeforeUnmount, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import StreamTargetCard from '../components/platform/StreamTargetCard.vue'
import StreamControlCard from '../components/platform/StreamControlCard.vue'
import DanmakuSourceCard from '../components/platform/DanmakuSourceCard.vue'
import { usePlatformStore } from '../stores/platform'
import { createVisibilityPolling } from '../utils/polling'

// 平台接入：抖音侧开播在直播伴侣/App 手动完成，本页只控推流与弹幕采集。
const router = useRouter()
const store = usePlatformStore()

// 页面打开期间 2s 轮询两个状态接口；离开页面停止，后台标签页自动暂停。
const poll = createVisibilityPolling(() => {
  void store.refreshStream()
  void store.refreshCollector()
}, 2000)

onMounted(() => {
  void store.refreshStream()
  void store.refreshCollector()
  poll.start()
})

onBeforeUnmount(() => {
  poll.stop()
})
</script>

<template>
  <div class="platform-page">
    <header class="head">
      <button type="button" class="back" @click="router.push('/')">← 返回直播台</button>
      <h1>平台接入</h1>
      <span class="head__hint">推流与弹幕采集</span>
    </header>

    <p class="notice">抖音平台侧开播需在抖音直播伴侣/App 手动完成；本页控制的是推流与弹幕采集。</p>

    <main class="grid">
      <StreamTargetCard />
      <StreamControlCard />
      <DanmakuSourceCard class="grid__wide" />
    </main>

    <footer class="compliance">
      AI 数字人直播需在直播间显著标识 AI 生成内容；第三方弹幕采集存在平台风控风险，由使用者自行承担。
    </footer>
  </div>
</template>

<style scoped>
.platform-page {
  min-width: 320px;
  min-height: 100%;
  padding: 12px 16px 24px;
  background: var(--nt-space-bg);
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.head {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.back {
  min-height: 44px;
  padding: 0 12px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-2);
  font: inherit;
  font-size: var(--nt-fs-body);
  cursor: pointer;
}
.back:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}
.head h1 {
  margin: 0;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-title);
}
.head__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.notice {
  margin: 0;
  padding: 6px 10px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-raised);
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
}
.grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 12px;
  flex: 1;
  align-content: start;
}
/* ≥800px 两卡片并列，弹幕源通栏；<800px 单列。 */
@media (min-width: 800px) {
  .grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .grid__wide {
    grid-column: 1 / -1;
  }
}
.compliance {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
</style>

<!-- 平台接入共享样式（pf-*）：按钮三档语义、输入、卡片、指标背板；取值只走 --nt-* 令牌。
     非 scoped 以便子组件复用，类名带 pf- 前缀避免碰撞。 -->
<style>
.pf-card {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: var(--nt-pad-panel);
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
  min-width: 0;
}
.pf-card__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.pf-card__title {
  margin: 0;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-panel);
}
.pf-tag {
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
}
.pf-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
}
.pf-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  flex: 1;
}
.pf-field > span {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.pf-input {
  width: 100%;
  min-height: 44px;
  padding: 0 12px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
  font-size: 16px; /* 触控输入 16px，防移动端聚焦自动缩放 */
}
.pf-btn {
  min-height: 44px;
  padding: 0 16px;
  border-radius: var(--nt-radius-m);
  border: 1px solid transparent;
  font: inherit;
  font-size: var(--nt-fs-body);
  cursor: pointer;
  transition: var(--nt-transition);
}
.pf-btn--primary {
  background: var(--nt-accent);
  border-color: var(--nt-accent);
  color: var(--nt-on-accent);
}
.pf-btn--primary:hover:not(:disabled) {
  box-shadow: var(--nt-glow-accent);
}
.pf-btn--secondary {
  background: transparent;
  border-color: var(--nt-accent-border);
  color: var(--nt-accent);
}
.pf-btn--secondary:hover:not(:disabled) {
  background: var(--nt-accent-soft);
}
.pf-btn--danger {
  background: transparent;
  border-color: var(--nt-danger-border);
  color: var(--nt-danger);
}
.pf-btn--danger:hover:not(:disabled) {
  background: var(--nt-danger-soft);
}
.pf-btn--ghost {
  background: transparent;
  border-color: var(--nt-border-1);
  color: var(--nt-text-2);
}
.pf-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.pf-btn:focus-visible,
.pf-input:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}
.pf-input:focus-visible {
  outline-offset: 2px;
}
.pf-actions {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.pf-form-row {
  display: flex;
  align-items: flex-end;
  gap: 10px;
}
.pf-error {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 0;
  padding: 6px 10px;
  border: 1px solid var(--nt-danger-border);
  border-radius: var(--nt-radius-m);
  background: var(--nt-danger-soft);
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
.pf-error button {
  min-height: 28px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-danger-border);
  background: transparent;
  color: var(--nt-danger);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.pf-empty {
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.pf-note {
  margin: 0;
  color: var(--nt-success);
  font-size: var(--nt-fs-caption);
}
.pf-mono {
  font-family: var(--nt-font-mono);
  font-variant-numeric: tabular-nums;
}
.pf-metrics {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
}
.pf-metric {
  padding: 8px 10px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-inset);
  min-width: 0;
}
.pf-metric b {
  display: block;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-num-lg);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pf-metric span {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
@media (max-width: 559px) {
  .pf-metrics {
    grid-template-columns: minmax(0, 1fr);
  }
  .pf-form-row {
    flex-direction: column;
    align-items: stretch;
  }
}
</style>

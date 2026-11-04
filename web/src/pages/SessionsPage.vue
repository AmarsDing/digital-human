<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ArchivePanel from '../components/panels/ArchivePanel.vue'

const route = useRoute()
const router = useRouter()
const sessionId = computed(() => (typeof route.params.id === 'string' ? route.params.id : ''))

function openSession(id: string): void {
  void router.push(`/sessions/${encodeURIComponent(id)}`)
}
</script>

<template>
  <div class="page sessions-page">
    <header class="sessions-page__head">
      <div>
        <h1>{{ sessionId ? '场次详情' : '场次复盘' }}</h1>
        <p>查看场次详情、成本与弹幕统计；数据来自当前后端存储。</p>
      </div>
      <div class="sessions-page__actions">
        <button v-if="sessionId" type="button" data-testid="sessions-back" @click="router.push('/sessions')">返回场次列表</button>
        <button type="button" @click="router.push('/')">返回直播台</button>
      </div>
    </header>
    <ArchivePanel :session-id="sessionId" @select="openSession" />
  </div>
</template>

<style scoped>
.sessions-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-height: 100%;
  padding: 20px;
  background: var(--nt-space-bg);
}
.sessions-page__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}
.sessions-page__head h1 {
  margin: 0;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-title);
}
.sessions-page__head p {
  margin: 4px 0 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.sessions-page__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.sessions-page__actions button {
  min-height: 44px;
  padding: 0 12px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  cursor: pointer;
}
.sessions-page__actions button:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}
@media (max-width: 559px) {
  .sessions-page {
    padding: 12px;
  }
}
</style>

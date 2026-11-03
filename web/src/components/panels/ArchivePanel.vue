<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { classifyApiError } from '../../services/api/client'
import NtSelect from '../ui/NtSelect.vue'
import { phaseLabel } from '../../utils/labels'
import {
  costSummary,
  exportSession,
  getDanmakuState,
  getSession,
  listSessions,
  type DanmakuState,
  type SessionDetail,
  type SessionSummary,
  type Totals
} from '../../services/api/panels'

const sessions = ref<SessionSummary[]>([])
const detail = ref<SessionDetail | null>(null)
const totals = ref<Totals[]>([])
const groupBy = ref<'session' | 'account'>('session')
const danmaku = ref<DanmakuState | null>(null)
const topScope = ref('')
const phase = ref('')
const loading = ref(false)
const detailLoading = ref(false)
const error = ref('')

// 分页：每次 20 条，"加载更多"追加；返回满页则假定还有下一页。
const PAGE = 20
const offset = ref(0)
const hasMore = ref(false)

const PHASE_OPTIONS = [
  { label: '全部场次', value: '' },
  { label: '直播中', value: 'live' },
  { label: '已结束', value: 'ended' },
  { label: '异常', value: 'error' }
]
const GROUP_OPTIONS = [
  { label: '按场', value: 'session' },
  { label: '按账号', value: 'account' }
]

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const [rows, cost, dm] = await Promise.all([
      listSessions(PAGE, 0, phase.value),
      costSummary(groupBy.value),
      getDanmakuState()
    ])
    sessions.value = rows
    offset.value = rows.length
    hasMore.value = rows.length >= PAGE
    totals.value = cost.totals ?? []
    danmaku.value = dm.state
    topScope.value = dm.top_scope
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

async function loadMore(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const rows = await listSessions(PAGE, offset.value, phase.value)
    sessions.value = [...sessions.value, ...rows]
    offset.value += rows.length
    hasMore.value = rows.length >= PAGE
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

async function open(id: string): Promise<void> {
  detailLoading.value = true
  error.value = ''
  try {
    detail.value = await getSession(id)
  } catch (err) {
    error.value = classifyApiError(err).message
    detail.value = null
  } finally {
    detailLoading.value = false
  }
}

async function changeGroup(g: 'session' | 'account'): Promise<void> {
  groupBy.value = g
  try {
    const cost = await costSummary(g)
    totals.value = cost.totals ?? []
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

async function exportAs(id: string, format: 'json' | 'csv'): Promise<void> {
  try {
    const blob = await exportSession(id, format)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `session-${id}.${format}`
    a.click()
    URL.revokeObjectURL(url)
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

function duration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return '—'
  const s = Math.round(ms / 1000)
  const m = Math.floor(s / 60)
  return m > 0 ? `${m}m${String(s % 60).padStart(2, '0')}s` : `${s}s`
}

function credits(v?: number | null): string {
  return v === undefined || v === null ? '—' : String(v)
}

onMounted(load)
</script>

<template>
  <div class="archive">
    <div class="bar">
      <NtSelect label="场次" :model-value="phase" :options="PHASE_OPTIONS" @update:model-value="phase = $event; void load()" />
      <button type="button" class="mini" :disabled="loading" @click="load">刷新</button>
    </div>
    <p v-if="loading && sessions.length === 0" class="note">正在读取场次…</p>
    <p v-else-if="error" class="error" role="alert" aria-live="assertive">{{ error }}</p>
    <p v-if="!loading && sessions.length === 0" class="note">
      暂无场次记录。开播后场次会写入当前存储（内存 sink 重启即清空）。
    </p>
    <template v-else>
      <ul class="list">
        <li v-for="s in sessions" :key="s.id" :class="{ active: detail?.session.id === s.id }">
          <button type="button" class="session" @click="open(s.id)">
            <span class="mono">{{ s.id.slice(0, 8) }}</span>
            <span :title="s.phase">{{ phaseLabel(s.phase) }}</span>
            <span class="mono">{{ duration(s.duration_s * 1000) }}</span>
            <span class="mono">积分 {{ credits(s.cost?.credits_spent) }}</span>
          </button>
        </li>
      </ul>
      <button v-if="hasMore" type="button" class="mini" :disabled="loading" @click="loadMore">
        {{ loading ? '加载中…' : '加载更多' }}
      </button>
    </template>

    <section class="detail">
      <p v-if="detailLoading" class="note">正在读取场次详情…</p>
      <template v-else-if="detail">
        <div class="bar">
          <span class="mono">{{ detail.session.id }}</span>
          <span class="mono">{{ detail.event_count }} 事件</span>
        </div>
        <p class="note">
          阶段 <span :title="detail.session.phase">{{ phaseLabel(detail.session.phase) }}</span> · 时长
          {{ duration(detail.session.duration_s * 1000) }} ·
          live_id {{ detail.session.live_id || '—' }}
        </p>
        <p class="note">
          积分 {{ credits(detail.cost?.credits_start) }} → {{ credits(detail.cost?.credits_end) }}（消耗
          {{ credits(detail.cost?.credits_spent) }}）· tokens {{ credits(detail.cost?.tokens_total) }} ·
          LLM {{ credits(detail.cost?.llm_calls) }} 次
        </p>
        <div class="row btns">
          <button type="button" class="mini" @click="exportAs(detail.session.id, 'json')">导出 JSON</button>
          <button type="button" class="mini" @click="exportAs(detail.session.id, 'csv')">导出 CSV</button>
        </div>
        <p v-if="!detail.cost" class="note">该场无成本账本（未调用 /api/v1/cost/ledger 上报）。</p>
      </template>
      <p v-else class="note">选择一场查看详情与导出。</p>
    </section>

    <section class="cost">
      <div class="bar">
        <span class="mono">成本汇总</span>
        <NtSelect label="汇总维度" :model-value="groupBy" :options="GROUP_OPTIONS" @update:model-value="changeGroup($event as 'session' | 'account')" />
      </div>
      <p v-if="totals.length === 0" class="note">暂无账本汇总。</p>
      <ul v-else class="totals">
        <li v-for="t in totals" :key="t.key">
          <span class="mono">{{ t.key.slice(0, 12) }}</span>
          <span>积分 {{ t.credits_spent }}</span>
          <span>tokens {{ t.tokens_total }}</span>
          <span>钻石 {{ t.diamonds }}</span>
        </li>
      </ul>
    </section>

    <section class="top">
      <div class="bar">
        <span class="mono">弹幕 Top10（{{ topScope || 'calendar_day' }}）</span>
      </div>
      <p v-if="!danmaku || danmaku.top.length === 0" class="note">今日暂无手动指令弹幕。</p>
      <ol v-else class="toplist">
        <li v-for="(t, i) in danmaku.top" :key="`${t.text}-${i}`">
          <span class="rank">#{{ i + 1 }}</span>
          <span class="text">{{ t.text }}</span>
          <span class="mono">{{ t.count }}</span>
        </li>
      </ol>
    </section>
  </div>
</template>

<style scoped>
.archive {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.note,
.error {
  margin: 0;
  font-size: var(--nt-fs-micro);
  line-height: 1.5;
  color: var(--nt-text-3);
}
.error {
  color: var(--nt-danger);
}
.list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 160px;
  overflow: auto;
}
.session {
  display: grid;
  grid-template-columns: auto auto 1fr auto;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 5px 6px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  text-align: left;
  cursor: pointer;
}
.active .session {
  border-color: var(--nt-accent-border);
  color: var(--nt-text-0);
}
.detail,
.cost,
.top {
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding-top: 7px;
  border-top: 1px solid var(--nt-border-1);
}
.row {
  display: flex;
  gap: 6px;
}
.totals,
.toplist {
  display: flex;
  flex-direction: column;
  gap: 3px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.totals li {
  display: flex;
  justify-content: space-between;
  gap: 6px;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
}
.toplist li {
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr) auto;
  gap: 6px;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
}
.rank {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
}
.text {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.mini {
  height: 28px;
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
.mono {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
</style>

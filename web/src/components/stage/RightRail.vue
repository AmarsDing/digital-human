<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { TranscriptLine } from '../../stores/transcript'
import type { WireEvent } from '../../services/api/types'
import { eventTypeLabel } from '../../utils/labels'
import { createVisibilityPolling } from '../../utils/polling'

const props = defineProps<{
  lines: TranscriptLine[]
  events: WireEvent[]
  freshMs: number
  protocol: string
  showEvents?: boolean
}>()
const emit = defineEmits<{ (e: 'clear-transcript'): void }>()

const tab = ref<'transcript' | 'events'>('transcript')
const focused = ref<'transcript' | 'events'>('transcript')

watch(
  () => props.showEvents,
  (show) => {
    if (!show) {
      tab.value = 'transcript'
      focused.value = 'transcript'
    }
  }
)

async function moveFocus(event: KeyboardEvent, currentId: 'transcript' | 'events'): Promise<void> {
  if (!props.showEvents) return
  const ids = ['transcript', 'events'] as const
  const index = ids.indexOf(currentId)
  let next = index
  if (event.key === 'ArrowRight') next = (index + 1) % ids.length
  else if (event.key === 'ArrowLeft') next = (index - 1 + ids.length) % ids.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = ids.length - 1
  else return
  event.preventDefault()
  const current = event.currentTarget as HTMLElement
  focused.value = ids[next]
  await nextTick()
  current.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')?.[next]?.focus()
}

// 新鲜度 1s 心跳（页面隐藏自动暂停）：freshMs 是父级渲染时刻的快照，
// 这里记录基准时刻并按心跳累加，文案每秒刷新。
const now = ref(Date.now())
const baseAt = ref(Date.now())
watch(
  () => props.freshMs,
  () => {
    baseAt.value = Date.now()
  }
)
const ticker = createVisibilityPolling(() => {
  now.value = Date.now()
}, 1000)
onMounted(() => ticker.start())
onBeforeUnmount(() => ticker.stop())

const fresh = computed(() => {
  if (props.freshMs < 0) return '未收到事件'
  const age = props.freshMs + Math.max(0, now.value - baseAt.value)
  return age < 1000 ? '刚刚' : `${Math.round(age / 1000)}s 前`
})

function clock(ts: number): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`
}
</script>

<template>
  <aside class="rail">
    <header class="rail__head">
      <div class="rail__tabs" role="tablist" aria-label="字幕与事件">
        <button
          id="rail-tab-transcript"
          type="button"
          role="tab"
          aria-controls="rail-panel-transcript"
          :aria-selected="tab === 'transcript'"
          :tabindex="focused === 'transcript' ? 0 : -1"
          :class="{ 'rail__tab--on': tab === 'transcript' }"
          @focus="focused = 'transcript'"
          @click="tab = 'transcript'"
          @keydown="moveFocus($event, 'transcript')"
          @keydown.enter.prevent="tab = 'transcript'"
          @keydown.space.prevent="tab = 'transcript'"
        >
          字幕
        </button>
        <button
          v-if="showEvents"
          id="rail-tab-events"
          type="button"
          role="tab"
          aria-controls="rail-panel-events"
          :aria-selected="tab === 'events'"
          :tabindex="focused === 'events' ? 0 : -1"
          :class="{ 'rail__tab--on': tab === 'events' }"
          @focus="focused = 'events'"
          @click="tab = 'events'"
          @keydown="moveFocus($event, 'events')"
          @keydown.enter.prevent="tab = 'events'"
          @keydown.space.prevent="tab = 'events'"
        >
          事件
        </button>
      </div>
      <span class="rail__fresh">{{ fresh }}</span>
    </header>

    <div
      v-if="tab === 'transcript' || !showEvents"
      id="rail-panel-transcript"
      class="rail__body"
      role="tabpanel"
      aria-labelledby="rail-tab-transcript"
    >
      <div class="rail__toolbar">
        <span class="rail__meta">{{ lines.length }} 条 · 上限 200</span>
        <button type="button" @click="emit('clear-transcript')">清空</button>
      </div>
      <ul class="lines" aria-live="polite">
        <li v-for="l in lines.slice().reverse()" :key="l.id" :class="{ 'line--partial': !l.final }">
          <span class="line__ts">{{ clock(l.tsMs) }}</span>
          <span class="line__text">{{ l.text }}</span>
        </li>
        <li v-if="!lines.length" class="line--empty">暂无字幕（等待 transcript 事件）</li>
      </ul>
    </div>

    <div v-else id="rail-panel-events" class="rail__body" role="tabpanel" aria-labelledby="rail-tab-events">
      <div class="rail__toolbar">
        <span class="rail__meta">{{ events.length }} 条 · {{ protocol }}</span>
      </div>
      <ul class="events">
        <li v-for="e in events.slice().reverse()" :key="e.event_id">
          <span class="event__seq">#{{ e.seq }}</span>
          <span class="event__type" :title="e.type">{{ eventTypeLabel(e.type) }}</span>
          <span class="event__time">{{ clock(e.ts_ms) }}</span>
          <span v-if="e.request_id" class="event__rid" :title="e.request_id">rid:{{ e.request_id.slice(-6) }}</span>
        </li>
        <li v-if="!events.length" class="line--empty">暂无事件</li>
      </ul>
    </div>
  </aside>
</template>

<style scoped>
.rail {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
  overflow: hidden;
}
.rail__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 4px;
  padding: 6px 10px;
  border-bottom: 1px solid var(--nt-border-1);
}
.rail__tabs {
  display: flex;
  gap: 4px;
}
.rail__tabs button {
  min-width: 44px;
  min-height: 44px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid transparent;
  background: transparent;
  color: var(--nt-text-3);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.rail__tab--on {
  border-color: var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
}
.rail__fresh {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.rail__body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.rail__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 10px;
  border-bottom: 1px solid var(--nt-border-1);
}
.rail__meta {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.rail__toolbar button {
  min-width: 44px;
  min-height: 44px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: transparent;
  color: var(--nt-text-2);
  font: inherit;
  font-size: var(--nt-fs-micro);
  cursor: pointer;
}
.lines,
.events {
  margin: 0;
  padding: 4px 10px;
  list-style: none;
  overflow: auto;
  flex: 1;
  min-height: 0;
}
.lines li {
  display: grid;
  grid-template-columns: 56px minmax(0, 1fr);
  gap: 8px;
  padding: 6px 0;
  border-bottom: 1px dashed var(--nt-border-1);
  color: var(--nt-text-1);
  font-size: var(--nt-fs-caption);
  line-height: 1.5;
}
.line--partial {
  color: var(--nt-text-3);
  font-style: italic;
}
.line__ts,
.event__time {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
.line--empty {
  display: block !important;
  color: var(--nt-text-3) !important;
  font-size: var(--nt-fs-micro);
  text-align: center;
  border: none !important;
}
.events li {
  display: grid;
  grid-template-columns: 42px minmax(0, 1fr) 54px;
  grid-template-areas: 'seq type time' 'seq rid rid';
  gap: 2px 8px;
  padding: 5px 0;
  border-bottom: 1px dashed var(--nt-border-1);
  font-size: var(--nt-fs-micro);
}
.event__seq {
  grid-area: seq;
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
}
.event__type {
  grid-area: type;
  color: var(--nt-text-1);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.event__time {
  grid-area: time;
  text-align: right;
}
.event__rid {
  grid-area: rid;
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
}
</style>

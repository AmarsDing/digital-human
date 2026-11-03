<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import VideoStage from '../components/stage/VideoStage.vue'
import ComposerBar from '../components/stage/ComposerBar.vue'
import RightRail from '../components/stage/RightRail.vue'
import StatsBar from '../components/stage/StatsBar.vue'
import PanelDock from '../components/stage/PanelDock.vue'
import PanelShell from '../components/panels/PanelShell.vue'
import VoicePanel from '../components/panels/VoicePanel.vue'
import CommandPanel from '../components/panels/CommandPanel.vue'
import DanmakuPanel from '../components/panels/DanmakuPanel.vue'
import KouboPanel from '../components/panels/KouboPanel.vue'
import RefsPanel from '../components/panels/RefsPanel.vue'
import SongsPanel from '../components/panels/SongsPanel.vue'
import ArchivePanel from '../components/panels/ArchivePanel.vue'
import SettingsDrawer from '../components/settings/SettingsDrawer.vue'
import NtConfirmDialog from '../components/ui/NtConfirmDialog.vue'
import { useLiveStore } from '../stores/live'
import { useEventsStore } from '../stores/events'
import { useTranscriptStore } from '../stores/transcript'
import { useSettingsStore } from '../stores/settings'
import { useUiPrefs, type PanelId } from '../stores/uiPrefs'
import { useAuthStore } from '../stores/auth'
import type { RtcCreds } from '../components/rtc/types'
import { toCreds, refreshDelay, degradeOnRefreshError } from '../components/rtc/creds'
import { getRtcToken } from '../services/api/live'
import { classifyApiError } from '../services/api/client'
import { getLedger, type Ledger } from '../services/api/panels'
import { createVisibilityPolling } from '../utils/polling'
import { registerStageHotkeys } from '../utils/hotkeys'

const router = useRouter()
const live = useLiveStore()
const events = useEventsStore()
const transcript = useTranscriptStore()
const settings = useSettingsStore()
const prefs = useUiPrefs()
const auth = useAuthStore()

const drawerOpen = ref(false)
const rightOpen = ref(true)
const tokenInput = ref('')
const rtcState = ref('')
const ledger = ref<Ledger | null>(null)
const creds = ref<RtcCreds | null>(null)
const rtcMode = ref<'unknown' | 'unconfigured' | 'managed'>('unknown')
const rtcNotice = ref('')
const composerRef = ref<InstanceType<typeof ComposerBar> | null>(null)
const confirmClear = ref(false)
let refreshTimer: ReturnType<typeof setTimeout> | null = null
let seeded = false
// 状态轮询只在 events 降级（非 ws 模式）时启用；页面隐藏自动暂停。
const statusPoll = createVisibilityPolling(() => void live.load().then(loadCost), 5000)
let unregisterHotkeys: (() => void) | null = null

function clearRefreshTimer(): void {
  if (refreshTimer) {
    clearTimeout(refreshTimer)
    refreshTimer = null
  }
}

// 按后端返回的过期时间提前刷新；失败不丢旧凭据，只透出错误（降级）。
async function loadRtc(): Promise<void> {
  clearRefreshTimer()
  const sid = live.status?.sessionId
  if (!sid) {
    creds.value = null
    rtcMode.value = 'unknown'
    rtcNotice.value = ''
    return
  }
  try {
    const reply = await getRtcToken(sid)
    if (reply.mode !== 'unconfigured' && reply.ok) {
      const next = toCreds(reply)
      creds.value = next
      rtcMode.value = next ? 'managed' : 'unconfigured'
      rtcNotice.value = next ? '' : 'RTC 未配置（后端未下发凭据）'
      const delay = refreshDelay(reply.expiresAt)
      if (delay >= 0) {
        refreshTimer = setTimeout(() => void loadRtc(), delay)
      }
      return
    }
    creds.value = null
    rtcMode.value = 'unconfigured'
    rtcNotice.value = 'RTC 未配置（后端未下发凭据）'
  } catch (err) {
    const degraded = degradeOnRefreshError(creds.value, classifyApiError(err).message)
    creds.value = degraded.creds
    rtcNotice.value = degraded.error
  }
}

const caption = computed(() => {
  const last = transcript.lines[transcript.lines.length - 1]
  return last?.text || ''
})

const captionCfg = computed(() => {
  const g = settings.groupById('captions')
  return {
    enabled: Boolean(g?.values.enabled ?? true),
    fontSize: Number(g?.values.font_size ?? 17),
    maxLines: Number(g?.values.max_lines ?? 2),
    offsetPx: Number(g?.values.offset_px ?? 200),
    showEvents: Boolean(g?.values.show_events ?? false)
  }
})

async function loadCost(): Promise<void> {
  const id = live.status?.sessionId
  if (!id) {
    ledger.value = null
    return
  }
  ledger.value = await getLedger(id).catch(() => null)
}

const stats = computed(() => [
  {
    id: 'credits',
    label: '积分',
    value: ledger.value ? String(ledger.value.credits_spent) : '—',
    hint: ledger.value ? `起始 ${ledger.value.credits_start}` : '本场无账本'
  },
  {
    id: 'tokens',
    label: 'Token',
    value: ledger.value ? String(ledger.value.tokens_total) : '—',
    hint: ledger.value ? `LLM ${ledger.value.llm_calls} 次` : '本场无账本'
  },
  {
    id: 'diamonds',
    label: '钻石',
    value: ledger.value ? String(ledger.value.diamonds) : '—',
    hint: ledger.value ? ledger.value.source : '统计未接入'
  },
  {
    id: 'gifts',
    label: '礼物',
    value: ledger.value ? String(ledger.value.gift_count) : '—',
    hint: ledger.value ? '按场计数' : '统计未接入'
  },
  { id: 'duration', label: '时长', value: live.isLive ? `${live.durationS}s` : '—' },
  { id: 'lag', label: '音频延迟', value: `${live.lagMs}ms`, hint: live.feedOwner ? '持有喂流' : '未持有喂流' }
])

const deckStyle = computed(() => ({ '--rail-w': rightOpen.value ? `${prefs.rightW}px` : '0px' }))

// 事件按序消费：accept 只入队，这里 drain 后逐条派发，批量回放不丢中间事件。
watch(
  () => events.pending.length,
  (n) => {
    if (!n) return
    for (const e of events.drain()) {
      live.applyEvent(e)
      transcript.applyEvent(e)
    }
  }
)

watch(
  () => live.status?.transcripts,
  (lines) => {
    if (!seeded && lines?.length) {
      transcript.seed(lines)
      seeded = true
    }
  },
  { immediate: true }
)

watch(
  () => drawerOpen.value,
  (open) => {
    if (!open) return
    // 已有数据时不再重复 load，避免打开快捷设置冲掉未保存修改。
    if (!settings.groups.length) void settings.load()
  }
)

watch(
  () => live.status?.sessionId,
  (sid) => {
    void loadRtc()
    if (sid) void events.connect(sid)
  },
  { immediate: true }
)

onMounted(() => {
  prefs.load()
  void live.load().then(loadCost)
  void settings.load()
  document.addEventListener('visibilitychange', onVisible)
  unregisterHotkeys = registerStageHotkeys({
    onStart: () => {
      if (live.busy || live.isLive) return
      void start()
    },
    onStop: () => {
      if (live.busy || !live.isLive) return
      void live.stop('manual')
    },
    onFocusComposer: () => composerRef.value?.focusInput()
  })
})

// 5s 状态轮询仅在 WS 降级（非 ws 模式）时兜底。
watch(
  () => events.mode,
  (mode) => {
    if (mode === 'ws') statusPoll.stop()
    else statusPoll.start()
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  statusPoll.stop()
  clearRefreshTimer()
  events.disconnect()
  unregisterHotkeys?.()
  document.removeEventListener('visibilitychange', onVisible)
})

function onVisible(): void {
  if (document.visibilityState === 'visible') {
    void live.load()
    void events.connect(live.status?.sessionId || '')
  }
}

function togglePanel(id: PanelId): void {
  prefs.togglePanel(id)
}

async function start(): Promise<void> {
  const s = settings.groupById('session')
  await live.start({
    personaId: String(settings.groupById('persona')?.values.active_id ?? ''),
    avatarId: String(s?.values.avatar_id ?? ''),
    greeting: String(s?.values.greeting ?? '')
  })
}

function applyToken(): void {
  auth.apply(tokenInput.value)
  void live.load()
  void settings.load()
  void events.disconnect()
  void events.connect(live.status?.sessionId || '')
}

function confirmClearTranscript(): void {
  confirmClear.value = true
}

function doClearTranscript(): void {
  confirmClear.value = false
  void live.clearTranscripts()
  transcript.clear()
}
</script>

<template>
  <div class="page">
    <header class="page__head">
      <div class="page__brand">
        <strong>小雅 · 数字人直播台</strong>
        <span class="page__sub">M3 前端 · 面板已接真实接口，空态/错误态显式呈现</span>
      </div>
      <div class="page__tools">
        <div class="token">
          <input v-model="tokenInput" type="password" placeholder="lan Bearer（本机模式留空）" @keyup.enter="applyToken" />
          <button type="button" @click="applyToken">应用</button>
        </div>
        <button type="button" class="tool" @click="router.push('/settings')">全部设置</button>
        <button type="button" class="tool" @click="drawerOpen = true">快捷设置</button>
      </div>
    </header>

    <div v-if="auth.error" class="auth-banner" role="alert">
      凭证失效，请重新填写 Bearer。
      <button type="button" @click="tokenInput = ''; auth.logout()">知道了</button>
    </div>

    <PanelDock :open="Object.fromEntries(Object.entries(prefs.panels).map(([k, v]) => [k, v.open])) as Record<PanelId, boolean>" :right-open="rightOpen" @toggle="togglePanel" @toggle-right="rightOpen = !rightOpen" />

    <main class="deck" :class="{ 'deck--no-rail': !rightOpen }" :style="deckStyle">
      <div class="deck__stage">
        <VideoStage
          :phase="live.phase"
          :live-id="live.status?.liveId || ''"
          :duration-s="live.durationS"
          :creds="creds"
          :rtc-notice="rtcNotice"
          :ws-state="events.mode === 'ws' ? '在线' : events.mode === 'poll' ? '轮询' : '离线'"
          :lag-ms="live.lagMs"
          :feed-owner="live.feedOwner"
          :degraded="events.degraded"
          :caption="caption"
          :captions-enabled="captionCfg.enabled"
          :caption-font-size="captionCfg.fontSize"
          :caption-max-lines="captionCfg.maxLines"
          :caption-offset-px="captionCfg.offsetPx"
          :busy="live.busy"
          :error="live.error || events.error"
          @start="start"
          @stop="live.stop('manual')"
          @open-settings="drawerOpen = true"
          @rtc-state="rtcState = $event"
        />

        <PanelShell
          v-if="prefs.panels.voice.open"
          title="声音"
          :x="prefs.panels.voice.x"
          :y="prefs.panels.voice.y"
          :collapsed="prefs.panels.voice.collapsed"
          @close="prefs.togglePanel('voice')"
          @move="(x, y) => prefs.movePanel('voice', x, y)"
          @toggle-collapse="prefs.toggleCollapse('voice')"
        >
          <VoicePanel
            :voice-id="String(settings.groupById('voice')?.values.voice_id ?? '')"
            @select="settings.setField('voice', 'voice_id', $event)"
          />
        </PanelShell>

        <PanelShell
          v-if="prefs.panels.command.open"
          title="动作"
          :x="prefs.panels.command.x"
          :y="prefs.panels.command.y"
          :collapsed="prefs.panels.command.collapsed"
          @close="prefs.togglePanel('command')"
          @move="(x, y) => prefs.movePanel('command', x, y)"
          @toggle-collapse="prefs.toggleCollapse('command')"
        >
          <CommandPanel
            :busy="live.busy"
            @stop-actions="live.stopCurrentActions()"
            @clear-actions="live.clearAllActions()"
          />
        </PanelShell>

        <PanelShell
          v-if="prefs.panels.danmaku.open"
          title="弹幕"
          :x="prefs.panels.danmaku.x"
          :y="prefs.panels.danmaku.y"
          :collapsed="prefs.panels.danmaku.collapsed"
          @close="prefs.togglePanel('danmaku')"
          @move="(x, y) => prefs.movePanel('danmaku', x, y)"
          @toggle-collapse="prefs.toggleCollapse('danmaku')"
        >
          <DanmakuPanel />
        </PanelShell>

        <PanelShell
          v-if="prefs.panels.koubo.open"
          title="口播"
          :x="prefs.panels.koubo.x"
          :y="prefs.panels.koubo.y"
          :collapsed="prefs.panels.koubo.collapsed"
          @close="prefs.togglePanel('koubo')"
          @move="(x, y) => prefs.movePanel('koubo', x, y)"
          @toggle-collapse="prefs.toggleCollapse('koubo')"
        >
          <KouboPanel />
        </PanelShell>

        <PanelShell
          v-if="prefs.panels.refs.open"
          title="参考图"
          :x="prefs.panels.refs.x"
          :y="prefs.panels.refs.y"
          :collapsed="prefs.panels.refs.collapsed"
          @close="prefs.togglePanel('refs')"
          @move="(x, y) => prefs.movePanel('refs', x, y)"
          @toggle-collapse="prefs.toggleCollapse('refs')"
        >
          <RefsPanel />
        </PanelShell>

        <PanelShell
          v-if="prefs.panels.songs.open"
          title="歌曲"
          :x="prefs.panels.songs.x"
          :y="prefs.panels.songs.y"
          :collapsed="prefs.panels.songs.collapsed"
          @close="prefs.togglePanel('songs')"
          @move="(x, y) => prefs.movePanel('songs', x, y)"
          @toggle-collapse="prefs.toggleCollapse('songs')"
        >
          <SongsPanel />
        </PanelShell>

        <PanelShell
          v-if="prefs.panels.archive.open"
          title="复盘 · 成本"
          :x="prefs.panels.archive.x"
          :y="prefs.panels.archive.y"
          :width="400"
          :collapsed="prefs.panels.archive.collapsed"
          @close="prefs.togglePanel('archive')"
          @move="(x, y) => prefs.movePanel('archive', x, y)"
          @toggle-collapse="prefs.toggleCollapse('archive')"
        >
          <ArchivePanel />
        </PanelShell>
      </div>

      <RightRail
        v-if="rightOpen"
        class="deck__rail"
        :lines="transcript.lines"
        :events="events.events"
        :fresh-ms="events.fresh"
        :protocol="events.mode"
        :show-events="captionCfg.showEvents"
        @clear-transcript="confirmClearTranscript"
      />
    </main>

    <ComposerBar
      ref="composerRef"
      class="page__composer"
      :can-speak="live.isLive"
      :busy="live.busy"
      :error="live.error"
      @say="(t, m) => live.say(t, m)"
      @command="(t) => live.command(t)"
      @stop="live.stop('manual')"
      @interrupt="live.interrupt()"
    />

    <StatsBar class="page__stats" :items="stats" />

    <SettingsDrawer :open="drawerOpen" @close="drawerOpen = false" />
    <NtConfirmDialog
      :open="confirmClear"
      title="清空字幕"
      message="将清空当前场次全部字幕记录，且不可恢复。确定继续？"
      confirm-text="清空"
      @confirm="doClearTranscript"
      @cancel="confirmClear = false"
    />
    <span class="sr-only">RTC 状态：{{ rtcState }}（{{ rtcMode }}）</span>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-height: 100%;
  padding: 12px 16px 16px;
  background: var(--nt-space-bg);
  overflow-x: hidden;
}
.page__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}
.page__brand {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.page__brand strong {
  color: var(--nt-text-1);
  font-size: var(--nt-fs-title);
}
.page__sub {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.page__tools {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex-wrap: wrap;
  gap: 8px;
  min-width: 0;
}
.token {
  display: flex;
  gap: 4px;
  min-width: 0;
}
.token input {
  width: 190px;
  max-width: 100%;
  height: 28px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
  font-size: var(--nt-fs-micro);
}
.token button,
.tool {
  height: 28px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-2);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.deck {
  display: grid;
  grid-template-columns: minmax(0, 1fr) var(--rail-w, 360px);
  gap: 10px;
  min-height: 0;
  flex: 1;
}
.deck--no-rail {
  grid-template-columns: minmax(0, 1fr);
}
.deck__stage {
  position: relative;
  min-width: 0;
  min-height: 520px;
  display: flex;
}
.deck__rail {
  min-width: 0;
  min-height: 0;
}

/* 中屏：右栏压缩到 300px，舞台仍保留最小可用高度。 */
@media (max-width: 1279px) {
  .deck:not(.deck--no-rail) {
    grid-template-columns: minmax(0, 1fr) 300px;
  }
}

/* 窄屏：右栏折到舞台下方，整页单列，不产生横向溢出。 */
@media (max-width: 1023px) {
  .deck:not(.deck--no-rail) {
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: minmax(380px, 1fr) minmax(150px, 220px);
  }
  .deck__stage {
    min-height: 380px;
  }
}

@media (max-width: 767px) {
  .token input {
    width: 140px;
  }
  .page__stats {
    overflow-x: auto;
  }
}
.deck__stage > :first-child {
  flex: 1;
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
}
.page__composer,
.page__stats {
  flex: none;
}
.auth-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 10px;
  border: 1px solid var(--nt-warn-border);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-raised);
  color: var(--nt-warn);
  font-size: var(--nt-fs-caption);
}
.auth-banner button {
  height: 24px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: transparent;
  color: var(--nt-text-2);
  font: inherit;
  font-size: var(--nt-fs-micro);
  cursor: pointer;
}
</style>

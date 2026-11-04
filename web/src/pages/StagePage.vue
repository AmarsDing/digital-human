<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import VideoStage from '../components/stage/VideoStage.vue'
import ComposerBar from '../components/stage/ComposerBar.vue'
import RightRail from '../components/stage/RightRail.vue'
import StatsBar from '../components/stage/StatsBar.vue'
import PanelDock from '../components/stage/PanelDock.vue'
import VoicePanel from '../components/panels/VoicePanel.vue'
import CommandPanel from '../components/panels/CommandPanel.vue'
import DanmakuPanel from '../components/panels/DanmakuPanel.vue'
import KouboPanel from '../components/panels/KouboPanel.vue'
import RefsPanel from '../components/panels/RefsPanel.vue'
import SongsPanel from '../components/panels/SongsPanel.vue'
import SettingsDrawer from '../components/settings/SettingsDrawer.vue'
import NtConfirmDialog from '../components/ui/NtConfirmDialog.vue'
import { useLiveStore } from '../stores/live'
import { useEventsStore } from '../stores/events'
import { useTranscriptStore } from '../stores/transcript'
import { useSettingsStore } from '../stores/settings'
import { useUiPrefs } from '../stores/uiPrefs'
import { useAuthStore } from '../stores/auth'
import { useStopControlStore } from '../stores/stopControl'
import { usePlatformStore } from '../stores/platform'
import { useDanmakuRuntimeStore } from '../stores/danmakuRuntime'
import type { RtcCreds } from '../components/rtc/types'
import { toCreds, refreshDelay, degradeOnRefreshError } from '../components/rtc/creds'
import { getRtcToken } from '../services/api/live'
import { classifyApiError } from '../services/api/client'
import type { WireEvent } from '../services/api/types'
import { getLedger, listAvatars, getRecord, setRecord, fetchRecord, type Ledger } from '../services/api/panels'
import { useAvatarsStore } from '../stores/avatars'
import { createVisibilityPolling } from '../utils/polling'
import { registerStageHotkeys } from '../utils/hotkeys'

const router = useRouter()
const live = useLiveStore()
const events = useEventsStore()
const transcript = useTranscriptStore()
const settings = useSettingsStore()
const prefs = useUiPrefs()
const auth = useAuthStore()
const avatars = useAvatarsStore()
const stopCtl = useStopControlStore()
const platform = usePlatformStore()
const danmakuRt = useDanmakuRuntimeStore()

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

const lyricCaption = ref('')
const caption = computed(() => {
  if (lyricCaption.value) return lyricCaption.value
  const last = transcript.lines[transcript.lines.length - 1]
  return last?.text || ''
})

function applyLyricEvent(e: WireEvent): void {
  if (e.type === 'song.playing') {
    const lines = e.payload?.caption_lines
    lyricCaption.value = Array.isArray(lines)
      ? lines.map((line) => String(line).trim()).filter(Boolean).join('\n')
      : ''
    return
  }
  if (e.type === 'song.stopped' || e.type === 'song.ended') lyricCaption.value = ''
}

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

// ---------- F34/N2 录播（蓝本 stage 1662-1674：开关保存 + 状态轮询 + 手动再拉） ----------
const recEnabled = ref(false)
const recBusy = ref(false)
const recHint = ref('')
// 运行态由 live.status.record（5s 状态轮询）提供；开关值经 GET /api/v1/record 拉取。
const recordState = computed(() => live.status?.record?.state || 'none')
const recordStateLabel = computed(
  () =>
    ({
      recording: '● 录制中',
      processing: '出片中…',
      ready: '已出片',
      failed: '出片失败',
      unavailable: '未开通',
      none: ''
    })[recordState.value] ?? recordState.value
)
// 手动再拉：Supervisor 已装配且非录制/出片中时可拉（出片失败补拉或最近一场复拉）。
const canFetchRecord = computed(
  () =>
    !!live.status?.record?.enabled &&
    !recBusy.value &&
    (recordState.value === 'failed' || recordState.value === 'ready' || recordState.value === 'none')
)

async function loadRecord(): Promise<void> {
  try {
    const r = await getRecord()
    recEnabled.value = r.enabled
  } catch {
    /* 录播非关键路径，静默 */
  }
}

async function toggleRecord(): Promise<void> {
  if (recBusy.value) return
  recBusy.value = true
  recHint.value = ''
  try {
    const r = await setRecord(!recEnabled.value)
    recEnabled.value = r.enabled
    recHint.value = r.note || '已保存'
  } catch (err) {
    recHint.value = classifyApiError(err).message
  } finally {
    recBusy.value = false
  }
}

async function doFetchRecord(): Promise<void> {
  if (recBusy.value) return
  recBusy.value = true
  recHint.value = ''
  try {
    const r = await fetchRecord(live.status?.liveId || '')
    recHint.value = '⏺ 开始拉取录播' + (r.live_id ? `（场次 ${r.live_id}）` : '（最近一场）')
  } catch (err) {
    recHint.value = classifyApiError(err).message
  } finally {
    recBusy.value = false
  }
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

// ── F48 统计条：开关走 prefs.stats（页面工具栏按钮）；拖动位置记忆 prefs.statsPos。
// {0,0} = 停靠底部通栏；拖动后转 fixed 浮动。双击把手复位。
const statsBarRef = ref<HTMLElement | null>(null)
const statsDragPos = ref<{ x: number; y: number } | null>(null)
const statsFloating = computed(() => !!(statsDragPos.value || prefs.statsPos.x > 0 || prefs.statsPos.y > 0))
const statsShown = computed(() => statsDragPos.value ?? prefs.statsPos)
let statsDrag: { px: number; py: number; ox: number; oy: number } | null = null

function statsDragDown(e: PointerEvent): void {
  const docked = prefs.statsPos.x === 0 && prefs.statsPos.y === 0
  const rect = docked ? statsBarRef.value?.getBoundingClientRect() : undefined
  const ox = rect ? rect.left : prefs.statsPos.x
  const oy = rect ? rect.top : prefs.statsPos.y
  statsDrag = { px: e.clientX, py: e.clientY, ox, oy }
  statsDragPos.value = { x: ox, y: oy }
  window.addEventListener('pointermove', statsDragMove)
  window.addEventListener('pointerup', statsDragUp)
  e.preventDefault()
}

function statsDragMove(e: PointerEvent): void {
  if (!statsDrag) return
  statsDragPos.value = {
    x: Math.max(0, statsDrag.ox + e.clientX - statsDrag.px),
    y: Math.max(0, statsDrag.oy + e.clientY - statsDrag.py)
  }
}

function statsDragUp(): void {
  if (statsDrag && statsDragPos.value) prefs.setStatsPos(statsDragPos.value.x, statsDragPos.value.y)
  statsDrag = null
  statsDragPos.value = null
  window.removeEventListener('pointermove', statsDragMove)
  window.removeEventListener('pointerup', statsDragUp)
}

function statsReset(): void {
  statsDrag = null
  statsDragPos.value = null
  prefs.resetStatsPos()
}

const deckStyle = computed(() => ({ '--rail-w': `${prefs.rightW}px` }))
const stageMode = computed(() => (['starting', 'live', 'stopping'].includes(live.phase) ? 'live' : 'prepare'))

// 事件按序消费：accept 只入队，这里 drain 后逐条派发，批量回放不丢中间事件。
watch(
  () => events.pending.length,
  (n) => {
    if (!n) return
    for (const e of events.drain()) {
      live.applyEvent(e)
      transcript.applyEvent(e)
      applyLyricEvent(e)
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
  void loadRecord()
  // 弹幕运行态由 Stage 持有（v2 §2.2）：面板只是选择器，切 Tab/卸载不停轮询。
  danmakuRt.start()
  document.addEventListener('visibilitychange', onVisible)
  unregisterHotkeys = registerStageHotkeys({
    // F9 只在就绪态进 Start；服务端 preflight 凭证接入后并入 canStart，不伪造 ready。
    canStart: () => !live.busy && !live.isLive,
    isBlocked: () => live.busy || platform.controlBusy,
    onStart: () => {
      if (live.busy || live.isLive) return
      void start()
    },
    // F10 只请求打开下播确认，不直接停止。
    onStop: () => stopCtl.requestStopLive('f10'),
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
  danmakuRt.stop()
  clearRefreshTimer()
  resyncCtrl?.abort()
  resyncCtrl = null
  events.disconnect()
  unregisterHotkeys?.()
  document.removeEventListener('visibilitychange', onVisible)
  window.removeEventListener('pointermove', statsDragMove)
  window.removeEventListener('pointerup', statsDragUp)
})

function onVisible(): void {
  if (document.visibilityState === 'visible') {
    void live.load()
    void events.connect(live.status?.sessionId || '')
  }
}

async function start(): Promise<void> {
  // G 批 M4：开播链路带 persona_id——读取当前 active 档案（v3 §1.2 唯一真相源）；
  // 无 active 档案时回落旧路径（settings 会话值），保持兼容。
  activeAvatarId.value = ''
  try {
    const r = await listAvatars(1, 100)
    const selected = r.avatars.find((a) => a.is_active)
    activeAvatarId.value = selected?.persona_id ?? ''
    if (selected) {
      const personaId = selected.persona_id
      const revision = selected.revision
      await live.start({ personaId }, async () => {
        const current = await listAvatars(1, 100)
        const active = current.avatars.find((a) => a.is_active)
        return active?.persona_id === personaId && active.revision === revision
      })
      return
    }
  } catch {
    activeAvatarId.value = ''
  }
  const s = settings.groupById('session')
  await live.start({
    avatarId: String(s?.values.avatar_id ?? ''),
    greeting: String(s?.values.greeting ?? '')
  })
}

// 「重新同步并开播」（v3 §2.2）：sync 成功后只进入一次统一 preflight/start 流程。
// resyncCtrl：组件卸载时中断轮询，避免离开直播台后 480s 上限的后台请求（R3-02）。
const activeAvatarId = ref('')
const resyncBusy = ref(false)
let resyncCtrl: AbortController | null = null
async function resyncAndStart(): Promise<void> {
  if (!activeAvatarId.value || resyncBusy.value) return
  resyncBusy.value = true
  resyncCtrl?.abort()
  resyncCtrl = new AbortController()
  try {
    const st = await avatars.syncAndWait(activeAvatarId.value, resyncCtrl.signal)
    if (st.state !== 'done') return
    await start()
  } catch {
    // store 已写入错误态（AbortError 为卸载中断，静默）
  } finally {
    resyncBusy.value = false
  }
}

const personaGuide = computed(() => {
  switch (live.errorCode) {
    case 'persona_not_found':
    case 'avatar_not_synced':
    case 'voice_not_found':
    case 'profile_changed':
      return 'jump'
    case 'preflight_expired':
    case 'preflight_required':
      return 'repreflight'
    case 'draft_required':
      return 'draft'
    case 'publish_target_unverified':
      return 'platform'
    case 'avatar_expired':
      return 'resync'
    default:
      return ''
  }
})

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

async function doClearTranscript(): Promise<void> {
  if (live.busy) return
  const ok = await live.clearTranscripts()
  if (!ok) return
  confirmClear.value = false
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
        <!-- F34/N2 录播（蓝本 stage 1662-1674）：开关 + 运行态徽标 + 手动再拉 -->
        <div class="record-ctl">
          <button
            type="button"
            class="tool"
            :class="{ 'tool--on': recEnabled }"
            :disabled="recBusy"
            :aria-pressed="recEnabled"
            @click="toggleRecord"
          >⏺ 录播{{ recEnabled ? '开' : '关' }}</button>
          <span
            v-if="recordState !== 'none' && recordState !== 'unavailable'"
            class="record-ctl__state"
            :class="`is-${recordState}`"
            aria-live="polite"
          >{{ recordStateLabel }}</span>
          <button v-if="canFetchRecord" type="button" class="tool" :disabled="recBusy" @click="doFetchRecord">拉取录播</button>
        </div>
        <button type="button" class="tool" @click="router.push('/avatars')">数字人档案</button>
        <button type="button" class="tool" data-testid="sessions-link" @click="router.push('/sessions')">场次复盘</button>
        <button type="button" class="tool" @click="router.push('/platform')">平台接入</button>
        <button type="button" class="tool" @click="router.push('/settings')">全部设置</button>
        <button type="button" class="tool" @click="drawerOpen = true">快捷设置</button>
        <!-- F48：统计条开关（s2s.stage.stats），拖动记忆走 statsPos -->
        <button
          type="button"
          class="tool"
          :class="{ 'tool--on': prefs.stats }"
          :aria-pressed="prefs.stats"
          @click="prefs.setStats(!prefs.stats)"
        >统计{{ prefs.stats ? '开' : '关' }}</button>
      </div>
    </header>

    <p v-if="recHint" class="page__rec-hint" aria-live="polite">{{ recHint }}</p>

    <div v-if="auth.error" class="auth-banner" role="alert">
      凭证失效，请重新填写 Bearer。
      <button type="button" @click="tokenInput = ''; auth.logout()">知道了</button>
    </div>

    <div v-if="personaGuide" class="guide-banner" role="alert">
      <span>{{ live.error }}</span>
      <button v-if="personaGuide === 'jump'" type="button" @click="router.push('/avatars')">前往档案页</button>
      <button v-else-if="personaGuide === 'repreflight'" type="button" :disabled="live.busy" @click="start">
        重新预检并开播
      </button>
      <span v-else-if="personaGuide === 'draft'">当前后端未提供草稿列表接口；请先在内容包中选择并提交草稿，再返回直播台。</span>
      <button v-else-if="personaGuide === 'platform'" type="button" :disabled="live.busy" @click="router.push('/platform')">
        前往平台接入验证
      </button>
      <template v-else>
        <button type="button" :disabled="resyncBusy || live.busy" @click="resyncAndStart">
          {{ resyncBusy ? '同步中…' : '重新同步并开播' }}
        </button>
        <button type="button" @click="router.push(`/avatars/${encodeURIComponent(activeAvatarId)}/edit`)">查看档案</button>
      </template>
    </div>

    <div class="stage-mode" :data-mode="stageMode" aria-live="polite">
      <strong>{{ stageMode === 'live' ? '直播中' : '准备' }}</strong>
      <span>{{ stageMode === 'live' ? '本场已冻结；形象、人设与音色修改仅作用于下一场' : '配置与开播预检仅作用于下一场' }}</span>
    </div>

    <main class="deck" :style="deckStyle">
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
          @stop="stopCtl.requestStopLive('video-stage')"
          @open-settings="drawerOpen = true"
          @rtc-state="rtcState = $event"
        />

      </div>

      <section class="workspace deck__rail" aria-label="直播单工作区">
        <PanelDock
          :active="prefs.activeWorkspaceTab"
          :pending-count="danmakuRt.pendingCount"
          :right-open="rightOpen"
          @select="prefs.setActiveWorkspaceTab"
          @toggle-right="rightOpen = !rightOpen"
        />
        <div
          :id="`workspace-panel-${prefs.activeWorkspaceTab}`"
          class="workspace__panel"
          role="tabpanel"
          :aria-labelledby="`workspace-tab-${prefs.activeWorkspaceTab}`"
        >
          <VoicePanel
            v-if="prefs.activeWorkspaceTab === 'voice'"
            :voice-id="String(settings.groupById('voice')?.values.voice_id ?? '')"
            :live-mode="stageMode === 'live'"
          />
          <CommandPanel
            v-else-if="prefs.activeWorkspaceTab === 'command'"
            :busy="live.busy"
            @stop-actions="live.stopCurrentActions()"
            @clear-actions="live.clearAllActions()"
          />
          <DanmakuPanel v-else-if="prefs.activeWorkspaceTab === 'danmaku'" />
          <KouboPanel v-else-if="prefs.activeWorkspaceTab === 'koubo'" />
          <RefsPanel v-else-if="prefs.activeWorkspaceTab === 'refs'" />
          <SongsPanel v-else />
        </div>
        <RightRail
          v-if="rightOpen"
          class="workspace__transcript"
          :lines="transcript.lines"
          :events="events.events"
          :fresh-ms="events.fresh"
          :protocol="events.mode"
          :show-events="captionCfg.showEvents"
          @clear-transcript="confirmClearTranscript"
        />
      </section>
    </main>

    <ComposerBar
      ref="composerRef"
      class="page__composer"
      :can-speak="live.isLive"
      :busy="live.busy"
      :error="live.error"
      @say="(t, m) => live.say(t, m)"
      @command="(t) => live.command(t)"
      @stop="stopCtl.requestStopLive('composer')"
      @interrupt="live.interrupt()"
    />

    <!-- F48：统计条可关（prefs.stats）、可拖动记忆（prefs.statsPos；双击把手复位） -->
    <div
      v-if="prefs.stats"
      ref="statsBarRef"
      class="page__stats"
      :class="{ 'page__stats--float': statsFloating }"
      :style="statsFloating ? { left: `${statsShown.x}px`, top: `${statsShown.y}px` } : undefined"
    >
      <button
        type="button"
        class="page__stats-grip"
        aria-label="拖动统计条，双击复位"
        title="拖动统计条（双击复位）"
        @pointerdown="statsDragDown"
        @dblclick="statsReset"
      >⠿</button>
      <StatsBar class="page__stats-bar" :items="stats" />
    </div>

    <SettingsDrawer :open="drawerOpen" @close="drawerOpen = false" />
    <!-- 唯一下播控制器确认框（v2 §3.2）：下播不自动停推。 -->
    <NtConfirmDialog
      :open="stopCtl.liveConfirmOpen"
      dangerous
      :busy="stopCtl.liveStopping"
      :error="stopCtl.liveError"
      title="下播确认"
      message="将结束当前直播场次。下播不会自动停止推流，如需停推请到平台接入页单独操作。确定下播？"
      confirm-text="确认下播"
      @confirm="void stopCtl.confirmStopLive()"
      @cancel="stopCtl.cancelStopLive()"
    />
    <NtConfirmDialog
      :open="confirmClear"
      title="清空字幕"
      message="将清空当前场次全部字幕记录，且不可恢复。确定继续？"
      confirm-text="清空"
      dangerous
      :busy="live.busy"
      :error="live.error"
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
  height: 100%;
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
  min-height: 44px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-2);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
/* F34/N2 录播控制（蓝本 stage 1662-1674） */
.record-ctl {
  display: flex;
  align-items: center;
  gap: 6px;
}
.record-ctl .tool--on {
  border-color: var(--nt-accent-border);
  color: var(--nt-accent);
}
.record-ctl__state {
  padding: 1px 8px;
  border-radius: var(--nt-radius-pill);
  background: var(--nt-data-inset);
  color: var(--nt-text-2);
  font-size: var(--nt-fs-micro);
  white-space: nowrap;
}
.record-ctl__state.is-recording {
  color: var(--nt-danger);
}
.record-ctl__state.is-ready {
  color: var(--nt-success);
}
.record-ctl__state.is-failed {
  color: var(--nt-danger);
}
.record-ctl__state.is-processing {
  color: var(--nt-accent);
}
.page__rec-hint {
  margin: 4px 0 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.deck {
  display: grid;
  grid-template-columns: minmax(0, 1fr) var(--rail-w, 360px);
  gap: 10px;
  min-height: 0;
  flex: 1;
  overflow: auto;
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
.stage-mode {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 8px 10px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-raised);
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
}
.stage-mode strong {
  color: var(--nt-accent);
}
.workspace {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.workspace__panel {
  flex: 1;
  min-height: 220px;
  max-height: 460px;
  overflow: auto;
  padding: var(--nt-pad-panel);
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
}
.workspace__transcript {
  flex: 1;
  min-height: 180px;
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
    grid-template-rows: minmax(280px, 1fr) auto;
  }
  .deck__stage {
    min-height: 280px;
  }
  .workspace__panel {
    max-height: 320px;
  }
}

@media (max-width: 767px) {
  .page {
    padding: 8px;
  }
  .page__head {
    align-items: flex-start;
  }
  .page__tools {
    justify-content: flex-start;
    width: 100%;
  }
  .token {
    width: 100%;
  }
  .token input {
    flex: 1;
    width: auto;
    min-height: 44px;
    font-size: 16px;
  }
  .record-ctl {
    flex-wrap: wrap;
  }
  .page__stats {
    overflow-x: auto;
  }
}

@media (max-width: 359px) {
  .page {
    gap: 6px;
  }
  .page__brand strong {
    font-size: var(--nt-fs-panel);
  }
  .stage-mode {
    align-items: flex-start;
    flex-direction: column;
    gap: 4px;
    padding: 6px 8px;
  }
  .deck:not(.deck--no-rail) {
    grid-template-rows: minmax(156px, 1fr) auto;
  }
  .deck__stage {
    min-height: 156px;
  }
  .page__stats-bar :deep(.stats) {
    overflow-x: auto;
  }
  .page__stats-bar :deep(.stats__item) {
    flex: 0 0 72px;
  }
  .page__stats-bar :deep(.stats__hint) {
    display: none;
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
.page__composer {
  position: relative;
  z-index: 5;
}
/* F48：统计条拖动把手 + 浮动位 */
.page__stats {
  position: relative;
  display: flex;
  align-items: stretch;
  gap: 6px;
}
.page__stats--float {
  position: fixed;
  z-index: 30;
  width: min(720px, calc(100vw - 16px));
}
.page__stats-grip {
  flex: none;
  align-self: stretch;
  width: 44px;
  min-height: 44px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-raised);
  color: var(--nt-text-3);
  font: inherit;
  cursor: grab;
  touch-action: none;
}
.page__stats-grip:active {
  cursor: grabbing;
}
.page__stats-grip:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}
.page__stats-bar {
  flex: 1;
  min-width: 0;
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
.guide-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 10px;
  border: 1px solid var(--nt-danger-border);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-raised);
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
.guide-banner button {
  min-height: 44px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.guide-banner button:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.guide-banner button:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
}
</style>

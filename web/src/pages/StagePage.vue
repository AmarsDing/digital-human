<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import VideoStage from '../components/stage/VideoStage.vue'
import PreflightDialog from '../components/stage/PreflightDialog.vue'
import { usePreparationStore } from '../stores/preparation'
import ComposerBar from '../components/stage/ComposerBar.vue'
import RightRail from '../components/stage/RightRail.vue'
import MetricsPopover from '../components/stage/MetricsPopover.vue'
import GroupPanelDock from '../components/stage/GroupPanelDock.vue'
import TaskControlDock from '../components/stage/TaskControlDock.vue'
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
import { useUiPrefs, type WorkspacePanelId } from '../stores/uiPrefs'
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
const preparation = usePreparationStore()
const preflightOpen = ref(false)
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
const activeAvatarName = ref('')
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

const deckStyle = computed(() => ({ '--rail-w': `${prefs.rightW}px` }))
const stageMode = computed(() => (['starting', 'live', 'stopping'].includes(live.phase) ? 'live' : 'prepare'))
type ControlGroup = 'live' | 'prepare' | 'settings' | ''
const LIVE_WORKSPACE_ITEMS: WorkspacePanelId[] = ['command', 'danmaku', 'songs']
const PREPARE_WORKSPACE_ITEMS: WorkspacePanelId[] = ['voice', 'koubo', 'refs']
const controlGroups = [
  { id: 'live', label: '播出中', summary: '互动、动作与歌曲' },
  { id: 'prepare', label: '场次准备', summary: '形象、音色与口播' },
  { id: 'settings', label: '设置', summary: '平台、录播与偏好' }
]
const controlGroup = ref<ControlGroup>(stageMode.value)
const quickActions = computed(() => [
  { id: 'captions', label: '字幕' },
  { id: 'command', label: '动作' },
  { id: 'songs', label: '歌曲' },
  stageMode.value === 'live' ? { id: 'danmaku', label: '弹幕' } : { id: 'preflight', label: '预检' }
])

function selectGroup(group: Exclude<ControlGroup, '' | 'settings'>, panel?: WorkspacePanelId): void {
  controlGroup.value = group
  const items = group === 'live' ? LIVE_WORKSPACE_ITEMS : PREPARE_WORKSPACE_ITEMS
  const next = panel ?? (items.includes(prefs.activeWorkspaceTab) ? prefs.activeWorkspaceTab : items[0])
  prefs.setActiveWorkspaceTab(next)
}

function handleQuickAction(id: string): void {
  if (id === 'captions') {
    selectGroup('live')
    rightOpen.value = true
    return
  }
  if (id === 'command' || id === 'danmaku' || id === 'songs') {
    selectGroup('live', id)
    return
  }
  if (id === 'preflight' && !live.busy && !live.isLive) void checkBeforeStart()
}

selectGroup(stageMode.value)
watch(stageMode, (mode) => selectGroup(mode))
watch(controlGroup, (group) => {
  if (group === 'live' || group === 'prepare') selectGroup(group)
})

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
  void loadActiveAvatarName()
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
})

function onVisible(): void {
  if (document.visibilityState === 'visible') {
    void live.load()
    void events.connect(live.status?.sessionId || '')
  }
}

async function loadActiveAvatarName(): Promise<void> {
  try {
    const r = await listAvatars(1, 100)
    activeAvatarName.value = r.avatars.find((a) => a.is_active)?.name || ''
  } catch {
    activeAvatarName.value = ''
  }
}

async function start(): Promise<void> {
  await preparation.load()
  if (preparation.selected) {
    const draft = preparation.selected
    activeAvatarId.value = draft.snapshot.avatar?.id || ''
    await live.start({ personaId: activeAvatarId.value, draftId: draft.draft_id, expectedRevision: draft.revision },
      () => preparation.selected?.draft_id === draft.draft_id && preparation.selected.revision === draft.revision)
    return
  }
  // G 批 M4：开播链路带 persona_id——读取当前 active 档案（v3 §1.2 唯一真相源）；
  // 无 active 档案时回落旧路径（settings 会话值），保持兼容。
  activeAvatarId.value = ''
  try {
    const r = await listAvatars(1, 100)
    const selected = r.avatars.find((a) => a.is_active)
    activeAvatarId.value = selected?.persona_id ?? ''
    activeAvatarName.value = selected?.name || ''
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

async function checkBeforeStart(): Promise<void> {
  if (live.busy || live.isLive) return
  preflightOpen.value = true
  await preparation.load()
  const draft = preparation.selected
  const personaId = draft?.snapshot.avatar?.id || activeAvatarId.value
  await live.runPreflight({ personaId, draftId: draft?.draft_id, expectedRevision: draft?.revision })
}

watch(() => preparation.selected, () => live.invalidatePreflight())

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
    <p v-if="recHint" class="page__rec-hint" aria-live="polite">{{ recHint }}</p>

    <div v-if="auth.error" class="auth-banner" role="alert">
      凭证失效，请重新填写 Bearer。
      <button type="button" @click="tokenInput = ''; auth.logout()">知道了</button>
    </div>

    <div v-if="personaGuide" class="guide-banner" role="alert">
      <span>{{ live.error }}</span>
      <button v-if="personaGuide === 'jump'" type="button" @click="router.push('/avatars')">前往档案页</button>
      <button v-else-if="personaGuide === 'repreflight'" type="button" :disabled="live.busy" @click="checkBeforeStart">
        重新预检
      </button>
      <button v-else-if="personaGuide === 'draft'" type="button" @click="router.push('/prepare')">选择并提交场次草稿</button>
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
      <span v-if="activeAvatarName" class="stage-mode__avatar">当前形象：{{ activeAvatarName }}</span>
      <div class="stage-mode__actions">
        <button type="button" class="tool" @click="router.push('/prepare')">场次准备</button>
        <MetricsPopover :items="stats" />
      </div>
    </div>

    <PreflightDialog :open="preflightOpen" :result="live.preflight" :busy="live.busy" :error="live.error"
      @close="preflightOpen = false" @retry="checkBeforeStart" @prepare="router.push('/prepare')"
      @start="preflightOpen = false; start()" />
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
        <TaskControlDock
          v-model:expanded-id="controlGroup"
          :groups="controlGroups"
          :quick-actions="quickActions"
          @quick-action="handleQuickAction"
        >
          <template #live>
            <GroupPanelDock
              v-if="controlGroup === 'live'"
              :active="prefs.activeWorkspaceTab"
              :items="LIVE_WORKSPACE_ITEMS"
              :pending-count="danmakuRt.pendingCount"
              @select="prefs.setActiveWorkspaceTab"
            />
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
          </template>
          <template #prepare>
            <GroupPanelDock
              v-if="controlGroup === 'prepare'"
              :active="prefs.activeWorkspaceTab"
              :items="PREPARE_WORKSPACE_ITEMS"
              :pending-count="danmakuRt.pendingCount"
              @select="prefs.setActiveWorkspaceTab"
            />
          </template>
          <template #settings>
            <div class="page__tools" aria-label="直播工具">
              <div class="token">
                <input v-model="tokenInput" type="password" placeholder="lan Bearer（本机模式留空）" @keyup.enter="applyToken" />
                <button type="button" @click="applyToken">应用</button>
              </div>
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
            </div>
          </template>
        </TaskControlDock>
        <div
          v-show="controlGroup === 'live' || controlGroup === 'prepare'"
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
      </section>
    </main>

    <ComposerBar
      ref="composerRef"
      class="page__composer"
      :can-speak="live.isLive"
      :session-id="live.status?.sessionId || ''"
      :epoch="live.status?.feed?.epoch ?? 0"
      :busy="live.busy"
      :error="live.error"
      @say="(t, m) => live.say(t, m)"
      @command="(t) => live.command(t)"
      @stop="stopCtl.requestStopLive('composer')"
      @interrupt="live.interrupt()"
    />



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
.page__tools {
  display: flex;
  align-items: center;
  justify-content: flex-start;
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
.stage-mode__avatar {
  color: var(--nt-text-1);
  font-weight: 600;
}
.stage-mode__actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
}
.tool:focus-visible,
.token button:focus-visible,
.token input:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
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
  .page {
    height: auto;
    min-height: 100%;
    overflow: visible;
  }
  .deck {
    display: contents;
  }
  .deck__stage {
    order: 1;
    min-height: 280px;
  }
  .page__composer {
    order: 2;
  }
  .deck__rail {
    order: 3;
  }
  .workspace__panel {
    flex: none;
    min-height: 0;
    max-height: 320px;
  }
  .workspace__transcript {
    flex: none;
  }
}

@media (max-width: 767px) {
  .page {
    padding: 8px;
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
}

@media (max-width: 359px) {
  .page {
    gap: 6px;
  }
  .stage-mode {
    align-items: flex-start;
    flex-direction: column;
    gap: 4px;
    padding: 6px 8px;
  }
  .stage-mode__actions {
    width: 100%;
    margin-left: 0;
    justify-content: flex-end;
  }
  .deck:not(.deck--no-rail) {
    grid-template-rows: auto auto;
  }
  .deck__stage {
    min-height: 156px;
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
.page__composer {
  flex: none;
  position: relative;
  z-index: 5;
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

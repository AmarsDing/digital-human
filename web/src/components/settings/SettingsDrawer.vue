<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import NtDrawer from '../ui/NtDrawer.vue'
import NtConfirmDialog from '../ui/NtConfirmDialog.vue'
import SettingsGroupForm from './SettingsGroupForm.vue'
import ViduKeysPanel from './ViduKeysPanel.vue'
import PatronsPanel from './PatronsPanel.vue'
import { GROUP_SPECS } from './schema'
import { useSettingsStore } from '../../stores/settings'
import { useLiveStore } from '../../stores/live'
import { listAvatars, getAvatar, getCredits, type AvatarDetail, type AvatarListItem } from '../../services/api/panels'

const props = defineProps<{ open: boolean; inline?: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const store = useSettingsStore()
const live = useLiveStore()
const active = ref('session')
const spec = computed(() => GROUP_SPECS.find((g) => g.id === active.value) || GROUP_SPECS[0])
const values = computed(() => store.groupById(spec.value.id)?.values || {})
const currentDirty = computed(() => store.dirtyGroupIds.includes(spec.value.id))
const savedFlash = ref('')
const formValid = ref(true)
const confirmClose = ref(false)

// F08/F36/G03/N3 额度：credits_balance readonly 字段展示真实 Vidu 余额。
// 声明必须先于下方 immediate watch（TDZ）。
const creditsText = ref('')
let creditsFetched = false
async function loadCredits(): Promise<void> {
  if (creditsFetched) return
  creditsFetched = true
  try {
    const r = await getCredits()
    if (!r.configured) {
      creditsText.value = '未配置 Vidu Key'
      return
    }
    const v = r.remain ?? r.remain_credit
    creditsText.value = typeof v === 'number' ? String(v) : '—'
  } catch {
    creditsText.value = '查询失败'
  }
}

watch(
  () => props.open,
  (open) => {
    if (!open) return
    savedFlash.value = ''
    // 仅首次打开拉取；已有数据时不再重复 load，避免打开抽屉把未保存修改冲掉。
    if (!store.groups.length) void store.load()
    // 只读派生字段（7 键）依赖 active 档案，打开抽屉即拉取一次。
    void loadActiveAvatar()
    // F08/F36/G03/N3：积分余额接 /api/v1/credits 实时值（无凭据显示明确空态）。
    void loadCredits()
  },
  { immediate: true }
)

function update(key: string, value: unknown): void {
  store.setField(spec.value.id, key, value)
}

async function save(): Promise<void> {
  if (!formValid.value) return
  const ok = await store.save()
  savedFlash.value = ok ? '已保存' : ''
}

// 未保存离开提示：关闭（含 Esc）前检测脏分组。
function requestClose(): void {
  if (store.dirtyCount) {
    confirmClose.value = true
  } else {
    emit('close')
  }
}

function discardAndClose(): void {
  confirmClose.value = false
  void store.load() // 重新拉取覆盖未保存修改
  emit('close')
}

// 有脏数据时拦截页面关闭/刷新。
function onBeforeUnload(e: BeforeUnloadEvent): void {
  if (store.dirtyCount) e.preventDefault()
}
onMounted(() => {
  if (typeof window !== 'undefined') window.addEventListener('beforeunload', onBeforeUnload)
})
onBeforeUnmount(() => {
  if (typeof window !== 'undefined') window.removeEventListener('beforeunload', onBeforeUnload)
})

// llm 组：清空记忆（后端接口已封装，这里接上入口）。
const llmMsg = ref('')
async function resetLlmMemory(): Promise<void> {
  const ok = await live.resetLlmMemory()
  llmMsg.value = ok ? `已清空 ${live.llmClearedTurns} 轮记忆` : live.error || '清空失败'
}

// persona 组（v3 §1.2）：人设已迁移至数字人档案，此处只读派生当前 active 档案 + 跳档案页。
const activeAvatar = ref<AvatarListItem | null>(null)
const activeAvatarDetail = ref<AvatarDetail | null>(null)
const activeAvatarLoaded = ref(false)

async function loadActiveAvatar(): Promise<void> {
  try {
    const r = await listAvatars(1, 100)
    activeAvatar.value = r.avatars.find((a) => a.is_active) ?? null
    // detail 提供 system_prompt / voice_id / vidu_avatar_id 等派生所需字段。
    activeAvatarDetail.value = activeAvatar.value ? await getAvatar(activeAvatar.value.persona_id) : null
  } catch {
    activeAvatar.value = null
    activeAvatarDetail.value = null
  } finally {
    activeAvatarLoaded.value = true
  }
}

// 只读派生值（v3 §1.2）：7 个 readonly 键展示 active 档案派生值而非 settings 库存储旧值。
// 仅注入展示层（displayValues），不写入 store，保存通路不受影响。
const NO_PROFILE = '未配置档案'
// F42/G18：Vidu Key 激活后由 ViduKeysPanel 回填生效尾号（后端已同步 settings，这里只刷展示）。
const viduActiveTail8 = ref('')
function onViduKeyChanged(tail8: string): void {
  viduActiveTail8.value = tail8
}
const derivedByGroup = computed((): Record<string, Record<string, string>> => {
  const extra: Record<string, Record<string, string>> = {}
  if (viduActiveTail8.value) {
    extra.vidu_key = { active_tail8: '…' + viduActiveTail8.value }
  }
  // F08/F36/G03/N3：积分余额展示 /api/v1/credits 实时值；未拉到前保留 schema 占位 '—'。
  if (creditsText.value) {
    extra.misc = { credits_balance: creditsText.value }
  }
  // 未拉取完成前不覆盖展示值，避免旧值→「未配置档案」→派生值的闪烁。
  if (!activeAvatarLoaded.value) return extra
  const d = activeAvatarDetail.value
  if (!d) {
    const none = { avatar_id: NO_PROFILE }
    return {
      ...extra,
      session: { avatar_id: NO_PROFILE, image_uri: NO_PROFILE },
      voice: { voice_id: NO_PROFILE },
      persona: { active_id: NO_PROFILE, content: NO_PROFILE },
      llm: { system: NO_PROFILE },
      assets: none
    }
  }
  const chars = [...(d.system_prompt || '')]
  const prompt = chars.length ? chars.slice(0, 200).join('') + (chars.length > 200 ? '…' : '') : '（档案未填写人设提示词）'
  const viduAvatar = d.vidu_avatar_id || '（未同步 Vidu）'
  return {
    ...extra,
    session: { avatar_id: viduAvatar, image_uri: d.vidu_image_uri || '（未同步 Vidu）' },
    voice: { voice_id: d.voice_id || '（档案未配置音色）' },
    persona: { active_id: `${d.name}（${d.slug}）`, content: prompt },
    llm: { system: prompt },
    assets: { avatar_id: viduAvatar }
  }
})

// 传给表单的展示值：存储值之上叠加派生值（readonly 键无 emit，不会回流 store）。
const displayValues = computed(() => ({ ...values.value, ...(derivedByGroup.value[spec.value.id] ?? {}) }))

function effectLabel(effective: string): string {
  return effective === 'immediate' ? '即时' : '下次连线'
}
</script>

<template>
  <NtDrawer
    :open="open"
    title="设置"
    :width="640"
    :mode="inline ? 'inline' : 'modal'"
    @close="requestClose"
  >
    <div class="drawer">
      <nav class="drawer__nav" aria-label="设置分组">
        <button
          v-for="g in GROUP_SPECS"
          :key="g.id"
          type="button"
          class="drawer__tab"
          :class="{ 'drawer__tab--active': g.id === active }"
          @click="active = g.id"
        >
          <span>{{ g.title }}</span>
          <i v-if="store.dirtyGroupIds.includes(g.id)" class="drawer__dot" aria-label="有未保存修改" />
        </button>
      </nav>

      <main class="drawer__main">
        <header class="drawer__group-head">
          <h3>{{ spec.title }}</h3>
          <span class="drawer__effect" :class="{ 'drawer__effect--immediate': spec.effective === 'immediate' }">
            {{ effectLabel(spec.effective) }}
          </span>
          <span v-if="currentDirty" class="drawer__dirty">未保存</span>
        </header>

        <div v-if="store.loading" class="drawer__loading">正在读取设置…</div>
        <SettingsGroupForm v-else :spec="spec" :values="displayValues" @update="update" @validity="formValid = $event">
          <template #custom="{ field }">
            <PatronsPanel v-if="field.key === 'patrons' && active === 'member'" />
          </template>
        </SettingsGroupForm>

        <div v-if="active === 'llm'" class="drawer__group-action">
          <button type="button" class="foot__btn" :disabled="live.busy" @click="resetLlmMemory">清空记忆</button>
          <span v-if="llmMsg" class="drawer__action-msg">{{ llmMsg }}</span>
        </div>
        <div v-if="active === 'persona'" class="drawer__group-action">
          <span v-if="activeAvatar" class="drawer__action-msg">
            当前档案：{{ activeAvatar.name }}（{{ activeAvatar.slug }}）
          </span>
          <span v-else class="drawer__action-msg">尚未选择数字人档案</span>
          <router-link class="drawer__link" to="/avatars" @click="emit('close')">前往数字人档案页</router-link>
        </div>
        <!-- F42/G18 多 key 管理：激活后同步 vidu_key.active_tail8 展示值（不写 dirty） -->
        <div v-if="active === 'vidu_key'" class="drawer__group-action drawer__group-action--block">
          <ViduKeysPanel @changed="onViduKeyChanged" />
        </div>

        <p v-if="store.missingGroups.length" class="drawer__missing">
          后端未返回分组：{{ store.missingGroups.join('、') }}（已按本地默认值展示，保存后写入）
        </p>
        <p v-if="store.error" class="drawer__error">{{ store.error }}</p>
      </main>
    </div>

    <template #footer>
      <div class="foot">
        <span class="foot__hint">
          <template v-if="savedFlash">{{ savedFlash }}</template>
          <template v-else-if="store.dirtyCount">{{ store.dirtyCount }} 组待保存</template>
          <template v-else>所有修改已保存</template>
        </span>
        <span v-if="store.reloadRequired.length" class="foot__reload">
          需重载：{{ store.reloadRequired.join('、') }}
        </span>
        <button class="foot__btn" type="button" :disabled="store.saving" @click="store.reload()">重载</button>
        <button
          class="foot__btn foot__btn--primary"
          type="button"
          :disabled="store.saving || !store.dirtyCount || !formValid"
          @click="save"
        >
          {{ store.saving ? '保存中…' : '保存' }}
        </button>
      </div>
    </template>
  </NtDrawer>
  <NtConfirmDialog
    :open="confirmClose"
    title="放弃未保存修改"
    :message="`有 ${store.dirtyCount} 组修改未保存，关闭后将丢弃。确定关闭？`"
    confirm-text="放弃并关闭"
    @confirm="discardAndClose"
    @cancel="confirmClose = false"
  />
</template>

<style scoped>
.drawer {
  display: grid;
  grid-template-columns: 160px minmax(0, 1fr);
  gap: 16px;
  min-width: 0;
  min-height: 420px;
}
.drawer__nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
  border-right: 1px solid var(--nt-border-1);
  padding-right: 8px;
}
.drawer__tab {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  height: 32px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid transparent;
  background: transparent;
  color: var(--nt-text-2);
  font: inherit;
  font-size: var(--nt-fs-caption);
  text-align: left;
  cursor: pointer;
}
.drawer__tab:hover {
  background: var(--nt-accent-soft);
  color: var(--nt-text-1);
}
.drawer__tab--active {
  border-color: var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
}
.drawer__dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--nt-warn);
}
.drawer__main {
  min-width: 0;
}
.drawer__group-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.drawer__group-head h3 {
  margin: 0;
  font-size: var(--nt-fs-panel);
  color: var(--nt-text-1);
}
.drawer__effect {
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid var(--nt-border-1);
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.drawer__effect--immediate {
  border-color: var(--nt-accent-border);
  color: var(--nt-accent);
}
.drawer__dirty {
  color: var(--nt-warn);
  font-size: var(--nt-fs-micro);
}
.drawer__loading {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
  padding: 24px 0;
}
.drawer__missing {
  margin-top: 16px;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
  line-height: 1.6;
}
.drawer__error {
  margin-top: 12px;
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
.drawer__group-action {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px dashed var(--nt-border-1);
}
.drawer__group-action--block {
  display: block;
}
.drawer__action-msg {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.drawer__link {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
  color: var(--nt-accent);
  font-size: var(--nt-fs-micro);
  text-decoration: none;
}
.drawer__link:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
  border-radius: var(--nt-radius-s);
}
.foot {
  display: flex;
  align-items: center;
  gap: 10px;
}
.foot__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.foot__reload {
  color: var(--nt-warn);
  font-size: var(--nt-fs-micro);
}
.foot__btn {
  height: 34px;
  padding: 0 14px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.foot__btn--primary {
  margin-left: auto;
  border-color: var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
}
.foot__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.drawer__tab:focus-visible,
.foot__btn:focus-visible {
  outline: 2px solid var(--nt-accent);
  outline-offset: 4px;
}
@media (max-width: 559px) {
  .drawer {
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
    min-height: 0;
  }
  .drawer__nav {
    flex-direction: row;
    min-width: 0;
    overflow-x: auto;
    border-right: 0;
    border-bottom: 1px solid var(--nt-border-1);
    padding: 0 0 8px;
  }
  .drawer__tab {
    flex: 0 0 auto;
    height: 44px;
    white-space: nowrap;
  }
  .drawer__group-head,
  .drawer__group-action,
  .foot {
    flex-wrap: wrap;
  }
  .foot__hint {
    flex: 1 1 160px;
  }
  .foot__btn {
    min-height: 44px;
  }
  .foot__btn--primary {
    margin-left: 0;
  }
}
</style>

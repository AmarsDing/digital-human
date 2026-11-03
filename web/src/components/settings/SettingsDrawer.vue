<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import NtDrawer from '../ui/NtDrawer.vue'
import NtConfirmDialog from '../ui/NtConfirmDialog.vue'
import SettingsGroupForm from './SettingsGroupForm.vue'
import { GROUP_SPECS } from './schema'
import { useSettingsStore } from '../../stores/settings'
import { useLiveStore } from '../../stores/live'

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

watch(
  () => props.open,
  (open) => {
    if (!open) return
    savedFlash.value = ''
    // 仅首次打开拉取；已有数据时不再重复 load，避免打开抽屉把未保存修改冲掉。
    if (!store.groups.length) void store.load()
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

// persona 组：应用人设 = 保存设置 + 提示下次开播生效。
async function applyPersona(): Promise<void> {
  const ok = await store.save()
  savedFlash.value = ok ? '已保存，下次开播生效' : ''
}

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
        <SettingsGroupForm v-else :spec="spec" :values="values" @update="update" @validity="formValid = $event" />

        <div v-if="active === 'llm'" class="drawer__group-action">
          <button type="button" class="foot__btn" :disabled="live.busy" @click="resetLlmMemory">清空记忆</button>
          <span v-if="llmMsg" class="drawer__action-msg">{{ llmMsg }}</span>
        </div>
        <div v-if="active === 'persona'" class="drawer__group-action">
          <button type="button" class="foot__btn" :disabled="store.saving" @click="applyPersona">应用人设</button>
          <span class="drawer__action-msg">保存后下次开播生效</span>
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
.drawer__action-msg {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
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
</style>

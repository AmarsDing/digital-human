<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import AvatarImagePicker from '../components/avatars/AvatarImagePicker.vue'
import AvatarPromptFields, { type AvatarFormValue } from '../components/avatars/AvatarPromptFields.vue'
import AvatarSyncBar from '../components/avatars/AvatarSyncBar.vue'
import ImagePreview from '../components/avatars/ImagePreview.vue'
import RefImageList from '../components/avatars/RefImageList.vue'
import VoicePicker from '../components/avatars/VoicePicker.vue'
import NtButton from '../components/ui/NtButton.vue'
import NtConfirmDialog from '../components/ui/NtConfirmDialog.vue'
import NtSelect from '../components/ui/NtSelect.vue'
import { useAvatarsStore } from '../stores/avatars'
import { useLiveStore } from '../stores/live'
import { listActionLibraries, type AvatarDetail } from '../services/api/panels'
import { ApiError, classifyApiError } from '../services/api/client'
import { canActivateAvatar } from '../utils/avatar-validate'

// 档案编辑页（M4）：create/edit 双模式；revision 乐观锁 409；开播中编辑提示；
// dirty 拦截（onBeforeRouteLeave + beforeunload）；大图预览同屏 1 个、关闭即 revoke。
const route = useRoute()
const router = useRouter()
const store = useAvatarsStore()
const live = useLiveStore()

const personaId = computed(() => String(route.params.id ?? ''))
const isNew = computed(() => !personaId.value)

const detail = ref<AvatarDetail | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const savedFlash = ref('')
const formValid = ref(true)

const form = ref<AvatarFormValue>({ slug: '', name: '', system_prompt: '', greeting: '' })
const voiceId = ref('')
const actionLibraryId = ref('')
const imageAssetId = ref('')
const revision = ref(0)
const baseline = ref('')

const libOptions = ref<{ label: string; value: string }[]>([])
const confirmLeave = ref(false)
const preview = ref<{ open: boolean; url: string }>({ open: false, url: '' })

function snapshot(): string {
  return JSON.stringify({ ...form.value, voiceId: voiceId.value, actionLibraryId: actionLibraryId.value, imageAssetId: imageAssetId.value })
}
const dirty = computed(() => baseline.value !== '' && snapshot() !== baseline.value)

async function load(): Promise<void> {
  if (isNew.value) {
    baseline.value = snapshot()
    return
  }
  loading.value = true
  error.value = ''
  const d = await store.detail(personaId.value)
  loading.value = false
  if (!d) {
    error.value = store.error || '档案加载失败'
    return
  }
  detail.value = d
  form.value = {
    slug: d.slug ?? '',
    name: d.name ?? '',
    system_prompt: d.system_prompt ?? '',
    greeting: d.greeting ?? ''
  }
  voiceId.value = d.voice_id ?? ''
  actionLibraryId.value = d.action_library_id ?? ''
  imageAssetId.value = d.avatar_asset_id ?? ''
  revision.value = d.revision ?? 0
  baseline.value = snapshot()
}

async function save(): Promise<void> {
  if (!formValid.value || saving.value) return
  saving.value = true
  error.value = ''
  savedFlash.value = ''
  const input = {
    name: form.value.name.trim(),
    slug: form.value.slug.trim(),
    system_prompt: form.value.system_prompt,
    greeting: form.value.greeting,
    voice_id: voiceId.value,
    action_library_id: actionLibraryId.value,
    image_asset_id: imageAssetId.value || undefined
  }
  try {
    if (isNew.value) {
      const d = await store.create(input)
      baseline.value = snapshot()
      router.replace(`/avatars/${encodeURIComponent(d.persona_id)}/edit`)
    } else {
      const d = await store.update(personaId.value, input, revision.value)
      detail.value = d
      revision.value = d.revision ?? revision.value
      baseline.value = snapshot()
      savedFlash.value = live.isLive ? '已保存，将在下一场开播生效' : '已保存'
    }
  } catch (err) {
    if (err instanceof ApiError && err.code === 'revision_conflict') {
      error.value = classifyApiError(err).message + '，可点击「重新加载」获取最新版本'
    } else {
      error.value = classifyApiError(err).message
    }
  } finally {
    saving.value = false
  }
}

const activateBlock = computed(() => (detail.value ? canActivateAvatar(detail.value) : '档案未加载'))
const activating = ref(false)
async function activate(): Promise<void> {
  if (activateBlock.value || !detail.value || activating.value) return
  activating.value = true
  try {
    await store.activate(personaId.value)
    await load()
  } finally {
    activating.value = false
  }
}

// 大图预览：同屏仅 1 个；传入的已是子组件持有的 objectURL，弹层只展示不另注册 blob，
// 关闭即复用子组件 blob、不占新预算，所有权归子组件（R3-07：移除无对应 registerBlob 的死代码）。
function openPreview(url: string): void {
  preview.value = { open: true, url }
}
function closePreview(): void {
  preview.value = { open: false, url: '' }
}

function onBeforeUnload(e: BeforeUnloadEvent): void {
  if (dirty.value) e.preventDefault()
}

onBeforeRouteLeave(() => {
  if (dirty.value && !confirmLeave.value) {
    confirmLeave.value = true
    return false
  }
  return true
})
function discardAndLeave(): void {
  baseline.value = snapshot()
  confirmLeave.value = false
  router.back()
}

onMounted(() => {
  void load()
  void live.load()
  void listActionLibraries()
    .then((libs) => {
      libOptions.value = [
        { label: '（不启用动作）', value: '' },
        ...libs.map((l) => ({ label: l.name, value: l.id }))
      ]
    })
    .catch(() => {
      libOptions.value = [{ label: '（不启用动作）', value: '' }]
    })
  window.addEventListener('beforeunload', onBeforeUnload)
})
onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
})
</script>

<template>
  <div class="page">
    <header class="head">
      <button type="button" class="back" @click="router.push('/avatars')">← 档案列表</button>
      <h1>{{ isNew ? '新建档案' : `编辑档案：${form.name || personaId}` }}</h1>
      <div class="head__tools">
        <NtButton v-if="!isNew && detail && !detail.is_active" variant="primary" :disabled="!!activateBlock || activating" :title="activateBlock" @click="activate">
          设为当前
        </NtButton>
        <span v-else-if="detail?.is_active" class="pill-active">当前档案</span>
        <NtButton variant="primary" :disabled="saving || !formValid" @click="save">
          {{ saving ? '保存中…' : '保存' }}
        </NtButton>
      </div>
    </header>

    <p v-if="live.isLive" class="live-banner" role="status">开播中，修改将在下一场开播生效。</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="savedFlash" class="saved" aria-live="polite">{{ savedFlash }}</p>
    <p v-if="loading" class="note">正在读取档案…</p>

    <main v-else class="main">
      <section class="panel">
        <h2 class="panel__title">基本信息</h2>
        <AvatarPromptFields v-model="form" :is-new="isNew" @validity="formValid = $event" />
      </section>

      <section class="panel">
        <AvatarImagePicker :persona-id="personaId" :asset-id="imageAssetId" @uploaded="imageAssetId = $event" @preview="openPreview" />
      </section>

      <section class="panel">
        <VoicePicker v-model="voiceId" />
      </section>

      <section class="panel">
        <NtSelect label="动作库" :model-value="actionLibraryId" :options="libOptions" @update:model-value="actionLibraryId = String($event)" />
      </section>

      <section v-if="!isNew && detail" class="panel">
        <RefImageList :persona-id="personaId" :refs="detail.refs ?? []" @change="load" @preview="openPreview" />
      </section>

      <section v-if="!isNew && detail" class="panel">
        <AvatarSyncBar :avatar="detail" @synced="load" />
      </section>
    </main>

    <ImagePreview :open="preview.open" :url="preview.url" alt="形象图大图预览" @close="closePreview" />
    <NtConfirmDialog
      :open="confirmLeave"
      title="放弃未保存修改"
      message="有修改未保存，离开后将丢弃。确定离开？"
      confirm-text="放弃并离开"
      @confirm="discardAndLeave"
      @cancel="confirmLeave = false"
    />
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 100%;
  padding: 12px 16px 24px;
  background: var(--nt-space-bg);
}
.head {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.head h1 {
  margin: 0;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-title);
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.head__tools {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
}
.head__tools :deep(.nt-btn) {
  min-height: 44px;
}
.back {
  height: 44px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-2);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.back:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
}
.live-banner {
  margin: 0;
  padding: 6px 10px;
  border: 1px solid var(--nt-warn-border);
  border-radius: var(--nt-radius-m);
  background: var(--nt-data-raised);
  color: var(--nt-warn);
  font-size: var(--nt-fs-caption);
}
.error {
  margin: 0;
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
.saved {
  margin: 0;
  color: var(--nt-success);
  font-size: var(--nt-fs-caption);
}
.note {
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.pill-active {
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
  font-size: var(--nt-fs-micro);
}
.main {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  max-width: 1080px;
}
.panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: var(--nt-pad-panel);
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
  min-width: 0;
}
.panel__title {
  margin: 0;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-panel);
}
@media (max-width: 1279px) {
  .main {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>

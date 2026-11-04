<script setup lang="ts">
import NtButton from "../components/ui/NtButton.vue"
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { listAvatars, type AvatarListItem } from '../services/api/panels'
import { commitDraft, createDraft, getDraft, listDrafts, openDraft, saveDraft, type DraftDetail, type DraftItem } from '../services/api/drafts'
import { classifyApiError } from '../services/api/client'
import { usePreparationStore } from '../stores/preparation'
import NtSelect from '../components/ui/NtSelect.vue'

const router = useRouter()
const preparation = usePreparationStore()
const drafts = ref<DraftItem[]>([])
const avatars = ref<AvatarListItem[]>([])
const current = ref<DraftDetail | null>(null)
const title = ref('')
const notes = ref('')
const avatarId = ref('')
const busy = ref(false)
const error = ref('')
const notice = ref('')
const storage = ref('')
const options = computed(() => [{ label: '选择数字人档案', value: '' }, ...avatars.value.map(a => ({ label: `${a.name}${a.vidu_sync_state?.state === 'done' ? '' : '（待同步）'}`, value: a.persona_id }))])
const chosen = computed(() => avatars.value.find(a => a.persona_id === avatarId.value))
const locked = computed(() => !!current.value && current.value.status !== 'editing')
const statusLabel = (s: string) => ({ editing: '编辑中', committed: '已提交', manual_required: '需要人工恢复', rolled_back: '已回滚' }[s] || '发布处理中')

async function refresh(): Promise<void> {
  await preparation.load()
  const [rows, profiles] = await Promise.all([listDrafts(preparation.owner), listAvatars(1, 100)])
  drafts.value = rows
  avatars.value = profiles.avatars
  storage.value = profiles.storage
  if (!avatarId.value) avatarId.value = profiles.avatars.find(a => a.is_active)?.persona_id || ''
}
async function perform(action: () => Promise<void>): Promise<void> {
  if (busy.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try { await action() } catch (err) { error.value = classifyApiError(err).message }
  finally { busy.value = false }
}
async function inspect(id: string): Promise<void> {
  await perform(async () => {
    const draft = await getDraft(id, preparation.owner)
    current.value = draft
    title.value = draft.snapshot.title || ''
    notes.value = draft.snapshot.notes || ''
    avatarId.value = draft.snapshot.avatar?.id || ''
  })
}
function newCopy(): void {
  current.value = null
  title.value = title.value ? `${title.value} · 副本` : ''
  error.value = ''; notice.value = '新草稿独立保存，已提交的场次版本保持可追溯。'
}
async function save(): Promise<void> {
  const profile = chosen.value
  if (!profile || !title.value.trim()) throw new Error('请填写场次名称并选择数字人档案')
  const snapshot = { ...(current.value?.snapshot || {}), title: title.value.trim(), notes: notes.value.trim(), avatar: { id: profile.persona_id, revision: profile.revision } }
  if (!current.value) {
    current.value = await createDraft(snapshot)
    // 创建尚未归属，立即取得编辑租约，之后才进入当前操作者的列表。
    current.value = await openDraft(current.value.draft_id, preparation.owner)
  } else {
    const revision = current.value.revision
    const lease = await openDraft(current.value.draft_id, preparation.owner)
    if (lease.revision !== revision) throw new Error('草稿已在其他窗口更新。请从列表重新打开后再编辑；当前输入仍保留。')
    current.value = await saveDraft(lease, snapshot)
  }
  await refresh()
  notice.value = '草稿已保存；提交后可在直播台运行预检。'
}
async function submit(): Promise<void> {
  await perform(async () => {
    await save()
    if (!current.value) return
    current.value = await commitDraft(current.value)
    if (!current.value.committed) throw new Error('发布尚未完成，请刷新查看状态；不要重复创建草稿')
    preparation.select(current.value)
    await refresh()
    notice.value = '已提交并选为下一场。请返回直播台运行预检；尚未开播。'
  })
}
async function choose(): Promise<void> {
  await perform(async () => {
    if (!current.value) return
    const fresh = await getDraft(current.value.draft_id, preparation.owner)
    if (!fresh.committed) throw new Error('请先提交草稿')
    preparation.select(fresh)
    notice.value = '已选为下一场，请返回直播台运行预检。'
  })
}
onMounted(() => void perform(async () => {
  await refresh()
  if (preparation.selected) {
    current.value = preparation.selected
    title.value = current.value.snapshot.title || ''
    notes.value = current.value.snapshot.notes || ''
    avatarId.value = current.value.snapshot.avatar?.id || ''
  }
}))
</script>

<template>
  <div class="prepare-page">
    <header class="prepare-head">
      <NtButton type="button" @click="router.push('/')">返回直播台</NtButton>
      <h1>场次准备</h1>
      <NtButton type="button" :disabled="busy" @click="newCopy">新建草稿 / 另存副本</NtButton>
    </header>
    <p>在这里组织下一场的内容。保存、提交和预检都不会自动开播。</p>
    <p v-if="storage === 'memory'" class="prepare-warn" role="status">当前使用临时存储，服务重启可能丢失草稿与档案。正式使用前需配置数据库及内容存储。</p>
    <p v-if="error" class="prepare-error" role="alert">{{ error }} <NtButton type="button" :disabled="busy" @click="perform(refresh)">刷新重试</NtButton></p>
    <p v-if="notice" class="prepare-notice" aria-live="polite">{{ notice }}</p>
    <div class="prepare-grid">
      <section class="prepare-card" aria-label="场次草稿">
        <h2>1. 场次草稿</h2>
        <p v-if="current">{{ statusLabel(current.status) }} · 版本 {{ current.revision }}</p>
        <label>场次名称<input v-model="title" maxlength="100" :disabled="busy || locked" placeholder="例如：晚间互动试播" /></label>
        <NtSelect v-model="avatarId" label="数字人档案" :options="options" :disabled="busy || locked" />
        <p v-if="chosen">绑定档案版本 {{ chosen.revision }}；人设、音色和开场白来自该档案。档案修改后需要另存并重新提交。</p>
        <label>准备备注<textarea v-model="notes" rows="4" maxlength="4000" :disabled="busy || locked" placeholder="本场主题、待检查素材和运营提醒；备注不会自动播报" /></label>
        <div class="prepare-actions">
          <template v-if="!locked">
            <NtButton type="button" :disabled="busy || !chosen || !title.trim()" @click="perform(save)">保存草稿</NtButton>
            <NtButton type="button" variant="primary" :disabled="busy || !chosen || !title.trim()" @click="submit">保存并提交</NtButton>
          </template>
          <NtButton v-if="current?.committed" type="button" class="prepare-primary" :disabled="busy" @click="choose">用于下一场</NtButton>
          <NtButton v-if="locked" type="button" :disabled="busy" @click="newCopy">另存为新草稿</NtButton>
        </div>
        <p v-if="busy" aria-live="polite">正在处理，请稍候…</p>
      </section>
      <section class="prepare-card" aria-label="已保存草稿">
        <h2>2. 已保存草稿</h2>
        <NtButton type="button" :disabled="busy" @click="perform(refresh)">刷新列表</NtButton>
        <p v-if="!drafts.length">暂无草稿。先填写场次名称、选择档案，再保存。</p>
        <ul class="prepare-list"><li v-for="draft in drafts" :key="draft.draft_id">
          <NtButton type="button" :disabled="busy" :aria-pressed="current?.draft_id === draft.draft_id" @click="inspect(draft.draft_id)">
            {{ draft.summary }} · {{ statusLabel(draft.status) }} · 版本 {{ draft.revision }}
          </NtButton>
        </li></ul>
      </section>
    </div>
    <section class="prepare-card" aria-label="素材准备说明">
      <h2>素材和形象怎么准备</h2>
      <p>形象图片、人设、音色和开场白在“数字人档案”中配置；本地图片可直接上传。同步 Vidu 会把形象提交给服务商，需要已开通的 API 权限与额度。</p>
      <p>音频、口播、歌曲和参考图在直播台各面板管理。这里的准备备注不会代替口播脚本，也不会自动播放素材。</p>
      <div class="prepare-actions"><NtButton type="button" @click="router.push('/avatars')">管理数字人档案</NtButton><NtButton type="button" @click="router.push('/models')">模型与接入</NtButton><NtButton type="button" @click="router.push('/')">管理口播与素材</NtButton></div>
    </section>
  </div>
</template>

<style scoped>
.prepare-page { max-width: 1200px; margin: 0 auto; padding: 16px; color: var(--nt-text-1); }
.prepare-head,.prepare-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
h1 { font-size: var(--nt-fs-title); } h2 { font-size: var(--nt-fs-panel); }
p { color: var(--nt-text-2); line-height: 1.6; overflow-wrap: anywhere; }
.prepare-grid { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr); gap: 16px; }
.prepare-card { min-width: 0; padding: 16px; margin-bottom: 16px; border: 1px solid var(--nt-border-1); border-radius: var(--nt-radius-l); background: var(--nt-data-bg); }
label { display: flex; flex-direction: column; gap: 8px; margin-bottom: 16px; }
input,textarea { width: 100%; box-sizing: border-box; min-height: 44px; padding: 12px; color: var(--nt-text-1); background: var(--nt-data-inset); border: 1px solid var(--nt-border-1); border-radius: var(--nt-radius-m); font: inherit; }
button:focus-visible,input:focus-visible,textarea:focus-visible { outline: 2px solid var(--nt-accent); outline-offset: 4px; }
.prepare-error { color: var(--nt-danger); } .prepare-warn { color: var(--nt-warn); } .prepare-notice { color: var(--nt-success); }
.prepare-list { list-style: none; margin: 12px 0; padding: 0; display: grid; gap: 8px; } .prepare-list button { width: 100%; text-align: left; }
@media (max-width: 799px) { .prepare-grid { grid-template-columns: minmax(0,1fr); } input,textarea { font-size: 16px; } }
</style>

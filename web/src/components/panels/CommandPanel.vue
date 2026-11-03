<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { classifyApiError } from '../../services/api/client'
import NtConfirmDialog from '../ui/NtConfirmDialog.vue'
import {
  createAction,
  createActionLibrary,
  deleteAction,
  getActionQueue,
  getChoreo,
  getChoreoStatus,
  listActionLibraries,
  listActions,
  runChoreoOnce,
  sendAction,
  startChoreo,
  stopChoreo,
  type ActionItem,
  type ActionLibrary,
  type ActionStatus,
  type ChoreoConfig,
  type ChoreoStatus
} from '../../services/api/panels'

defineProps<{ busy?: boolean }>()
const emit = defineEmits<{
  (e: 'stop-actions'): void
  (e: 'clear-actions'): void
}>()

const libraries = ref<ActionLibrary[]>([])
const actions = ref<ActionItem[]>([])
const queue = ref<ActionStatus | null>(null)
const cfg = ref<ChoreoConfig | null>(null)
const choreo = ref<ChoreoStatus | null>(null)
const loading = ref(false)
const error = ref('')
const note = ref('')
const libName = ref('')
const actionName = ref('')

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const [libs, acts, q, config, st] = await Promise.all([
      listActionLibraries(),
      listActions(),
      getActionQueue(),
      getChoreo(),
      getChoreoStatus()
    ])
    libraries.value = libs
    actions.value = acts
    queue.value = q
    cfg.value = config
    choreo.value = st
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

async function fire(text: string): Promise<void> {
  note.value = ''
  error.value = ''
  try {
    const res = await sendAction(text, 'manual')
    note.value = res.ok
      ? res.deferred
        ? `已进入队列（优先级 ${res.priority}）`
        : `已发送（优先级 ${res.priority}）`
      : `被丢弃：${res.dropped || res.reason || '队列已满'}`
    queue.value = await getActionQueue()
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

async function addLibrary(): Promise<void> {
  const name = libName.value.trim()
  if (!name) return
  try {
    await createActionLibrary(name)
    libName.value = ''
    await load()
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

async function addAction(): Promise<void> {
  const name = actionName.value.trim()
  if (!name) return
  if (libraries.value.length === 0) {
    error.value = '请先创建动作库'
    return
  }
  try {
    await createAction(libraries.value[0].id, name, name)
    actionName.value = ''
    actions.value = await listActions()
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

async function removeAction(id: string): Promise<void> {
  try {
    await deleteAction(id)
    actions.value = await listActions()
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

// 破坏性操作确认（动作删除 / 停止全部动作 / 清空队列）。
const confirm = ref<{ title: string; message: string; run: () => void } | null>(null)
function askConfirm(title: string, message: string, run: () => void): void {
  confirm.value = { title, message, run }
}
function onConfirm(): void {
  const c = confirm.value
  confirm.value = null
  c?.run()
}

async function choreoRun(): Promise<void> {
  try {
    const r = await runChoreoOnce()
    choreo.value = r.status
    note.value = `编排已执行 ${r.steps} 步`
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

async function choreoToggle(): Promise<void> {
  try {
    if (choreo.value?.running) {
      choreo.value = (await stopChoreo()).status
      note.value = '编排已停止'
    } else {
      choreo.value = (await startChoreo()).status
      note.value = '编排已启动'
    }
  } catch (err) {
    error.value = classifyApiError(err).message
  }
}

onMounted(load)
onBeforeUnmount(() => (actions.value = []))
</script>

<template>
  <div class="cmd">
    <div class="bar">
      <span class="mono">队列 {{ queue?.queue.length ?? 0 }}/{{ queue?.queue_max ?? 3 }} · 已发 {{ queue?.fired ?? 0 }}</span>
      <button type="button" class="mini" :disabled="loading" @click="load">刷新</button>
    </div>
    <p v-if="loading" class="note">正在读取动作库…</p>
    <p v-else-if="error" class="error" role="alert" aria-live="assertive">{{ error }}</p>
    <p v-if="note" class="ok" aria-live="polite">{{ note }}</p>

    <p v-if="!loading && actions.length === 0" class="note">
      动作库为空。创建动作库与动作后即可手动触发（优先级 manual=2）。
    </p>
    <div v-else class="grid">
      <div v-for="a in actions" :key="a.id" class="act__wrap">
        <button
          type="button"
          class="act"
          :disabled="busy"
          @click="fire(a.name)"
          @contextmenu.prevent="askConfirm('删除动作', `将删除动作「${a.name}」。确定继续？`, () => removeAction(a.id))"
        >
          {{ a.name }}
        </button>
        <button
          type="button"
          class="act__del"
          :aria-label="`删除动作 ${a.name}`"
          title="删除动作"
          @click="askConfirm('删除动作', `将删除动作「${a.name}」。确定继续？`, () => removeAction(a.id))"
        >
          ⋯
        </button>
      </div>
    </div>

    <div class="row">
      <input v-model="libName" type="text" placeholder="新动作库名称" />
      <button type="button" class="mini" :disabled="!libName.trim()" @click="addLibrary">建库</button>
    </div>
    <div class="row">
      <input v-model="actionName" type="text" placeholder="新动作名称（右键动作删除）" @keyup.enter="addAction" />
      <button type="button" class="mini" :disabled="!actionName.trim()" @click="addAction">加动作</button>
    </div>

    <div class="row">
      <button
        type="button"
        class="mini"
        :disabled="busy"
        @click="askConfirm('停止全部动作', '将停止当前正在执行的全部动作。确定继续？', () => emit('stop-actions'))"
      >
        停止当前动作
      </button>
      <button
        type="button"
        class="mini"
        :disabled="busy"
        @click="askConfirm('清空动作队列', '将清空动作队列中全部待执行动作。确定继续？', () => emit('clear-actions'))"
      >
        清空队列
      </button>
    </div>

    <section class="choreo">
      <div class="bar">
        <span class="mono">编排 {{ choreo?.running ? '运行中' : '未运行' }} · 组 {{ cfg?.groups.length ?? 0 }} · 执行 {{ choreo?.runs ?? 0 }}</span>
        <span class="mono">{{ cfg?.step_order === 'order' ? '顺序' : '随机' }}</span>
      </div>
      <p v-if="!cfg || cfg.groups.length === 0" class="note">暂无编排组（可在后端 /api/v1/choreo/groups 创建）。</p>
      <ul v-else class="groups">
        <li v-for="g in cfg.groups" :key="g.id">{{ g.name }} <span class="mono">{{ g.steps.length }} 步</span></li>
      </ul>
      <div class="row">
        <button type="button" class="mini" :disabled="busy" @click="choreoRun">执行一轮</button>
        <button type="button" class="mini" :disabled="busy" @click="choreoToggle">
          {{ choreo?.running ? '停止编排' : '启动编排' }}
        </button>
      </div>
    </section>
    <NtConfirmDialog
      :open="!!confirm"
      :title="confirm?.title ?? ''"
      :message="confirm?.message ?? ''"
      @confirm="onConfirm"
      @cancel="confirm = null"
    />
  </div>
</template>

<style scoped>
.cmd {
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
.ok,
.error {
  margin: 0;
  font-size: var(--nt-fs-micro);
  line-height: 1.5;
  color: var(--nt-text-3);
}
.ok {
  color: var(--nt-text-1);
}
.error {
  color: var(--nt-danger);
}
.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 6px;
}
.act {
  height: 34px;
  padding: 0 8px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.act:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.act__wrap {
  position: relative;
}
.act__wrap .act {
  width: 100%;
}
.act__del {
  position: absolute;
  top: 2px;
  right: 2px;
  width: 22px;
  height: 22px;
  border: none;
  border-radius: var(--nt-radius-s);
  background: transparent;
  color: var(--nt-text-3);
  font: inherit;
  cursor: pointer;
}
.act__del:hover,
.act__del:focus-visible {
  color: var(--nt-danger);
  background: var(--nt-data-raised);
}
.row {
  display: flex;
  gap: 6px;
}
.row input {
  flex: 1;
  min-width: 0;
  height: 32px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.row input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
}
.mini {
  height: 32px;
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
.choreo {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding-top: 6px;
  border-top: 1px solid var(--nt-border-1);
}
.groups {
  margin: 0;
  padding: 0 0 0 14px;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
}
.mono {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
</style>

<script setup lang="ts">
// F42/G18 Vidu 多 key 管理（蓝本 bridge.py:1458,1476 /api/vidu/keys）。
// key 密文落库、面板只显示尾 8 位；激活即时热切换 vidu client（既有资产可能失效，v3 H6 仅提示）。
import { onMounted, ref } from 'vue'
import { classifyApiError } from '../../services/api/client'
import { getViduKeys, viduKeysAction, type ViduKeyEntry } from '../../services/api/panels'

const emit = defineEmits<{ (e: 'changed', activeTail8: string): void }>()

const keys = ref<ViduKeyEntry[]>([])
const active = ref('')
const newKey = ref('')
const alsoActivate = ref(true)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const note = ref('')

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const r = await getViduKeys()
    keys.value = r.keys ?? []
    active.value = r.active ?? ''
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

async function add(): Promise<void> {
  const k = newKey.value.trim()
  if (!k || busy.value) return
  busy.value = true
  error.value = ''
  note.value = ''
  try {
    const r = await viduKeysAction('add', k, alsoActivate.value)
    keys.value = r.keys ?? keys.value
    if (r.active) active.value = r.active
    note.value = alsoActivate.value
      ? '已新增并切换为当前 Key（既有 Vidu 资产可能失效）'
      : '已新增备用 Key'
    emit('changed', active.value)
    newKey.value = ''
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    busy.value = false
  }
}

async function activate(tail8: string): Promise<void> {
  if (busy.value || tail8 === active.value) return
  busy.value = true
  error.value = ''
  note.value = ''
  try {
    const r = await viduKeysAction('activate', tail8)
    keys.value = r.keys ?? keys.value
    active.value = r.active ?? tail8
    note.value = '已切换当前 Key（既有 Vidu 资产可能失效）'
    emit('changed', active.value)
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    busy.value = false
  }
}

async function remove(tail8: string): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  note.value = ''
  try {
    const r = await viduKeysAction('delete', tail8)
    keys.value = r.keys ?? keys.value
    note.value = '已删除备用 Key'
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="vkeys">
    <p v-if="loading" class="vkeys__note">正在读取 Key 列表…</p>
    <p v-if="error" class="vkeys__error" role="alert" aria-live="assertive">{{ error }}</p>
    <p v-if="note" class="vkeys__ok" aria-live="polite">{{ note }}</p>

    <p v-if="!loading && keys.length === 0" class="vkeys__note">
      暂无托管 Key。新增后密文落库，面板只显示尾号。
    </p>
    <ul v-else class="vkeys__list">
      <li v-for="k in keys" :key="k.tail8" :class="{ 'vkeys__row--active': k.tail8 === active }">
        <span class="vkeys__tail mono">…{{ k.tail8 }}</span>
        <span v-if="k.tail8 === active" class="vkeys__badge">生效中</span>
        <span class="vkeys__spacer" />
        <button
          v-if="k.tail8 !== active"
          type="button"
          class="vkeys__btn"
          :disabled="busy"
          @click="activate(k.tail8)"
        >激活</button>
        <button
          v-if="k.tail8 !== active"
          type="button"
          class="vkeys__btn vkeys__btn--danger"
          :disabled="busy"
          @click="remove(k.tail8)"
        >删除</button>
      </li>
    </ul>

    <div class="vkeys__add">
      <input
        v-model="newKey"
        type="password"
        autocomplete="off"
        placeholder="vda_ 开头的完整 Key（仅本次提交，不回显）"
        aria-label="新增 Vidu Key"
        @keyup.enter="add"
      />
      <label class="vkeys__check">
        <input v-model="alsoActivate" type="checkbox" />
        新增并激活
      </label>
      <button type="button" class="vkeys__btn vkeys__btn--primary" :disabled="busy || !newKey.trim()" @click="add">
        {{ busy ? '提交中…' : '新增' }}
      </button>
    </div>
    <p class="vkeys__note">激活立即热切换 Vidu 客户端；删除当前生效 Key 会被拒绝（409）。</p>
  </div>
</template>

<style scoped>
.vkeys {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.vkeys__note,
.vkeys__ok,
.vkeys__error {
  margin: 0;
  font-size: var(--nt-fs-micro);
  line-height: 1.5;
}
.vkeys__note {
  color: var(--nt-text-3);
}
.vkeys__ok {
  color: var(--nt-text-1);
}
.vkeys__error {
  color: var(--nt-danger);
}
.vkeys__list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.vkeys__row--active {
  border-color: var(--nt-accent-border);
}
.vkeys__list li {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
}
.vkeys__tail {
  color: var(--nt-text-1);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-caption);
}
.vkeys__badge {
  padding: 1px 6px;
  border-radius: var(--nt-radius-pill);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
  font-size: var(--nt-fs-micro);
}
.vkeys__spacer {
  flex: 1;
}
.vkeys__btn {
  height: 30px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.vkeys__btn--primary {
  border-color: var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
}
.vkeys__btn--danger {
  color: var(--nt-danger);
}
.vkeys__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.vkeys__add {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.vkeys__add input[type='password'] {
  flex: 1;
  min-width: 220px;
  height: 32px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.vkeys__add input[type='password']:focus {
  outline: none;
  border-color: var(--nt-accent-border);
  box-shadow: 0 0 0 2px var(--nt-focus-ring);
}
.vkeys__check {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
  white-space: nowrap;
}
</style>

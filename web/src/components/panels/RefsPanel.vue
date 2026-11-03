<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ApiError, classifyApiError } from '../../services/api/client'
import { getRefs, removeRefs, switchRefs, type RefsImage, type RefsState } from '../../services/api/panels'

const state = ref<RefsState | null>(null)
const pending = ref<RefsImage[]>([])
const url = ref('')
const imageId = ref('')
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const note = ref('')

async function load(clearError = true): Promise<void> {
  loading.value = true
  if (clearError) error.value = ''
  try {
    state.value = await getRefs()
    pending.value = [...(state.value.images ?? [])]
  } catch (err) {
    error.value = classifyApiError(err).message
  } finally {
    loading.value = false
  }
}

function add(): void {
  const u = url.value.trim()
  if (!u) return
  pending.value.push({ image_id: imageId.value.trim() || `img-${pending.value.length + 1}`, url: u })
  url.value = ''
  imageId.value = ''
  note.value = '已加入待下发列表'
}

async function apply(op: 'switch' | 'remove'): Promise<void> {
  busy.value = true
  error.value = ''
  note.value = ''
  try {
    state.value = op === 'switch' ? await switchRefs(pending.value) : await removeRefs()
    pending.value = [...(state.value.images ?? [])]
    note.value = op === 'switch' ? `切换指令已下发（${pending.value.length} 张）` : '移除指令已下发'
  } catch (err) {
    if (err instanceof ApiError) {
      if (err.status === 502) {
        error.value = `参考图指令失败（type14）：${err.message}`
      } else {
        error.value = classifyApiError(err).message
      }
    } else {
      error.value = classifyApiError(err).message
    }
    // 刷新失败状态但不清掉本次错误提示。
    await load(false).catch(() => undefined)
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="refs">
    <p v-if="loading" class="note">正在读取参考图状态…</p>
    <p v-else-if="error" class="error" role="alert" aria-live="assertive">{{ error }}</p>
    <p v-if="note" class="ok" aria-live="polite">{{ note }}</p>

    <div class="bar">
      <span class="mono">已下发 {{ state?.images.length ?? 0 }} 张 · {{ state?.source || 'fake' }}</span>
      <button type="button" class="mini" :disabled="loading" @click="() => load()">刷新</button>
    </div>

    <p v-if="state?.ack" class="note" aria-live="polite" :class="{ error: !state.ack.success }">
      最近回执：{{ state.ack.success ? '成功' : `失败 ${state.ack.error || ''}` }}
    </p>

    <p v-if="pending.length === 0" class="note">暂无参考图。可粘贴图片 URL 或资产 ID 后下发。</p>
    <ul v-else class="list">
      <li v-for="(img, i) in pending" :key="`${img.image_id}-${i}`">
        <span class="mono">{{ img.image_id }}</span>
        <span class="url">{{ img.url }}</span>
        <button type="button" class="mini" @click="pending.splice(i, 1)">移除</button>
      </li>
    </ul>

    <div class="row">
      <input v-model="imageId" type="text" placeholder="资产 ID（可选）" />
      <input v-model="url" type="text" placeholder="图片 URL" @keyup.enter="add" />
      <button type="button" class="mini" :disabled="!url.trim()" @click="add">添加</button>
    </div>
    <div class="row btns">
      <button type="button" class="mini" :disabled="busy || pending.length === 0" @click="apply('switch')">下发切换</button>
      <button type="button" class="mini" :disabled="busy" @click="apply('remove')">移除全部</button>
    </div>
    <p class="note">参考图以 URL/资产 ID 引用；蓝本未定义二进制上传接口，故不臆造上传。</p>
  </div>
</template>

<style scoped>
.refs {
  display: flex;
  flex-direction: column;
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
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.list li {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-s);
  background: var(--nt-data-inset);
}
.url {
  overflow: hidden;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.row {
  display: flex;
  gap: 6px;
}
.row input {
  flex: 1;
  min-width: 0;
  height: 30px;
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
.mini:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.btns {
  flex-wrap: wrap;
}
.mono {
  color: var(--nt-text-3);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-micro);
}
</style>

<script setup lang="ts">
// F29 贵宾名单管理 UI（专属会员组内嵌）：拉取 /api/v1/patrons，行内增删改，保存全量替换。
// 对齐蓝本 bridge.py:878-940（list/save）；进场欢迎由后端 join 钩子自动播报。
import { onMounted, ref } from 'vue'
import { getPatrons, savePatrons, patronWelcome, type PatronEntry, type PatronsState } from '../../services/api/panels'

const items = ref<PatronEntry[]>([])
const state = ref<PatronsState | null>(null)
const loading = ref(false)
const saving = ref(false)
const msg = ref('')
const err = ref('')
const dirty = ref(false)

async function load(): Promise<void> {
  loading.value = true
  err.value = ''
  try {
    const r = await getPatrons()
    items.value = (r.items || []).map((e) => ({ ...e }))
    state.value = r.state || null
    dirty.value = false
  } catch (e) {
    err.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function addRow(): void {
  items.value.push({ nick: '', uid: '', alias: '', note: '', since: '' })
  dirty.value = true
}

function removeRow(i: number): void {
  items.value.splice(i, 1)
  dirty.value = true
}

function markDirty(): void {
  dirty.value = true
}

async function save(): Promise<void> {
  saving.value = true
  msg.value = ''
  err.value = ''
  try {
    const r = await savePatrons(items.value)
    msg.value = `已保存 ${r.count} 位贵宾`
    dirty.value = false
    await load()
  } catch (e) {
    err.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    saving.value = false
  }
}

async function welcome(nick: string): Promise<void> {
  msg.value = ''
  err.value = ''
  try {
    const r = await patronWelcome(nick)
    msg.value = r.line ? `已欢迎：${r.line}` : `已跳过（${r.skipped || '未知原因'}）`
  } catch (e) {
    err.value = e instanceof Error ? e.message : '欢迎失败'
  }
}

onMounted(load)
</script>

<template>
  <div class="patrons">
    <div class="patrons__meta">
      <span v-if="state">名单 {{ state.count }} 人 · 本场已欢迎 {{ state.welcomed }} 次</span>
      <span v-else-if="loading">加载中…</span>
    </div>

    <table v-if="items.length" class="patrons__table">
      <thead>
        <tr>
          <th>昵称</th>
          <th>UID</th>
          <th>称呼</th>
          <th>备注</th>
          <th aria-label="操作" />
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row, i) in items" :key="i">
          <td><input v-model="row.nick" aria-label="昵称" @input="markDirty" /></td>
          <td><input v-model="row.uid" aria-label="UID" @input="markDirty" /></td>
          <td><input v-model="row.alias" aria-label="称呼" @input="markDirty" /></td>
          <td><input v-model="row.note" aria-label="备注" @input="markDirty" /></td>
          <td class="patrons__ops">
            <button type="button" class="patrons__btn" :disabled="!row.nick" @click="welcome(row.nick)">欢迎</button>
            <button type="button" class="patrons__btn patrons__btn--danger" @click="removeRow(i)">删除</button>
          </td>
        </tr>
      </tbody>
    </table>
    <p v-else class="patrons__empty">暂无贵宾，点击下方按钮添加。</p>

    <div class="patrons__foot">
      <button type="button" class="patrons__btn" @click="addRow">添加贵宾</button>
      <button
        type="button"
        class="patrons__btn patrons__btn--primary"
        :disabled="saving || !dirty"
        @click="save"
      >
        {{ saving ? '保存中…' : '保存名单' }}
      </button>
    </div>
    <p v-if="msg" class="patrons__ok">{{ msg }}</p>
    <p v-if="err" class="patrons__err" role="alert">{{ err }}</p>
  </div>
</template>

<style scoped>
.patrons {
  min-width: 0;
}
.patrons__meta {
  margin-bottom: 8px;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.patrons__table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--nt-fs-caption);
}
.patrons__table th {
  text-align: left;
  padding: 4px 6px;
  color: var(--nt-text-3);
  font-weight: 400;
  font-size: var(--nt-fs-micro);
  border-bottom: 1px solid var(--nt-border-1);
}
.patrons__table td {
  padding: 4px 6px;
  border-bottom: 1px solid var(--nt-border-1);
}
.patrons__table input {
  width: 100%;
  height: 32px;
  padding: 0 8px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
  font-size: var(--nt-fs-caption);
}
.patrons__table input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
  box-shadow: 0 0 0 2px var(--nt-focus-ring);
}
.patrons__ops {
  white-space: nowrap;
}
.patrons__empty {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.patrons__foot {
  display: flex;
  gap: 10px;
  margin-top: 10px;
}
.patrons__btn {
  height: 32px;
  min-height: 32px;
  padding: 0 12px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-1);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.patrons__btn--primary {
  border-color: var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-accent);
}
.patrons__btn--danger {
  color: var(--nt-danger);
}
.patrons__btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.patrons__ok {
  margin-top: 8px;
  color: var(--nt-success);
  font-size: var(--nt-fs-caption);
}
.patrons__err {
  margin-top: 8px;
  color: var(--nt-danger);
  font-size: var(--nt-fs-caption);
}
</style>

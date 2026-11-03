<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import AvatarCard from '../components/avatars/AvatarCard.vue'
import NtButton from '../components/ui/NtButton.vue'
import NtConfirmDialog from '../components/ui/NtConfirmDialog.vue'
import { useAvatarsStore } from '../stores/avatars'
import { releaseBlobs } from '../services/blob-pool'

// 档案列表页（M4）：mem 存储提示条（v3 §7.2）；列表卡片懒加载 + blob 预算；
// 刷新前回收列表 blob（revoke 时机四）。
const router = useRouter()
const store = useAvatarsStore()
const pendingDelete = ref('')
const confirmDelete = ref(false)

onMounted(() => {
  releaseBlobs('avatar-list:')
  void store.load()
})

onBeforeUnmount(() => {
  releaseBlobs('avatar-list:')
})

function refresh(): void {
  releaseBlobs('avatar-list:')
  void store.load()
}

function askDelete(id: string): void {
  pendingDelete.value = id
  confirmDelete.value = true
}

async function doDelete(): Promise<void> {
  confirmDelete.value = false
  if (!pendingDelete.value) return
  await store.remove(pendingDelete.value)
  pendingDelete.value = ''
}

async function duplicate(id: string): Promise<void> {
  await store.duplicate(id)
}

// 防连点（R3-09）：per-id busy 守卫，后端 SetActive 幂等但避免重复请求与重复 load。
const activating = ref<Record<string, boolean>>({})
async function activate(id: string): Promise<void> {
  if (activating.value[id]) return
  activating.value = { ...activating.value, [id]: true }
  try {
    await store.activate(id)
  } finally {
    const rest = { ...activating.value }
    delete rest[id]
    activating.value = rest
  }
}
</script>

<template>
  <div class="page">
    <header class="head">
      <button type="button" class="back" @click="router.push('/')">← 返回直播台</button>
      <h1>数字人档案</h1>
      <div class="head__tools">
        <NtButton size="sm" @click="refresh">刷新</NtButton>
        <NtButton variant="primary" @click="router.push('/avatars/new')">新建档案</NtButton>
      </div>
    </header>

    <p v-if="store.storage === 'memory'" class="mem-banner" role="status">
      当前为内存存储，重启后档案丢失（或运行中切换存储后丢失）。
    </p>
    <p v-if="store.error" class="error" role="alert">{{ store.error }}</p>
    <p v-if="store.loading && !store.items.length" class="note">正在读取档案列表…</p>

    <div v-else-if="!store.items.length" class="empty">
      <p>暂无数字人档案。</p>
      <NtButton variant="primary" @click="router.push('/avatars/new')">创建第一个档案</NtButton>
    </div>

    <div v-else class="grid" role="list" aria-label="档案列表">
      <AvatarCard
        v-for="a in store.items"
        :key="a.persona_id"
        :avatar="a"
        role="listitem"
        @edit="(id) => router.push(`/avatars/${encodeURIComponent(id)}/edit`)"
        @activate="activate"
        @duplicate="duplicate"
        @remove="askDelete"
      />
    </div>

    <NtConfirmDialog
      :open="confirmDelete"
      title="删除档案"
      message="将软删除该档案，不可恢复。确定继续？"
      confirm-text="删除"
      @confirm="doDelete"
      @cancel="confirmDelete = false"
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
}
.head__tools {
  margin-left: auto;
  display: flex;
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
.mem-banner {
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
.note {
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 48px 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.empty :deep(.nt-btn) {
  min-height: 44px;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px;
}
@media (max-width: 1279px) {
  .grid {
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  }
}
@media (max-width: 767px) {
  .grid {
    grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  }
}
@media (max-width: 359px) {
  .grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>

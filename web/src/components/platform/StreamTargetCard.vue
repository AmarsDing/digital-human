<script setup lang="ts">
import { computed, ref } from 'vue'
import { usePlatformStore } from '../../stores/platform'

// 推流目标：stream_key 用 password 输入只写不回显；
// 已配置时只显示指纹，"更换" 才重新展开输入；保存成功后清空 key 输入框。
const store = usePlatformStore()
const rtmpUrl = ref('')
const streamKey = ref('')
const editing = ref(false)
const savedNote = ref('')

const fingerprint = computed(() => store.stream?.keyFingerprint8 ?? '')
const configured = computed(() => Boolean(fingerprint.value) && !editing.value)
const canSave = computed(() => !store.streamUnavailable && !store.targetBusy && Boolean(rtmpUrl.value.trim()) && Boolean(streamKey.value))

async function save(): Promise<void> {
  if (!canSave.value) return
  savedNote.value = ''
  const ok = await store.saveTarget(rtmpUrl.value.trim(), streamKey.value)
  if (ok) {
    streamKey.value = '' // 明文即刻清出 DOM，此后只展示指纹
    editing.value = false
    savedNote.value = '已保存'
  }
}
</script>

<template>
  <section class="pf-card" aria-label="推流目标">
    <div class="pf-card__head">
      <h2 class="pf-card__title">推流目标</h2>
      <span v-if="configured" class="pf-tag pf-mono">已配置 · 指纹 {{ fingerprint }}</span>
    </div>

    <div v-if="configured" class="pf-row">
      <p class="pf-empty">推流地址与密钥已保存，密钥不回显明文。</p>
      <button type="button" class="pf-btn pf-btn--secondary" @click="editing = true">更换</button>
    </div>

    <template v-else>
      <label class="pf-field">
        <span>RTMP 地址</span>
        <input v-model="rtmpUrl" class="pf-input pf-mono" type="text" placeholder="rtmp://…" autocomplete="off" />
      </label>
      <label class="pf-field">
        <span>推流密钥（只写不回显）</span>
        <input v-model="streamKey" class="pf-input pf-mono" type="password" placeholder="stream key" autocomplete="new-password" />
      </label>
      <div class="pf-actions">
        <button type="button" class="pf-btn pf-btn--secondary" :disabled="!canSave" @click="save">
          {{ store.targetBusy ? '保存中…' : '保存推流配置' }}
        </button>
        <button v-if="fingerprint" type="button" class="pf-btn pf-btn--ghost" @click="editing = false">取消</button>
      </div>
    </template>

    <p v-if="savedNote" class="pf-note">{{ savedNote }}</p>
    <p v-if="store.targetError" class="pf-error" role="alert">{{ store.targetError }}</p>
  </section>
</template>

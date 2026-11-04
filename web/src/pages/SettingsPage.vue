<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import SettingsDrawer from '../components/settings/SettingsDrawer.vue'
import NtSelect from '../components/ui/NtSelect.vue'
import { useUiPrefs } from '../stores/uiPrefs'
import { useSettingsStore } from '../stores/settings'

const router = useRouter()
const prefs = useUiPrefs()
const settings = useSettingsStore()

const THEMES = [
  { label: '深色', value: 'dark' },
  { label: '浅色', value: 'light' }
]
const DENSITY = [
  { label: '标准', value: 'normal' },
  { label: '紧凑', value: 'compact' }
]
const MOTION = [
  { label: '完整动效', value: 'full' },
  { label: '减少动效', value: 'reduced' }
]
const QUALITY = [
  { label: '完整视觉', value: 'full' },
  { label: '节能', value: 'eco' }
]
const VISION = [
  { label: '默认', value: 'base' },
  { label: '色觉安全', value: 'safe' }
]

onMounted(() => {
  prefs.load()
  // 已有脏数据时不再拉取覆盖（SettingsDrawer 亦只在首次打开时 load）。
  if (!settings.groups.length && !settings.dirtyCount) void settings.load()
})
</script>

<template>
  <div class="settings-page">
    <header class="head">
      <button type="button" @click="router.push('/')">← 返回直播台</button>
      <h1>设置</h1>
      <span class="head__hint">13 组服务端设置：5 组当场生效 · 8 组下一场生效（人设属下一场）</span>
    </header>

    <div class="body">
      <div class="body__drawer">
        <SettingsDrawer :open="true" inline @close="router.push('/')" />
      </div>

      <aside class="prefs">
        <h2>界面偏好（仅本机）</h2>
        <NtSelect label="主题" :model-value="prefs.theme" :options="THEMES" @update:model-value="prefs.setTheme($event as 'dark' | 'light')" />
        <NtSelect label="密度" :model-value="prefs.density" :options="DENSITY" @update:model-value="prefs.setDensity($event as 'normal' | 'compact')" />
        <NtSelect label="色觉" :model-value="prefs.colorVision" :options="VISION" @update:model-value="prefs.setColorVision($event as 'base' | 'safe')" />
        <NtSelect label="动效" :model-value="prefs.motion" :options="MOTION" @update:model-value="prefs.setMotion($event as 'full' | 'reduced')" />
        <NtSelect label="视觉质量" :model-value="prefs.visualQuality" :options="QUALITY" @update:model-value="prefs.setVisualQuality($event as 'full' | 'eco')" />
        <label class="num">
          <span>右侧栏宽度</span>
          <input
            type="number"
            min="280"
            max="560"
            step="10"
            :value="prefs.rightW"
            @input="prefs.setRightW(Number(($event.target as HTMLInputElement).value))"
          />
        </label>
        <p class="prefs__note">主题/密度/色觉/动效写入 &lt;html&gt; data-*，与 NovaTrader tokens 同源。</p>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.settings-page {
  height: 100%;
  min-height: 100%;
  padding: 12px 16px 24px;
  background: var(--nt-space-bg);
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.head button {
  height: 30px;
  padding: 0 10px;
  border-radius: var(--nt-radius-s);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-raised);
  color: var(--nt-text-2);
  font: inherit;
  font-size: var(--nt-fs-caption);
  cursor: pointer;
}
.head h1 {
  margin: 0;
  color: var(--nt-text-1);
  font-size: var(--nt-fs-title);
}
.head__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.body {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 280px;
  gap: 16px;
  flex: 1;
  min-height: 0;
}
.body__drawer {
  height: 100%;
  min-height: 0;
}
.prefs {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--nt-border-1);
  border-radius: var(--nt-radius-l);
  background: var(--nt-data-bg);
  align-self: start;
}
.prefs h2 {
  margin: 0 0 4px;
  color: var(--nt-text-2);
  font-size: var(--nt-fs-panel);
}
.num {
  display: flex;
  flex-direction: column;
  gap: 6px;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.num input {
  height: 40px;
  padding: 0 10px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.prefs__note {
  margin: 0;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
  line-height: 1.5;
}
@media (max-width: 1023px) {
  .settings-page { height: auto; min-height: 100%; }
  .body {
    display: flex;
    flex-direction: column;
    flex: none;
    overflow: visible;
  }
  .body__drawer {
    height: auto;
    min-height: 0;
  }
  .body__drawer :deep(.nt-drawer--inline) { height: auto; }
  .body__drawer :deep(.nt-drawer__body) { flex: none; overflow: visible; }
}
</style>

<script setup lang="ts">
import NtButton from "../components/ui/NtButton.vue"
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getSettings, saveSettings, type SettingsGroup } from '../services/api/settings'
import { apiRequest, classifyApiError } from '../services/api/client'
import { useSettingsStore } from '../stores/settings'
import NtSelect from '../components/ui/NtSelect.vue'
import ViduKeysPanel from '../components/settings/ViduKeysPanel.vue'

const router = useRouter()
const shared = useSettingsStore()
const groups = ref<SettingsGroup[]>([])
const revision = ref(0)
const baseUrl = ref('')
const originalBase = ref('')
const model = ref('')
const key = ref('')
const keyTail = ref('')
const videoModel = ref('vidu-s2')
const busy = ref(false)
const error = ref('')
const message = ref('')
const capabilities = ref<Record<string, string>>({})
const connectionNote = ref('')
const checkingVidu = ref(false)
async function checkVidu(): Promise<void> {
  if (checkingVidu.value) return
  checkingVidu.value = true; connectionNote.value = ''
  try {
    const r = await apiRequest<{ configured: boolean; remain?: number; remain_credit?: number }>('/api/v1/credits', { timeoutMs: 20000 })
    connectionNote.value = r.configured ? `Vidu API 已连接，可用积分 ${r.remain ?? r.remain_credit ?? '请查看服务商控制台'}。尚未创建直播会话。` : '请先保存并激活 Vidu API Key。'
  } catch (err) { connectionNote.value = `Vidu 连接未通过：${classifyApiError(err).message}` }
  finally { checkingVidu.value = false }
}
const status = (name: string) => ({ real: '已验证', fake: '开发模拟', unconfigured: '未配置', unknown: '待验证' }[capabilities.value[name]] || '待验证')
const provider = computed(() => baseUrl.value.startsWith('https://api.openai.com/') ? 'OpenAI' : baseUrl.value.startsWith('https://api.deepseek.com') ? 'DeepSeek' : '兼容服务')
async function load(): Promise<void> {
  busy.value = true; error.value = ''
  try {
    const result = await getSettings()
    groups.value = result.groups; revision.value = result.revision
    const llm = result.groups.find(g => g.id === 'llm')?.values || {}
    baseUrl.value = String(llm.base_url || 'https://api.deepseek.com')
    originalBase.value = baseUrl.value
    model.value = String(llm.model || '')
    keyTail.value = String(llm.api_key_tail8 || '')
    key.value = ''
    videoModel.value = String(result.groups.find(g => g.id === 'session')?.values.model || 'vidu-s2')
    try { capabilities.value = (await apiRequest<{ capability_matrix: Record<string, string> }>('/healthz')).capability_matrix || {} }
    catch { capabilities.value = {} }
  } catch (err) { error.value = classifyApiError(err).message }
  finally { busy.value = false }
}
async function save(): Promise<void> {
  if (busy.value) return
  error.value = ''; message.value = ''
  if (shared.dirtyCount) { error.value = '全部设置中还有未保存的修改，请先保存或处理后再更换模型。'; return }
  let url: URL
  try { url = new URL(baseUrl.value) } catch { error.value = '请输入有效的服务地址'; return }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) { error.value = '服务地址只支持 HTTP/HTTPS，且不能在地址中携带凭据'; return }
  if (!model.value.trim()) { error.value = '请填写服务商提供的对话模型 ID'; return }
  if (/^(gpt-realtime|gpt-live|sora)/i.test(model.value.trim())) { error.value = '该模型需要专用实时或视频接口，当前文本对话通道不支持，请勿填入此处。'; return }
  if (baseUrl.value.replace(/\/+$/, '') !== originalBase.value.replace(/\/+$/, '') && !key.value.trim()) { error.value = '更换服务地址时请填写对应服务商的新 API Key，避免把旧密钥发送到新服务商。'; return }
  busy.value = true
  try {
    const next: SettingsGroup[] = JSON.parse(JSON.stringify(groups.value))
    const llm = next.find(g => g.id === 'llm')
    const session = next.find(g => g.id === 'session')
    if (!llm || !session) throw new Error('设置不完整，请刷新后重试')
    llm.values = { ...llm.values, base_url: baseUrl.value.replace(/\/+$/, ''), model: model.value.trim(), api_key: key.value.trim() }
    session.values.model = videoModel.value
    const result = await saveSettings(next, revision.value)
    if (!result.saved) throw new Error('保存被拒绝，请检查设置内容')
    key.value = ''
    await load()
    if (!shared.dirtyCount) await shared.load()
    message.value = '模型配置已保存。返回场次准备，重新预检后用于下一场。'
  } catch (err) { error.value = classifyApiError(err).message }
  finally { busy.value = false }
}
onMounted(() => void load())
</script>

<template>
  <div class="models-page">
    <header class="models-head"><NtButton type="button" @click="router.push('/')">返回直播台</NtButton><h1>模型与接入</h1><NtButton type="button" @click="router.push('/prepare')">场次准备</NtButton></header>
    <p>视频负责形象与口型，对话负责回答，语音负责听说。三类能力分别配置，换模型后需重新预检。</p>
    <p v-if="error" class="models-error" role="alert">{{ error }}</p>
    <p v-if="message" class="models-success" aria-live="polite">{{ message }}</p>
    <form @submit.prevent="save">
      <div class="models-grid">
        <section class="models-card">
          <h2>数字人视频</h2><p>Vidu · {{ status('vidu') }}</p>
          <NtSelect v-model="videoModel" label="视频模型" :options="[{label:'Vidu S2',value:'vidu-s2'},{label:'Vidu S1（须有对应权限）',value:'vidu-s1'}]" :disabled="busy" />
          <p>当前项目的视频适配器为 Vidu。更换为其他视频厂商需要单独接入，OpenAI 实时语音不能直接替代视频输出。</p>
          <ViduKeysPanel />
          <NtButton type="button" :disabled="checkingVidu" @click="checkVidu">{{ checkingVidu ? '验证中…' : '验证 Vidu 连接与余额' }}</NtButton>
          <p v-if="connectionNote" role="status">{{ connectionNote }}</p>
          <p>先保存并激活 Key，再验证连接。此操作不创建直播会话；真实开播仍需通过语音、观看与推流预检。</p>
        </section>
        <section class="models-card">
          <h2>文本对话</h2><p>{{ provider }} · 支持兼容的流式对话接口</p>
          <div class="models-actions"><NtButton type="button" :disabled="busy" @click="baseUrl = 'https://api.openai.com/v1'">填写 OpenAI 地址</NtButton><NtButton type="button" :disabled="busy" @click="baseUrl = 'https://api.deepseek.com'">填写 DeepSeek 地址</NtButton></div>
          <label>服务地址<input v-model="baseUrl" type="url" required :disabled="busy" autocomplete="off" /></label>
          <label>对话模型 ID<input v-model="model" required :disabled="busy" placeholder="填写该账号可调用的文本对话模型" autocomplete="off" /></label>
          <label>API Key<input v-model="key" type="password" :disabled="busy" :placeholder="keyTail ? `已配置，尾号 ${keyTail}；同地址留空保留` : '填写此服务商的 API Key'" autocomplete="new-password" /></label>
          <p>地址包含服务商要求的版本前缀（如 OpenAI 的 /v1）。更换地址必须更换密钥；保存后不回显完整密钥。</p>
        </section>
        <section class="models-card">
          <h2>语音与实时交互</h2><p>当前语音合成：{{ status('tts') }}</p>
          <p>现有链路使用已配置的语音识别、文本对话和 TTS。音色在数字人档案中选择。</p>
          <p>OpenAI GPT-Live / Realtime：尚未接入本项目的实时音频通道，暂不可切换。接入后仍需视频引擎驱动数字人画面。</p>
          <NtButton type="button" @click="router.push('/avatars')">管理形象与音色</NtButton>
        </section>
      </div>
      <div class="models-actions"><NtButton variant="primary" type="submit" :disabled="busy || !groups.length">{{ busy ? '处理中…' : '保存模型配置' }}</NtButton><NtButton type="button" :disabled="busy" @click="load">重新读取配置</NtButton></div>
    </form>
  </div>
</template>

<style scoped>
.models-page { max-width: 1200px; margin: 0 auto; padding: 16px; color: var(--nt-text-1); }
.models-head,.models-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; margin-bottom: 16px; }
h1 { font-size: var(--nt-fs-title); } h2 { font-size: var(--nt-fs-panel); }
p { line-height: 1.6; color: var(--nt-text-2); overflow-wrap: anywhere; }
.models-grid { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 16px; margin-bottom: 16px; }
.models-card { min-width: 0; padding: 16px; border: 1px solid var(--nt-border-1); border-radius: var(--nt-radius-l); background: var(--nt-data-bg); }
label { display: flex; flex-direction: column; gap: 8px; margin: 16px 0; }
input,button { min-height: 44px; box-sizing: border-box; padding: 8px 12px; font: inherit; color: var(--nt-text-1); border: 1px solid var(--nt-border-1); border-radius: var(--nt-radius-m); }
input { width: 100%; background: var(--nt-data-inset); font-family: var(--nt-font-mono); } button { background: var(--nt-data-raised); cursor: pointer; }
.models-error { color: var(--nt-danger); } .models-success { color: var(--nt-success); }
@media(max-width:799px) { .models-grid { grid-template-columns: minmax(0,1fr); } input { font-size: 16px; } }
</style>

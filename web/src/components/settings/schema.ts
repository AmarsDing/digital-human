// 12 组设置字段定义：只描述“本机 UI 需要的形状”，密钥类字段只读展示尾号。
export type FieldType = 'text' | 'number' | 'toggle' | 'select' | 'textarea' | 'secret' | 'readonly'

export interface FieldSpec {
  key: string
  label: string
  type: FieldType
  options?: { label: string; value: string }[]
  hint?: string
  min?: number
  max?: number
  step?: number
  suffix?: string
}

export interface GroupSpec {
  id: string
  title: string
  effective: 'immediate' | 'next_session'
  note?: string
  fields: FieldSpec[]
  defaults: Record<string, unknown>
}

export const GROUP_SPECS: GroupSpec[] = [
  {
    id: 'session',
    title: '会话',
    effective: 'next_session',
    note: '模型、RTC 模式与形象在下次连线时生效。',
    fields: [
      {
        key: 'model',
        label: '模型',
        type: 'select',
        options: [
          { label: 'vidu-s2', value: 'vidu-s2' },
          { label: 'vidu-s1', value: 'vidu-s1' }
        ]
      },
      {
        key: 'rtc_mode',
        label: 'RTC 模式',
        type: 'select',
        options: [
          { label: '托管 managed', value: 'managed' },
          { label: '自备 customer', value: 'customer' }
        ]
      },
      {
        key: 'provider',
        label: 'RTC Provider',
        type: 'select',
        options: [
          { label: 'ARTC', value: 'artc' },
          { label: 'TRTC', value: 'trtc' }
        ]
      },
      { key: 'avatar_id', label: '形象 ID', type: 'text' },
      { key: 'image_uri', label: '形象图片 URI', type: 'text', hint: '与形象 ID 至少填一项' },
      { key: 'greeting', label: '开场白', type: 'textarea' },
      { key: 'idle_timeout_s', label: '闲置挂断', type: 'number', min: 30, max: 3600, suffix: '秒' },
      { key: 'max_duration_s', label: '单场上限', type: 'number', min: 300, max: 14400, suffix: '秒' },
      { key: 'record_enabled', label: '托管录制', type: 'toggle' }
    ],
    defaults: {
      model: 'vidu-s2',
      rtc_mode: 'managed',
      provider: 'artc',
      avatar_id: '',
      image_uri: '',
      greeting: '',
      idle_timeout_s: 300,
      max_duration_s: 1800,
      record_enabled: false
    }
  },
  {
    id: 'voice',
    title: '声音',
    effective: 'immediate',
    note: '音色切换即时生效；喂流帧长在下次连线生效。OmniVoice 边车未接入前只能查看。',
    fields: [
      { key: 'voice_id', label: '音色预设', type: 'text', hint: '来自边车 /v1/voices（未接入）' },
      { key: 'preview_text', label: '试听文本', type: 'text' },
      { key: 'frame_ms', label: '喂流帧长', type: 'select', options: [
        { label: '50ms', value: '50' },
        { label: '100ms', value: '100' }
      ], hint: '100ms = 4800B @24kHz' },
      { key: 'output_sentence', label: '按句输出 type10', type: 'toggle' }
    ],
    defaults: {
      voice_id: '',
      preview_text: '你好，我是小雅。',
      frame_ms: '100',
      output_sentence: true
    }
  },
  {
    id: 'member',
    title: '专属会员',
    effective: 'immediate',
    note: '会员名单与欢迎语由 PatronService 管理（后端未实现）。',
    fields: [
      { key: 'enabled', label: '启用', type: 'toggle' },
      { key: 'display_name', label: '称呼', type: 'text' },
      { key: 'title', label: '备注', type: 'text' },
      { key: 'threshold_diamond', label: '门槛钻石', type: 'number', min: 0, suffix: '钻石' },
      { key: 'cooldown_s', label: '欢迎冷却', type: 'number', min: 5, suffix: '秒' },
      { key: 'once_per_session', label: '每场仅一次', type: 'toggle' }
    ],
    defaults: {
      enabled: true,
      display_name: '',
      title: '',
      threshold_diamond: 99,
      cooldown_s: 30,
      once_per_session: true
    }
  },
  {
    id: 'persona',
    title: '人设',
    effective: 'next_session',
    note: '保存人设需后端 PersonaService；应用后清空记忆（ResetLlm 即时）。',
    fields: [
      { key: 'active_id', label: '当前人设 ID', type: 'text' },
      { key: 'content', label: '人设文本', type: 'textarea' }
    ],
    defaults: { active_id: '', content: '' }
  },
  {
    id: 'llm',
    title: 'LLM',
    effective: 'next_session',
    note: 'Key 只显示尾号，写入需后端密钥托管（未实现）；清空记忆按钮即时生效。',
    fields: [
      { key: 'base_url', label: 'Base URL', type: 'text' },
      { key: 'model', label: '模型', type: 'text' },
      { key: 'api_key_tail8', label: 'Key 尾号', type: 'secret', hint: '只读' },
      { key: 'temperature', label: 'temperature', type: 'number', min: 0, max: 2, step: 0.1 },
      { key: 'max_tokens', label: 'max_tokens', type: 'number', min: 64, max: 32768 },
      { key: 'system', label: 'System Prompt', type: 'textarea' }
    ],
    defaults: {
      base_url: '',
      model: '',
      api_key_tail8: '',
      temperature: 0.7,
      max_tokens: 1024,
      system: ''
    }
  },
  {
    id: 'rtc',
    title: '自备 RTC',
    effective: 'next_session',
    note: '自备 RTC 需要后端 RtcService 签发 token；当前仅保存参数，不能入会。',
    fields: [
      { key: 'mode', label: '模式', type: 'select', options: [
        { label: '托管 managed', value: 'managed' },
        { label: '自备 customer', value: 'customer' }
      ] },
      { key: 'provider', label: 'Provider', type: 'select', options: [
        { label: 'ARTC', value: 'artc' },
        { label: 'TRTC', value: 'trtc' }
      ] },
      { key: 'artc_app_id', label: 'ARTC AppID', type: 'text' },
      { key: 'artc_app_key_tail8', label: 'ARTC AppKey 尾号', type: 'secret' },
      { key: 'artc_token_form', label: 'ARTC token 形态', type: 'select', options: [
        { label: '形态 1', value: '1' },
        { label: '形态 2', value: '2' },
        { label: '形态 3', value: '3' }
      ] },
      { key: 'trtc_app_id', label: 'TRTC AppID', type: 'text' },
      { key: 'trtc_key_tail8', label: 'TRTC Key 尾号', type: 'secret' }
    ],
    defaults: {
      mode: 'managed',
      provider: 'artc',
      artc_app_id: '',
      artc_app_key_tail8: '',
      artc_token_form: '1',
      trtc_app_id: '',
      trtc_key_tail8: ''
    }
  },
  {
    id: 'action_library',
    title: '动作库',
    effective: 'next_session',
    note: '动作库 CRUD 由 ActionService 提供（后端未实现）。',
    fields: [
      { key: 'enabled', label: '启用', type: 'toggle' },
      { key: 'library_id', label: '库 ID', type: 'text' }
    ],
    defaults: { enabled: true, library_id: '' }
  },
  {
    id: 'assets',
    title: '资产库',
    effective: 'next_session',
    note: '资产拉取/上传由资产接口提供（后端未实现）。',
    fields: [
      { key: 'avatar_id', label: '当前形象', type: 'text' },
      { key: 'ref_image_count', label: '参考图数量', type: 'readonly' }
    ],
    defaults: { avatar_id: '', ref_image_count: 0 }
  },
  {
    id: 'vidu_key',
    title: 'Vidu Key',
    effective: 'next_session',
    note: 'Key 明文不落前端；启用/删除需后端密钥托管（未实现）。',
    fields: [
      { key: 'active_tail8', label: '当前生效尾号', type: 'secret' },
      { key: 'backup_count', label: '备用数量', type: 'readonly' },
      { key: 'new_key_tail8', label: '新增 Key 尾号', type: 'secret', hint: '只读占位' }
    ],
    defaults: { active_tail8: '', backup_count: 0, new_key_tail8: '' }
  },
  {
    id: 'captions',
    title: '画面内字幕层',
    effective: 'immediate',
    note: '字幕层为浏览器叠加渲染，保存即生效。',
    fields: [
      { key: 'enabled', label: '显示', type: 'toggle' },
      { key: 'offset_px', label: '下移', type: 'number', min: 0, max: 1080, suffix: 'px' },
      { key: 'font_size', label: '字号', type: 'number', min: 10, max: 48, suffix: 'px' },
      { key: 'max_lines', label: '行数', type: 'number', min: 1, max: 6 },
      { key: 'show_events', label: '显示事件', type: 'toggle', hint: '控制右栏事件页签显隐' }
    ],
    defaults: {
      enabled: true,
      offset_px: 200,
      font_size: 17,
      max_lines: 2,
      show_events: false
    }
  },
  {
    id: 'danmaku',
    title: '弹幕主播模式',
    effective: 'immediate',
    note: '模式与播报间隔即时生效。',
    fields: [
      { key: 'mode', label: '模式', type: 'select', options: [
        { label: '关闭', value: 'off' },
        { label: '确认后播报', value: 'confirm' },
        { label: '自动播报', value: 'auto' }
      ] },
      { key: 'read_interval_ms', label: '播报间隔', type: 'number', min: 500, step: 100, suffix: 'ms' },
      { key: 'vip_interval_ms', label: 'VIP 间隔', type: 'number', min: 500, step: 100, suffix: 'ms' }
    ],
    defaults: { mode: 'off', read_interval_ms: 3000, vip_interval_ms: 2000 }
  },
  {
    id: 'misc',
    title: '其他',
    effective: 'next_session',
    note: '积分查询依赖 Vidu credits 接口；余额只读。',
    fields: [
      { key: 'credits_balance', label: '积分余额', type: 'readonly' },
      { key: 'record_enabled', label: '录制', type: 'toggle' },
      { key: 'lan_confirm', label: '局域网二次确认', type: 'toggle' }
    ],
    defaults: { credits_balance: '—', record_enabled: false, lan_confirm: true }
  }
]

export function specOf(id: string): GroupSpec | undefined {
  return GROUP_SPECS.find((g) => g.id === id)
}

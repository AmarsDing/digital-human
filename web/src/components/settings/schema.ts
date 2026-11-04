// 13 组设置字段定义：只描述“本机 UI 需要的形状”，密钥类字段只读展示尾号。
export type FieldType = 'text' | 'number' | 'toggle' | 'select' | 'textarea' | 'secret' | 'readonly' | 'custom'

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
  format?: 'csv'
  // 存储为整数毫秒、表单以秒展示；仅用于显式声明的字段。
  unitConversion?: 'ms-to-s'
  // 只读派生字段的跳转链接（v3 §1.2：已迁移至数字人档案）。
  linkTo?: string
  linkLabel?: string
}

export interface GroupSpec {
  id: string
  title: string
  effective: 'immediate' | 'next_session'
  note?: string
  fields: FieldSpec[]
  defaults: Record<string, unknown>
}

export function millisecondsToSeconds(milliseconds: number): number {
  if (!Number.isFinite(milliseconds)) throw new RangeError('milliseconds must be finite')
  return milliseconds / 1000
}

// 后端只接受整数毫秒；多余精度向零截断，超出安全整数范围则拒绝写入。
export function secondsToMilliseconds(seconds: number): number {
  if (!Number.isFinite(seconds)) throw new RangeError('seconds must be finite')
  const milliseconds = Math.trunc(seconds * 1000)
  if (!Number.isSafeInteger(milliseconds)) throw new RangeError('milliseconds exceed safe integer range')
  return milliseconds
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
      { key: 'avatar_id', label: '形象 ID', type: 'readonly', hint: '已迁移至数字人档案', linkTo: '/avatars', linkLabel: '前往档案页' },
      { key: 'image_uri', label: '形象图片 URI', type: 'readonly', hint: '已迁移至数字人档案', linkTo: '/avatars', linkLabel: '前往档案页' },
      { key: 'greeting', label: '开场白', type: 'textarea' },
      { key: 'greeting_audio_file', label: '开场白音频文件', type: 'text', hint: '配置后优先于文本；解码或播放失败会显式报错，不回退 TTS' },
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
      greeting_audio_file: '',
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
      { key: 'voice_id', label: '音色预设', type: 'readonly', hint: '已迁移至数字人档案', linkTo: '/avatars', linkLabel: '前往档案页' },
      { key: 'preview_text', label: '试听文本', type: 'text' },
      { key: 'speed', label: '语速', type: 'number', step: 0.1 },
      { key: 'frame_ms', label: '喂流帧长', type: 'select', options: [
        { label: '50ms', value: '50' },
        { label: '100ms', value: '100' }
      ], hint: '100ms = 4800B @24kHz' },
      { key: 'output_sentence', label: '按句输出 type10', type: 'toggle' }
    ],
    defaults: {
      voice_id: '',
      preview_text: '你好，这是一段语音试听。',
      speed: 1.0,
      frame_ms: '100',
      output_sentence: true
    }
  },
  {
    id: 'member',
    title: '专属会员',
    effective: 'immediate',
    note: '贵宾名单在下方管理；贵宾礼按单件钻石价 > 门槛触发（连击按单价），每场每人一次 + 冷却双闸。',
    fields: [
      { key: 'enabled', label: '启用', type: 'toggle' },
      { key: 'threshold_diamond', label: '门槛钻石', type: 'number', min: 0, suffix: '钻石' },
      { key: 'cooldown_s', label: '欢迎冷却', type: 'number', min: 5, suffix: '秒' },
      { key: 'once_per_session', label: '每场仅一次', type: 'toggle' },
      { key: 'template', label: '贵宾礼模板', type: 'text', hint: '{nick}/{item} 占位' },
      { key: 'forward_joins', label: '进场欢迎', type: 'toggle' },
      { key: 'welcome_template', label: '进场欢迎模板', type: 'text', hint: '{nick}/{note} 占位' },
      { key: 'patrons', label: '贵宾名单', type: 'custom' }
    ],
    defaults: {
      enabled: true,
      threshold_diamond: 99,
      cooldown_s: 30,
      once_per_session: true,
      template: '{nick}，欢迎光临！',
      forward_joins: true,
      welcome_template: '诶，{nick}来啦，欢迎欢迎。',
      patrons: []
    }
  },
  {
    id: 'persona',
    title: '人设',
    effective: 'next_session',
    note: '人设提示词已迁移至数字人档案（v3 §1.2 唯一真相源 = Persona.system_prompt）；以下为当前 active 档案的只读派生值，修改请到档案页。',
    fields: [
      { key: 'active_id', label: '当前档案 ID', type: 'readonly', linkTo: '/avatars', linkLabel: '前往档案页' },
      { key: 'content', label: '人设提示词（只读派生）', type: 'readonly', hint: '已迁移至数字人档案', linkTo: '/avatars', linkLabel: '前往档案页' }
    ],
    defaults: { active_id: '', content: '' }
  },
  {
    id: 'gift',
    title: '礼物合并播报',
    effective: 'immediate',
    note: '同人同礼在窗口内合并为一条感谢；达到人数/钻石阈值立即播报，否则窗口到期再播。',
    fields: [
      { key: 'enabled', label: '启用', type: 'toggle' },
      { key: 'window_s', label: '合并窗口', type: 'number', min: 1, suffix: '秒' },
      { key: 'flush_people', label: '立即播报人数', type: 'number', min: 1, suffix: '人' },
      { key: 'flush_diamond', label: '立即播报钻石', type: 'number', min: 1, suffix: '钻石' },
      { key: 'max_names', label: '最多上榜', type: 'number', min: 1, max: 8, suffix: '名' },
      { key: 'min_interval_s', label: '最小间隔', type: 'number', min: 0, suffix: '秒' },
      { key: 'urgent_min_interval_s', label: '紧急最小间隔', type: 'number', min: 0, suffix: '秒' },
      { key: 'max_per_minute', label: '每分钟最多', type: 'number', min: 1, suffix: '次' },
      { key: 'max_items', label: '窗口内最多组合', type: 'number', min: 4, suffix: '组' }
    ],
    defaults: {
      enabled: true,
      window_s: 6,
      flush_people: 4,
      flush_diamond: 50,
      max_names: 4,
      min_interval_s: 25,
      urgent_min_interval_s: 10,
      max_per_minute: 2,
      max_items: 12
    }
  },
  {
    id: 'llm',
    title: 'LLM',
    effective: 'next_session',
    note: 'API Key 只写不回显（保存后仅见尾号）；降级模型在主模型重试 2 次失败后依次顶替；清空记忆按钮即时生效。',
    fields: [
      { key: 'base_url', label: 'Base URL', type: 'text' },
      { key: 'model', label: '模型', type: 'text' },
      { key: 'fallback_models', label: '降级模型', type: 'text', format: 'csv', hint: '逗号分隔，主模型失败时依次降级' },
      { key: 'api_key', label: 'API Key', type: 'secret', hint: '只写不回显' },
      { key: 'api_key_tail8', label: 'Key 尾号', type: 'secret', hint: '只读' },
      { key: 'temperature', label: 'temperature', type: 'number', min: 0, max: 2, step: 0.1 },
      { key: 'max_tokens', label: 'max_tokens', type: 'number', min: 64, max: 32768 },
      { key: 'system', label: '全局 System Prompt', type: 'textarea', hint: '第二来源；档案人设优先，相同内容自动去重' }
    ],
    defaults: {
      base_url: 'https://api.deepseek.com',
      model: '',
      fallback_models: [],
      api_key: '',
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
      { key: 'avatar_id', label: '当前形象', type: 'readonly', hint: '已迁移至数字人档案', linkTo: '/avatars', linkLabel: '前往档案页' },
      { key: 'ref_image_count', label: '参考图数量', type: 'readonly' }
    ],
    defaults: { avatar_id: '', ref_image_count: 0 }
  },
  {
    id: 'vidu_key',
    title: 'Vidu Key',
    effective: 'next_session',
    note: 'Key 密文落库、只展示尾号；下方管理区支持新增/激活/删除（激活即时热切换）。',
    fields: [{ key: 'active_tail8', label: '当前生效尾号', type: 'secret' }],
    defaults: { active_tail8: '' }
  },
  {
    id: 'captions',
    title: '画面内字幕层',
    effective: 'immediate',
    note: '字幕层为浏览器叠加渲染，保存即生效。',
    fields: [
      { key: 'enabled', label: '显示', type: 'toggle' },
      { key: 'output_sentences', label: '按句输出', type: 'toggle' },
      { key: 'offset_px', label: '下移', type: 'number', min: 0, max: 1080, suffix: 'px' },
      { key: 'font_size', label: '字号', type: 'number', min: 10, max: 48, suffix: 'px' },
      { key: 'max_lines', label: '行数', type: 'number', min: 1, max: 6 },
      { key: 'show_events', label: '显示事件', type: 'toggle', hint: '控制右栏事件页签显隐' }
    ],
    defaults: {
      enabled: true,
      output_sentences: true,
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
        { label: '自动播报', value: 'auto' },
        { label: '顺序复读', value: 'read_all' },
        { label: '弹幕主播（复读+AI 回答）', value: 'dm_anchor' }
      ], hint: 'read_all 按顺序只复读；dm_anchor 取最新弹幕复读并给 AI 完整回答（需配 LLM）' },
      { key: 'read_interval_ms', label: '播报间隔', type: 'number', min: 0.5, step: 0.1, suffix: '秒', unitConversion: 'ms-to-s' },
      { key: 'vip_interval_ms', label: 'VIP 间隔', type: 'number', min: 500, step: 100, suffix: 'ms' },
      { key: 'block_words', label: '屏蔽词（逗号/换行分隔）', type: 'textarea' }
    ],
    defaults: { mode: 'off', read_interval_ms: 20000, vip_interval_ms: 2000, block_words: '' }
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

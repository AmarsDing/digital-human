import type { RtcAdapter, RtcAdapterFactory } from './types'

declare global {
  interface Window {
    __DH_RTC_FACTORY__?: RtcAdapterFactory
  }
}

export class RtcSdkMissingError extends Error {
  constructor(provider: string) {
    super(`RTC SDK 未自托管（${provider}），请把 vendor SDK 放入 /vendor 并注册 __DH_RTC_FACTORY__`)
    this.name = 'RtcSdkMissingError'
  }
}

// 唯一集成点：vendor 自托管脚本自行注册工厂，避免把未验证的厂商 API 写死进业务层。
export function createRtcAdapter(provider: 'artc' | 'trtc'): RtcAdapter {
  const factory = window.__DH_RTC_FACTORY__
  if (!factory) throw new RtcSdkMissingError(provider)
  return factory.create(provider)
}

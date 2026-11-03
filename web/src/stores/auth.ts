import { defineStore } from 'pinia'
import { setBearer, setAuthErrorHandler } from '../services/api/client'

// Bearer 只驻留内存：不写 localStorage，避免局域网机器上长期明文留存。
export const useAuthStore = defineStore('auth', {
  state: () => ({
    token: '',
    checking: false,
    error: ''
  }),
  getters: {
    hasToken: (s) => s.token.length > 0
  },
  actions: {
    apply(token: string): void {
      this.token = token.trim()
      this.error = ''
      setBearer(this.token)
    },
    markInvalid(): void {
      this.error = '凭证失效，请重新填写 Bearer'
    },
    logout(): void {
      this.apply('')
    }
  }
})

// apiRequest 命中 401 时标记凭证失效；延迟取 store 以保证 pinia 已激活。
setAuthErrorHandler(() => {
  try {
    useAuthStore().markInvalid()
  } catch {
    // pinia 尚未激活（如测试早期）：忽略
  }
})

import { createPinia } from 'pinia'

// 全站唯一 Pinia 实例。App.vue / 全部 store 依赖它，缺少 boot 会导致首屏白屏。
export default ({ app }) => {
  app.use(createPinia())
}

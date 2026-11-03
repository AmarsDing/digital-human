import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 组件测试只加载 Nt*/业务组件，不启动 Quasar 壳；QW 组件未使用。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      src: fileURLToPath(new URL('./src', import.meta.url)),
      app: fileURLToPath(new URL('./src', import.meta.url)),
      components: fileURLToPath(new URL('./src/components', import.meta.url)),
      stores: fileURLToPath(new URL('./src/stores', import.meta.url)),
      services: fileURLToPath(new URL('./src/services', import.meta.url))
    }
  },
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.ts']
  }
})

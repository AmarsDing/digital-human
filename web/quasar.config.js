// Quasar 只作为工程壳：不启用 Material 皮肤，视觉原子全部走 Nt* + NovaTrader tokens。
// 详见方案 §6.9（与 R-UI-12 的偏差申请）。
import { configure } from 'quasar/wrappers'

export default configure(function () {
  return {
    boot: ['pinia'],
    css: ['app.css'],
    extras: [],
    build: {
      target: { browser: ['es2022', 'chrome100', 'firefox100', 'safari15'] },
      vueRouterMode: 'history',
      publicPath: '/',
      // Quasar 只当工程壳：项目内零 q-* 组件/类名，默认注入的 240KB 基础样式全部未使用。
      // 这里用虚拟模块把 quasar 的预编译样式裁掉，视觉原子全部来自 app.css + tokens。
      extendViteConf(viteConf) {
        viteConf.plugins = viteConf.plugins || []
        viteConf.plugins.push({
          name: 'strip-unused-quasar-css',
          enforce: 'pre',
          resolveId(id) {
            if (/^quasar\/dist\/quasar\.(sass|css)$/.test(id)) return '\0quasar-empty.css'
            return null
          },
          load(id) {
            if (id === '\0quasar-empty.css') {
              return '/* quasar base css stripped: 本项目不使用任何 q-* 组件样式 */'
            }
            return null
          }
        })
      }
    },
    devServer: {
      port: 9000,
      proxy: {
        // 开发期只代理到本机后端；lan 模式仍需 Bearer，不在这里放宽鉴权。
        '/api': { target: 'http://127.0.0.1:8795', changeOrigin: false },
        '/healthz': { target: 'http://127.0.0.1:8795', changeOrigin: false },
        '/ws': { target: 'ws://127.0.0.1:8795', ws: true }
      }
    },
    framework: {
      config: {},
      plugins: []
    }
  }
})

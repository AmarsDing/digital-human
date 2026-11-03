# 数字人直播 · 前端独立审查报告

- 审查对象：`f:\myproject\teamwork\digital-human\web`（Quasar 2.35.2 + Vue 3.5 + Pinia 2 + TypeScript 5.9）
- 基准文档：`f:\myproject\teamwork\digital-human-go-plan.md`（v2.0，§6 前端与 R-UI 规范）
- 样式规范来源：`f:\myproject\teamwork\NovaTrader`（`web/src/theme/tokens.css`、`doc/开发文档/前端UI/README.md` 的 R-UI 条目）
- 蓝本行为参考：`f:\myproject\teamwork\cyber-xiaoya-live-main\stage\index.html`（仅行为，未参考 DOM/结构/代码）
- 审查方式：只读源码 + 实际构建/测试 + 起真实后端与前端服务做运行时验证；**未修改任何业务代码，未 git 提交，未安装系统软件**
- 审查日期：2026-11-03
- 时点快照标注（2026-11-04）：文中「54 条面板路由」等计数均为评审当日口径；主线已陆续交付 DZ/G 批等接线，现口径为 **85 条面板路由 + 1 条独立 preflight 路由**（面板单一事实来源 `web/src/services/api/panel-routes.json`，分组见 `docs/api-contracts.md` §2）。评审统计本体保留不改。

---

## 0. 结论速览

**总体判定：不可交付。**

原因不是"有小毛病"，而是**前端在开发服务器与生产构建下均渲染为空白页**：Pinia 从未安装，`App.vue` 在 `setup` 中调用 `useStageStore()` 直接抛错，`#q-app` 内 0 个子节点。此外 3 项核心能力（直播画面、实时字幕、弹幕面板）在前后端链路上尚不存在，事件流状态机存在会导致缓冲反复清空/重复连接的竞态。

| 维度 | 结论 |
|---|---|
| 能否启动 | ❌ dev/prod 均白屏（P0-1） |
| 规范（R-UI/token） | ⚠ 大部分落地，但 10 处裸色值、无刻度间距、无断点、抽屉越级 |
| 架构分层 | ⚠ 大体守住，但存在第二套未鉴权 store（`stage.js`）与"组件内直接 fetch" |
| 实时状态 | ❌ epoch 语义不一致 + 重连竞态 + 批量回放丢事件 |
| RTC | ❌ 四态恒为 `unconfigured`，无 token 接口、无 SDK、无适配器 |
| 面板完整性 | ⚠ 54 条面板路由覆盖 52 条，弹幕面板整体缺失，口播 3 个按钮被裁 |
| 可访问性 | ❌ 无 `aria-live`/`role=alert`、错误不可感知、抽屉无焦点管理 |
| 性能 | ⚠ JS 194.8KB / CSS 250.5KB，其中约 240KB 是从未被使用的 Quasar CSS |
| 测试 | ⚠ 24 例全绿，但恰好绕开了会崩的入口、WS 状态机与全部业务面板 |

---

## 1. 实际执行的验证（原始记录）

命令均在 `f:\myproject\teamwork\digital-human\web` 下执行。

| 命令 | 结果 |
|---|---|
| `npm ci` | 成功，`added 500 packages`；`20 vulnerabilities (14 moderate, 4 high, 2 critical)` |
| `npm run typecheck`（`vue-tsc --noEmit`） | 退出码 0，无输出 |
| `npm test`（`vitest run`） | `Test Files 6 passed (6)` / `Tests 24 passed (24)`，1.01s |
| `npm run build`（`quasar build`） | 成功。JS `194.79 KB`（gzip `68.68 KB`）、CSS `250.52 KB`（gzip `41.95 KB`）、`index.html 0.35 KB` |
| `npm run dev`（Quasar dev, 9000） | 页面**空白**，控制台报 Pinia 未装配 |
| `dist/spa` 静态服务（9011） | 页面**空白**，`#q-app` children = 0，报 `TypeError: Cannot read properties of undefined (reading '_s')` |
| 后端 `bin/dh.exe -conf configs/config.yaml` | 端口 8795 已有实例在跑（degraded：无 PG/Redis，fake live controller）；`/healthz` 返回 503，`/api/v1/live/status` 返回 `phase=live` |
| 面板 GET 路由抽查（16 条） | 全部 200，返回 fake 数据 |

> 说明：8795 上已有一个**先前启动**的后端实例（我未启动第二个成功实例，`-conf` 启动时报端口占用）。该实例用于验证前端契约，不影响结论。

---

## 2. P0（阻断交付，必须修）

### P0-1 应用白屏：Pinia 从未装配

| 项 | 内容 |
|---|---|
| 位置 | `web/quasar.config.js:7`（`boot: []`）、`web/src/App.vue:1-7`（`<script setup>` 调 `useStageStore()`） |
| 问题 | 全工程没有 `createPinia()`，`src/boot/` 目录不存在，Quasar 生成的入口只装 `Quasar` + `router`。`App.vue` 的 `setup` 一执行就抛 `getActivePinia() was called but there was no active Pinia`。 |
| 证据 | dev：控制台 `[🍍]: "getActivePinia()" was called but there was no active Pinia ... at setup (src/App.vue:11:15)`，`document.body.innerText = ""`；prod（9011）：`#q-app` 子节点 0，`TypeError ... reading '_s'`（Pinia 内部字段）。两者截图均全空。 |
| 复现 | 1) `npm run dev` → 打开 `http://127.0.0.1:9000/`；2) 或 `npm run build` 后用任意静态服务打开 `dist/spa`。任意路径（`/`、`/settings`）都是空白。 |
| 修复建议 | 新增 `src/boot/pinia.ts`：`import { createPinia } from 'pinia'; export default ({ app }) => app.use(createPinia())`，并在 `quasar.config.js` 写 `boot: ['pinia']`（如同时要首帧防闪，再加 `boot: ['theme']`，见 P2-6）。修完后必须补一条"挂载 App.vue 后 `#q-app` 非空"的冒烟测试（见 P1-20）。 |

> 补充：`App.vue` 是 `<script setup>`（无 `lang="ts"`），`tsconfig.json` 又是 `checkJs:false`，所以**崩掉的那一行恰好不受类型检查覆盖**——这解释了"typecheck 全绿 + 单元测试全绿 + 构建成功"却无法运行。

### P0-2 直播画面（RTC）完全不可用，且失败被"合法化"

| 项 | 内容 |
|---|---|
| 位置 | `web/src/pages/StagePage.vue:43-48`（`creds` computed 恒 `null`）、`web/src/components/rtc/adapter.ts`（仅 hook）、`web/public/`（空目录）、`web/src/components/rtc/RtcPlayer.vue:44-55` |
| 问题 | 三重断链：(1) `creds` 先判 `s.rtc.tokenExpireAt` 再无条件 `return null`，是死分支；(2) 后端 `LiveStatus.rtc.tokenExpireAt` 实测为 `0`，且 proto 层**没有**签发/刷新 viewer token 的 RPC；(3) `public/vendor/` 不存在，方案 §6.6 要求的 `adapters/artc.ts`、`adapters/trtc.ts` 均未实现，`RtcPlayer` 只定义了 `RtcState = 'unconfigured'|'joining'|'joined'|'failed'` 四态类型。 |
| 证据 | 运行时 `/api/v1/live/status` 返回 `"rtc": {...,"tokenExpireAt":"0"}`；`public/` 为空；`adapter.ts` 只有 `createAdapter` 的空壳；`RtcPlayer` 在 `creds===null` 时 `setState('unconfigured')` 并 `emit('state')`——**UI 显示"RTC 凭证未下发"，看起来像"正常待配置"，实际是永久态**。属于"把失败静默成成功"的变体：没有降级提示、没有诊断入口、没有刷新动作。 |
| 复现 | 打开直播台 → 画面区永远只有"RTC 凭证未下发"；开播后 `phase=live` 也不变化。 |
| 修复建议 | 二选一并写明：<br>A. 后端补 `GetViewerToken`（返回 `channel/token/expire_at`），前端去掉死分支，`RtcPlayer` 在 `expire_at-60s` 前主动刷新；<br>B. 若本期不交付画面，必须把画面区改成显式"功能未交付（v1 不含画面）"的阻断态，并删除 `RtcPlayer`/`adapter` 死代码与 `rtc-state` 空播报，避免"看起来已实现"。 |

### P0-3 实时字幕链路根本不存在（后端不发布 transcript 事件）

| 项 | 内容 |
|---|---|
| 位置 | `web/src/stores/transcript.ts`（处理 `transcript.partial/final/say.text`）、`web/src/pages/StagePage.vue:104-122`（消费 `events.latest` + 一次性 `seed`） |
| 问题 | 后端事件总线只有 `live.* / song.* / refs.* / action.*`（`internal/biz/**` 里 `Publish` 的全部调用点），没有任何 `transcript.*` 或 `say.text`。前端 `applyEvent` 处理的三类事件永远不会到达；字幕只能靠 `live.status.transcripts` 在**首次** `seed` 一次，之后不再更新。 |
| 证据 | `grep '"transcript\.' / '"say\.'` 在 `internal/**` 零命中；`RightRail` 空态文案"暂无字幕（等待 transcript 事件）"在整个运行期不会变化；开播并在 ComposerBar 发言后，字幕区仍为空。 |
| 复现 | 开播 → 在底部输入"你好"发送 → 右侧"字幕"tab 与画面字幕条均无新增行。 |
| 修复建议 | 由后端在 TTS 播放/LLM 流式时发布 `transcript.partial`（含 `utterance_id`、`text`）与 `transcript.final`；前端改为**按 `utterance_id` 归并**（当前是按"最后一行是否 final"的位置归并，交错发言会串行覆盖，见 P2-9）。若本期不做，必须把字幕区标注为"静态回放字幕"，不得自称实时（R-UI-26）。 |

### P0-4 epoch 语义不一致 → 事件缓冲被反复清空，去重表失效

| 项 | 内容 |
|---|---|
| 位置 | `web/src/stores/events-core.ts:37-42`；后端 `internal/biz/live/live.go:153`（`epoch = time.Now().UnixMilli()`）、`internal/biz/song/song.go:171`、`internal/biz/refs/refs.go:181`、`internal/biz/action/action.go:297`（三者均 `Publish(..., 0, ...)`） |
| 问题 | 前端把"`epoch` 变化"当作"换场次，清空 UI"。而后端只有 `live.*` 事件带真实 epoch，`song.*/refs.*/action.*` 一律带 `0`，`live.crash_recovered` 也带 `0`。于是**同一场直播里，只要播放一首歌/切一次参考图/下发一条动作，epoch 就在 `X → 0 → X` 之间来回跳**，每次跳变都会 `events = []` 并把整个 `seen` 去重表清空。 |
| 证据 | `mergeEvent` 第 38-42 行：`const epochChanged = buffer.lastSeq > 0 && incoming.epoch !== buffer.epoch; if (epochChanged) { events = [] ... delete seen[key] }`；后端点位如上。实测 `/api/v1/events` 中 `live.*` 的 `epoch` 与 `song.*` 的 `epoch` 不同源。 |
| 复现 | 开播 → 在"歌曲"面板点播放 → 右侧"事件"列表被清空重建；再发一条动作 → 再清一次。高频操作下事件列表永远只剩最近一段。 |
| 修复建议 | 前端不要用 `epoch` 差值做清空判据：改成"`session_id` 变化才清空"，`epoch` 仅用于喂流/歌词这类子域的版本比较；同时后端把 song/refs/action 事件补上真实 `session_id` + `epoch`（当前为 `""` 和 `0`）。二者必须同时改，否则仍会误清。 |

### P0-5 WS 重连竞态：旧连接的 `onclose` 清空新连接引用 → socket 泄漏 + 重复连接

| 项 | 内容 |
|---|---|
| 位置 | `web/src/stores/events.ts:40-70`（`connect` 的 guard 在 `await` 之前；`onclose` 里 `socket = null` 无条件）、`:147`、`StagePage.vue:172-173` |
| 问题 | `connect()` 先判 `socket.readyState`，再 `await createWsTicket()`，最后才赋值 `socket`。两个并发 `connect()` 都会通过 guard 并各建一条 WS，后者覆盖前者引用，前者泄漏且仍在收消息。更常见的是"应用 token"路径：`disconnect()`（`manuallyClosed=true`、`socket.close()`、`socket=null`）紧接着 `connect()` 把 `manuallyClosed` 复位为 false、并把 `socket` 指向新 WS；旧 WS 的 `onclose` **异步**到达后又执行 `socket = null`，把新 WS 的引用抹掉。结果：新 WS 无法被 `disconnect()` 关闭（卸载后仍在收），且 `onclose` 分支会 `startPolling + scheduleReconnect`，1s 后 guard 因 `socket===null` 通过，再开第三条连接。 |
| 证据 | 代码路径如上；`onclose` 无 `this` 代际校验、无 `socket === ws` 比对。运行期可观察到点击"应用"后 `/api/v1/ws-ticket` 连续出现 2-3 次、`/ws/v1/events` 出现多条 concurrent 连接（`browser_network_requests` 可见）。 |
| 复现 | 打开直播台 → 顶部输入任意 token → 点"应用"（或切到别的标签页再切回使 `visibilitychange` 触发 `connect`）。观察 Network：WS 请求成对出现；Event 列表后续出现重复 `#seq`（去重表已被 P0-4 清空时尤其明显）。 |
| 修复建议 | `connect()` 加 in-flight 互斥（`connecting` flag 或 `Promise` 复用）；创建后校验 `if (socket !== ws) { ws.close(); return }`；`onclose` 里加代际判断 `if (socket === ws) socket = null`；`disconnect()` 先取局部引用 `const cur = socket; socket = null; cur?.close()`，避免闭包误伤。 |

### P0-6 无响应式断点：窄屏/低分辨率直接崩版

| 项 | 内容 |
|---|---|
| 位置 | `web/src/pages/StagePage.vue:102`（`gridTemplateColumns: minmax(0,1fr) ${prefs.rightW}px`）、`:337`（`min-height:100vh`）、`StatsBar.vue:23-31`（6 等分 flex）、`PanelShell.vue:55`（绝对定位不夹取）、`SettingsPage.vue:108`（`minmax(0,1fr) 280px`） |
| 问题 | 全工程**没有任何 `@media` 断点**（R-UI-43 要求 <1280 收侧栏、<960 单列、<720 只留卡片、最小 320px 可读）。右侧栏固定 360px（`prefs.rightW` 默认 360，可在设置里拨到 560）与 1fr 并排；在 375px 视口下 `1fr` 被压到 0，Rail 直接横向溢出。StatsBar 6 个等分单元每格 ~60px，`积分/Token/钻石/礼物/时长/音频延迟` 标签与数值被省略号截断。浮动面板 `left/top` 直接取持久化的 x/y，**没有容器边界夹取**，小屏上会停留在屏幕外且无"重置位置"。`min-height:100vh` 嵌在 `App.dh-main`（`overflow:auto`，且顶部还有 57px 头部）里，必然产生外层滚动条 + 页面双滚动。 |
| 证据 | CSS 事实：deck 无 media query；`rightW` 默认 360（`uiPrefs.ts`）；`StatsBar` 无 `overflow-x`/wrap；`PanelShell` 仅 `Math.max(0, …)`。`App.vue:61-66` 的 `.dh-main` 已有 `padding:16px 20px; overflow:auto`，与 `StagePage .page{padding:12px 16px; min-height:100vh}` 叠加。 |
| 复现 | 仅需把浏览器窗口拉到 1024×768 或 420×800（因 P0-1 白屏，当前需修复 Pinia 后复现；静态 CSS 推导同样成立：303px 内容区 + 360px 固定栏 = 溢出）。 |
| 修复建议 | 1) 按 R-UI-43 加三档断点，`<1280` 时 `rightOpen` 强制 false 且不渲染 Rail；`<960` deck 改单列（Rail 变抽屉）；`<720` 只留画面 + 输入 + 统计横向滚动。2) `rightW` clamp 到 `容器宽*0.35`。3) `PanelShell` 拖动时按容器 `clientWidth/clientHeight - panelW/H` clamp，并给"重置面板位置"。4) 去掉 `StagePage` 的 `min-height:100vh`（外层已有滚动容器），或让 `App.dh-main` 不滚动、由页面自身滚动，二选一。 |

---

## 3. P1（严重，交付前必修）

| # | 位置 | 问题 | 证据 | 复现 | 修复建议 |
|---|---|---|---|---|---|
| P1-1 | `web/src/pages/StagePage.vue:104-111`、`web/src/stores/events.ts:96-108` | **批量回放只应用最后一条事件**：`accept()` 每收一条就 `this.latest = e`，Vue 在同一 tick 内合并 watcher，只以最后一条触发 `live.applyEvent / transcript.applyEvent`。断线补数与 `pollOnce` 都是循环 `accept`，中间事件对 UI 全部丢失。 | watcher 只 watch `events.latest` 单值；`resync()` 内 `for (const e of events) this.accept(e)`。 | 断网 10s 后恢复 → 字幕/阶段只体现最后一条，中间的 `live.started`/`song.ended` 等无痕。 | 改为 watch `events.events` 的**新增片段**（或让 store 暴露 `pending: WireEvent[]` 队列，页面按序消费）；或让 store 内部按序直接派发给 `live/transcript`，页面只读结果。 |
| P1-2 | `web/src/stores/events-core.ts:33-45`、`src/stores/__tests__/events-core.test.ts`（"已裁剪 id 不再重复 / 乱序 replay" 用例） | **旧事件回放乱序追加，`latest` 回退**：`mergeEvent` 不校验 `incoming.seq < lastSeq`，直接把旧事件 `push` 到数组尾部；测试把这个行为写成了期望（replay 后 buffer 变成 `[4,5,1]`）。旧 `live.start_requested` 重放会把 `latest` 打成旧事件，`live.applyEvent` 可能把 `phase` 从 live 回退。 | 见测试断言 `replay.buffer.events` 含 seq 1 且位于末尾；`latest = e` 无 seq 单调判断。 | 手动 `resync`（断线补齐）或 WS backlog 重连后，阶段徽标/统计短暂回退。 | `mergeEvent` 增加 `if (incoming.seq <= buffer.lastSeq) return {duplicate:true}`（或按 seq 插入并拒绝 `latest` 回退）；同步修正该单测，把"乱序接受"改为"乱序拒绝"。 |
| P1-3 | `web/src/pages/StagePage.vue:133-140` | **状态轮询 5s 无条件、无可见性暂停、无版本守卫**（违 R-UI-34/35）：即使 WS 健康也每 5s 拉一次 `/api/v1/live/status`；页面隐藏后仍轮询；慢响应可覆盖新响应（开播后 WS 已把 phase 置 live，一个"开播前发出"的 status 响应回来又把它写成 idle）。 | `statusTimer = setInterval(() => void live.load().then(loadCost), 5000)`；`live.load` 无 `loadVersion` 判定（模块里声明了 `loadVersion` 但未使用）。 | 开播瞬间切后台再切回，或弱网下连续观察 5s 周期，阶段徽标会闪回 idle/starting。 | 1) 仅在 `events.mode !== 'ws' || events.degraded` 时轮询；2) 用 `createVisibilityPolling`（新建 `src/utils/polling.ts`，R-UI-34）；3) `live.load` 内用 `isCurrent(token)` 守卫（R-UI-35），丢弃过期响应。 |
| P1-4 | `web/src/components/stage/VideoStage.vue`（开始/停止按钮）、`web/src/stores/live.ts:61-74` | **连点开播无防抖、幂等键每次随机**：开始按钮只按 `phase` 置灰，不含 `busy`；`startLive()` 每次调用生成新的 `idempotencyKey`。双击 → 两次 start 请求 → 两条不同幂等键 → 后端按"重复开播先 stop 再重开"执行，出现"刚开就停再开"的抖动。 | `busy` 只在 store 内、按钮未绑定；`idem()` 每次 `randomUUID`。 | 快速双击"开始直播"（或触屏连点）。 | 按钮 `:disabled="phase!=='idle' && phase!=='ended' \|\| busy"` 且显示 loading；store 层加 in-flight 互斥（`pending: Set<string>`）；同一用户动作复用同一 `idempotencyKey`（如以 `sessionId+action+时间片(2s)` 生成）。 |
| P1-5 | `KouboPanel.vue:120-127`、`VoicePanel.vue:120-127`、`SongsPanel.vue`（删除）、`CommandPanel.vue:166`、`RightRail.vue:37`、`StagePage.vue:246-247`、`:311` | **破坏性操作零二次确认**（违 R-UI-31）：删除 TTS 文件/歌曲/动作库/动作（右键即删）、清空字幕（同时清服务端）、停止全部动作、清空动作，全部一击生效；`CommandPanel` 的动作删除只靠 `@contextmenu.prevent`，键盘用户无等价路径（违 R-UI-71）。 | `grep 'confirm('` 全工程 0 命中；`@contextmenu.prevent="removeAction(a.id)"`。 | 任意面板点删除/清空，无法撤销。 | 统一封装 `NtConfirmDialog`（文案直写后果，如"删除 12 段口播音频"），对"清空字幕/清空动作"额外提示影响范围；动作删除改为显式 ⋯ 菜单项（含焦点回退）。 |
| P1-6 | `SettingsDrawer.vue`、`SettingsPage.vue:43` | **无未保存离开提示**（违 R-UI-49）：抽屉可编辑 12 组设置并显示脏点，但点"关闭"、按 Esc、跳转 `/settings`、刷新页面都不提示，改动直接丢失；`SettingsPage` 挂载即 `settings.load()` 会把抽屉里未保存的修改覆盖。 | 抽屉只有 `dirtyGroupIds` 徽标，无 `beforeunload`/`onBeforeRouteLeave`/关闭前拦截。 | 改 2 个字段 → 点右上 ×（或 Esc）→ 重开抽屉，改动没了。 | 关闭/路由离开前检测 `dirtyGroupIds.length`，弹确认（"有 3 组未保存，仍要离开？"）；`SettingsPage` 首次挂载若已有脏数据则跳过 `load()`。 |
| P1-7 | `web/src/components/panels/RefsPanel.vue:37-59` | **错误被自己的刷新吞掉**：`apply()` 在 `catch` 里先 `error.value = …`，紧接着 `await load()`，而 `load()` 第一行就是 `error.value = ''`，用户最终看到"什么都没有"。属"失败静默成成功"。 | L45-55。 | 让后端参考图接口返回 502（或断网点"切换"）→ 无任何错误提示。 | `catch` 中不要调用会清错误的 `load()`；改为 `const msg = ...; error.value = msg; void refreshWithoutClearingError()`。 |
| P1-8 | `web/src/components/panels/KouboPanel.vue`（分段文本 `@change`） | **乐观写入无回执对齐/无回滚**（违 R-UI-32）：改完分段文本失焦即提交，失败时 textarea 仍显示新值，而服务端保持旧值；下次 `load` 前的窗口里是假态。 | `:value="seg.text"` + `@change="edit(seg,$event)"`，无失败回滚。 | 断网点编辑 → 看起来已保存。 | 失败时把该段文本回写为响应体中的值（或 `await load()` 后强制重渲染），并在面板内行内提示。 |
| P1-9 | `web/src/components/settings/schema.ts`、`SettingsDrawer.vue` | **配置项"假生效"**：`captions` 组 8 个字段，前端实际只消费 `enabled/font_size/max_lines`（`StagePage.vue:55-62`），`offset_px/fade/shadow/privacy_mask/show_events` 改了没有任何效果（画面字幕 `bottom` 硬编码 12px）；`persona` 组无"应用/刷新"；`llm` 组写着有"清空记忆"，但 `live.resetLlmMemory` **全工程零调用**；`action_library`/`assets`/`member`/`vidu_key` 组多为只读占位，且 `action_library` 注释"后端未实现"与事实不符（`/api/v1/actions/*` 已可用）。 | schema 字段 vs 消费点对照；`grep resetLlmMemory` 仅定义处。 | 改 `offset_px=200` → 保存 → 画面上字幕位置不变。 | 二选一：把字段接到渲染（`bottom: offset_px`、`fade`→CSS 遮罩、`show_events`→事件角标）并在组内加"清空记忆/应用人设"按钮；或从 schema 删除未实现项，只保留能生效的字段（避免"假开关"）。 |
| P1-10 | `web/src/components/stage/PanelDock.vue`（6 个面板） | **弹幕面板整体缺失**：后端有 `GetState/ListPending/Confirm/Dismiss/SetMode` 5 条弹幕接口（`danmaku.proto`），前端只封装了 `getDanmakuState`（且无 UI），待审弹幕无法确认/忽略、无法切自动/手动。方案 §6.2 明确要求弹幕面板。 | `PanelDock` 仅 voice/command/koubo/refs/songs/archive；`panels.ts` 无 `listPending/confirm/dismiss/setMode`。 | 舞台上看不到任何弹幕待审入口。 | 新增 `DanmakuPanel`：模式切换、待审列表（确认/忽略/批量）、Top 榜（复用 `/danmaku/state`），接完 4 条写接口。 |
| P1-11 | `web/src/services/api/panels.ts:430/435/459` | **三个口播按钮被裁掉且无说明**：`mergeKouboSegments`、`splitKouboSegment`、`kouboActionsAll` 已封装但**从未被调用**，对应后端 3 条路由在 UI 上不可达。这属于"刻意裁掉"还是"漏做"没有被记录，评审无法判断。 | 仅定义处出现；`KouboPanel` 只有 分段/TTS/选中/动作/播放/停止。 | 口播面板找不到"合并""按位置拆分""全部套动作"。 | 若刻意裁掉：在面板加一行"v1 不提供合并/拆分/批量动作（接口保留）"，并写入方案偏差清单；若漏做：补齐按钮。 |
| P1-12 | `web/src/stores/stage.js`（整文件）、`web/src/App.vue:2,6-7` | **第二套无鉴权 store 并存**：`stage.js` 是 JS（非 TS）store，用裸 `fetch` 直连 `/healthz` 与 `/api/v1/settings`，不走 `apiRequest`、不带 Bearer、不过 `classifyApiError`、不参与 `endpoint.ts`（违 R-UI-61）。它导致：每次进页面**重复拉一次 settings**；lan 模式下该请求必然 401，顶部"降级/阶段"徽标数据不可信；`startLive/stopLive` 是死代码。 | `jsonFetch` 自定义；`Promise.all([jsonFetch('/healthz'), jsonFetch('/api/v1/settings')])`；`startLive` 无调用方。 | lan 模式下打开页面，`/api/v1/settings` 出现两条（一条 401、一条 200）。 | 删除 `stage.js` 与 `App.vue` 对它的依赖，顶部状态改由 `live`/`events` store 提供（`events.mode`、`live.phase`、`events.fresh`）。 |
| P1-13 | `web/src/App.vue:13-21`、`:64`；`StagePage.vue:179-183`、`:337` | **UI 残留 M0 文案 + 双头部 + 双内边距**：顶部固定写死"M0 骨架 · 外部依赖未接入"，与 `StagePage` 的"M3 前端 · 面板已接真实接口"直接矛盾；两个品牌头（"数字人直播 S2·控制台" / "小雅 · 数字人直播台"）叠加；`App.dh-main` 与 `.page` 各有一层 padding。 | 文案与样式如上。 | 任何页面首屏即可见。 | 删除 App 级品牌头与 M0 文案（或改为"运行环境：本机/局域网"真实信息）；只保留一层页面内边距。 |
| P1-14 | `web/src/services/api/client.ts` | **无超时/无 401 引导**（违 R-UI-36 的"阻断型错误给引导卡"）：`apiRequest` 不建 `AbortController`（`signal` 参数从未被传入），后端 hang 住时 `live.busy` 永久为 true，Composer 与开播按钮永久不可用；401 只映射成文案，不清 token、不提示重新填 Bearer、不跳转登录。 | `client.ts` 无 timeout；`auth` 只存内存。 | 后端进程被 `Stop-Process` 冻结时点开播 → 按钮永久转圈（需刷新）。 | `apiRequest` 默认 10s timeout（`AbortSignal.timeout`），超时映射为 `network`；401 时置 `auth.error` 并在舞台顶部展示"凭证失效 → 重新填写"引导条。 |
| P1-15 | 全站 | **无全局快捷键/命令面板**（违 R-UI-37）：`uiPrefs.kbCmdOpen` 有开关但没有任何命令面板，F9/F10/`/` 指令均未实现；`ComposerBar` 也没有"`/` 开头走 SendCommand"的分流（`live.command` 定义了但 UI 不可达）。 | `grep keydown` 仅 NtDrawer 的 Esc；无 `src/utils/hotkeys.ts`。 | 试按 F9/F10、输入 `/动作 xxx` 回车 → 当普通发言发送。 | 新建 `utils/hotkeys.ts` 统一登记（F9 开播/F10 打开下播确认，显式确认后停止/`/` 聚焦输入/`Ctrl+K` 命令面板），Composer 对 `/` 前缀改走 `live.command` 并在输入框内提示。 |
| P1-16 | 全站样式 | **无 `aria-live` / `role="alert"` / `aria-current` / `aria-selected` / `aria-pressed`**：所有错误提示都是普通 `<p class="error">`，读屏用户收不到；`RightRail` 两个 tab、`PanelDock` 面板开关都是纯 `<button>` + class，无选中语义。 | 全工程 grep 上述属性 0 命中。 | 用 NVDA/VoiceOver 操作：发送失败无播报；tab 切换无状态播报。 | 错误容器统一 `role="alert"`；tab 用 `role="tablist"/"tab"` + `aria-selected`；面板开关加 `aria-pressed`；字幕区加 `aria-live="polite"`。 |
| P1-17 | `web/src/components/settings/SettingsDrawer.vue`（720px）、`NtDrawer.vue:33` | **抽屉宽度越级 + 模态语义错误**：宽度 720px 不在 R-UI-45 的 360/480/640 阶梯；`SettingsPage` 把抽屉当内联区块使用却仍带 `role="dialog" aria-modal="true"`（页面里并没有模态行为，`aria-modal` 会误导读屏把其余内容当成不可用）。 | 样式 `width:720`；`aria-modal="true"` 无条件。 | 打开全部设置页 → 读屏只能感知弹窗，实际是同页内嵌。 | 宽度改 640（或 480）；`NtDrawer` 增加 `mode: 'modal' | 'inline'`，仅 modal 才输出 `role/aria-modal`，并补焦点捕获/回焦/滚动锁。 |
| P1-18 | `web` 构建产物 | **约 240KB 是从未使用的 Quasar CSS**：`framework.plugins = []`、零 Quasar 组件，但构建 CSS 里仍有 3517 个 `.q-*` 选择器（总计 250.52KB / gzip 41.95KB）。R-UI-12 的"不引入 UI 库"只在视觉层成立，体积层不成立。 | `grep -c '\.q-' dist/spa/assets/*.css` → 3517；`package.json` 依赖 `quasar`。 | `ls dist/spa/assets/*.css` 看体积。 | 若坚持"Quasar 仅作工程壳"，评估 `framework: false`（保留 router/devServer）或改用纯 Vite；否则接受并记录偏差。 |
| P1-19 | `web/src/stores/*` | **6 个本地偏好键是死键**：`stats`、`statsPos`、`rndEvery`、`rndBias`、`cmdEdits`、`songPath` 写入 localStorage 但 UI 从不消费（`StatsBar` 的 items 由 `StagePage` 硬编码，顺序固定）；`cmdFx`/`kbCmdOpen` 只在设置页出现，没有对应功能。方案里"9 键已接"与事实不符。 | 全工程 grep `prefs.stats|statsPos|rndEvery|rndBias|cmdEdits|songPath` 仅定义处。 | 设置页可切换这些项，但界面无任何变化。 | 要么把键接到真实 UI（统计项显隐/排序、指令动效、歌词路径），要么从设置页移除，避免"看起来能配、实际无效"。 |
| P1-20 | `web/src/**/__tests__`（6 文件/24 例） | **测试恰好绕开所有会崩的路径**：`vitest.config.ts` 注释明确"不启动 Quasar 壳"，因此没有任何用例挂载 `App.vue`；`events.ts`（WS/poll/重连/resync）、`live.ts`（busy/竞态/事件应用）、`transcript.ts`、`RtcPlayer`、`VideoStage`、`StagePage`、6 个业务面板、`client.ts`（鉴权头/401）全部 0 覆盖；`events-core.test.ts` 还把"乱序回放"断言成正确。 | 见 §8 覆盖矩阵。 | `npm test` 全绿，但应用打不开。 | 加最小冒烟：`mount(App)` + 断言 `#q-app` 非空（此例即可抓出 P0-1）；为 `events` store 建 fake WS/poll 状态机测试；为 `live.start` 双击并发写用例（断言只发一次请求）。 |

---

## 4. P2（一般问题，建议同批清理）

| # | 位置 | 问题 | 修复建议 |
|---|---|---|---|
| P2-1 | `VoicePanel.vue:216`、`SongsPanel.vue:161`、`ArchivePanel.vue:204`、`CommandPanel.vue:229`、`RefsPanel.vue:119`、`KouboPanel.vue:220` | 裸色值 `var(--nt-danger, #d9534f)`（`--nt-danger` 在 tokens 里确实存在，fallback 是死字面量） | 改成 `var(--nt-danger)`；若要保留 fallback，必须挂 `theme/ui-literals-allowlist.json`（当前该文件不存在，违 R-UI-02） |
| P2-2 | `NtStatusPill.vue:36,40,44`、`NtButton.vue:57-58` | 5 处硬编码 `rgba(...)`（写死暗色主题 RGB）：浅色主题与 `data-colorVision='safe'` 下边框色不跟随令牌 | 改为 `color-mix(in srgb, var(--nt-success) 40%, transparent)` 或新增 `--nt-success-border` 等令牌 |
| P2-3 | `ArchivePanel.vue`（`{{ s.phase }}`）、`App.vue:18`（`label = store.phase`）、`RightRail.vue:55`（`{{ e.type }}`） | 裸枚举直渲（违 R-UI-22）：用户看到 `live`/`ended`/`song.ended` 这类码 | 建 `STATUS_MAP` + 文案；事件类型映射为中文（"歌曲结束"/"参考图切换"…），原始码放 `title` |
| P2-4 | `StagePage.vue:98` | 时长 `value: ${live.durationS}s` → 空闲时恒显示 `0s`（伪 0，违 R-UI-20） | 未开播显 `—` + 原因"未开播" |
| P2-5 | `StatsBar.vue:45-52` | 数字等宽只用 `--nt-font-mono`，缺 `font-variant-numeric: tabular-nums`（违 R-UI-41） | 补 `font-variant-numeric: tabular-nums; font-feature-settings:'tnum'` |
| P2-6 | `web/index.html`（全文 10 行） | 无 R-UI-60 首帧防闪内联脚本：`data-theme/density/colorVision/visualQuality` 要等 `uiPrefs.load()`（StagePage `onMounted`）才写入；浅色主题用户会先看到深色闪一下；`html` 无底色兜底 | 在 `index.html` 内联同步脚本读同一份 `nt.settings.v1` 写 `documentElement.dataset.*`，并给 `html{background:#0b1b2b}` 兜底（与 tokens 同步） |
| P2-7 | `web/index.html` | 无 CSP `<meta>`，方案 §6.9 要求 `script-src 'self'`（若由 nginx 下发需在 README 注明） | 加 meta CSP 或在部署文档明确由网关下发 |
| P2-8 | `NtStatusPill.vue:46-55` | 脉冲动画只响应 `prefers-reduced-motion`，不响应应用内 `data-motion='reduced'` | 加 `:root[data-motion='reduced'] .nt-pill__dot { animation: none }`（或全局用 `[data-motion]` 禁动画） |
| P2-9 | `transcript.ts`（partial 归并） | 按"最后一行是否 final"做位置归并，不按 `utterance_id`；两段交错发言会互相覆盖 | payload 带 `utterance_id`，按 id 查找更新；找不到则新建 |
| P2-10 | `NtDrawer.vue` | 无焦点捕获、无回焦、无滚动锁；Esc 只关自身 | 补 focus trap + 记录 `document.activeElement` 关闭后回焦 |
| P2-11 | `SettingsGroupForm.vue` | 数字输入无校验：清空即 `Number('')=0`，`min/max` 只作用于原生控件，保存时不做钳制/不禁止提交（违 R-UI-30） | 失焦即校验，越界行内提示并禁用"保存"；改为 `v-model.number` + `clamp` |
| P2-12 | `SettingsPage.vue` | 直接进入 `/settings` 时未调用 `prefs.load()`：主题/密度可能显示为默认值且 `data-*` 未应用（`body__drawer` 还用了 `calc(100vh - 110px)` 魔数） | 在 `SettingsPage` 挂载时 `prefs.load()`；高度改用 flex |
| P2-13 | `ArchivePanel.vue:100,152`、`KouboPanel.vue:187` | 原生 `<select>` 与 `NtSelect` 混用，且控件高度三档不一（42/34/28） | 统一改用 `NtSelect`；沉淀 `--nt-control-h` |
| P2-14 | `VoicePanel.vue:67-82`、`KouboPanel.vue:84-86` | `URL.createObjectURL` 永不 `revoke`；试听音频在面板关闭后继续播放（无 `onBeforeUnmount` 停止） | `revokeObjectURL` + 卸载时 `audio.pause()` |
| P2-15 | `events.ts:15`（`fresh` getter）、`RightRail.vue:15` | 新鲜度文案不随时间刷新：没有 1s tick，只有来新事件才重算 → 长时间显示"刚刚"（违 R-UI-26 的"证据"初衷） | 加 1s 计时器（页面隐藏时停），或改用 `<time :datetime>` + CSS 计时 |
| P2-16 | `ArchivePanel.vue`（`listSessions(20,0,phase)`） | 场次固定 20 条、无分页/无"加载更多"；`duration_s` 为 null 时会渲染 `NaN` | 加分页；数值统一走格式化函数（null → `—`） |
| P2-17 | `events-core.ts:47-51` | 去重窗口=300 且淘汰即 `delete seen` → 长场次下重放事件可能重复计入 | 去重表改为"最近 N 个 id 的环形队列"或按 `seq` 水位判重（`seq <= lastSeq` 直接丢弃，见 P1-2） |
| P2-18 | `events-core.ts:26-28` | 畸形事件（无 `seq`）被静默当重复丢弃，无计数/无日志 | 返回 `malformed` 并累加 `droppedMalformed`，在调试区暴露 |
| P2-19 | `panel` 拖拽（`PanelShell.vue:28-34`） | 只 `Math.max(0,…)`，可拖出右/下边界；x/y 持久化后换小屏无"复位" | 按容器 clamp + 复位按钮 |
| P2-20 | `web/package.json:3`、`App.vue:21` | 版本号仍是 `0.1.0-m0`，UI 仍写"M0 骨架"；`npm ci` 报 20 个漏洞（2 critical） | 版本改 `0.1.0-m3`；评估升级 `quasar/app-vite` 与 `jsdom`（critical 条目） |
| P2-21 | `services/api/panels.ts`（`apiBlob/apiUpload/kouboAudio/exportSession`） | 4 套自定义 `fetch` 复制鉴权/错误处理，绕开 `apiRequest`（R-UI-61"HTTP 只走一处"） | 收敛为 `apiRequest` 的 `blob/form` 模式 |

---

## 5. 规范性：R-UI 逐条对照（实际核对 NovaTrader README + tokens.css）

> 核对方式：先读 `NovaTrader/web/src/theme/tokens.css`（与本站 `tokens.css` **字节完全一致**，SHA256 相同，属同源复用，合规），再逐条对照代码。

| 规范条目 | 代码位置 | 是否满足 | 偏差说明 |
|---|---|---|---|
| R-UI-02 颜色只写 `var(--nt-*)`，裸色入 allowlist | 见 P2-1/P2-2 共 10 处 | ❌ | 5 处 `#d9534f` fallback + 5 处 `rgba()`；`theme/ui-literals-allowlist.json` 不存在，也没有 `ui-literals.test.ts` |
| R-UI-03 外部色反转/不照搬 hex | `theme/tokens.css` | ✅ | 与 NovaTrader 同源，未混入外部调色板 |
| R-UI-10 `ui/` 禁 import store/router | `components/ui/*` | ✅ | 6 个 Nt* 组件均只吃 props/emit |
| R-UI-12 不引入 UI 组件库 | `quasar.config.js:24-27` | ⚠ | 零 Quasar 组件（视觉合规），但 Quasar 全量 CSS 仍打包 ~240KB（体积不合规） |
| R-UI-20 缺位显「—」+ 原因 | `StagePage.vue:73-100` | ⚠ | 积分/Token/钻石/礼物合规；`duration` 显 `0s` 伪 0 |
| R-UI-22 枚举不直渲 | `ArchivePanel/App/RightRail` | ❌ | 场次 phase、顶部 pill、事件 type 均裸渲 |
| R-UI-24 「—」与「***」分槽 | `NtSecretText.vue`、Vidu Key 组 | ✅ | 遮罩用 `***尾8`，缺失用 `—`，未混用 |
| R-UI-26 在线/实时必配证据 | `VideoStage` 的"WS 在线"、`RightRail` 的"Xs 前" | ❌ | 画面区"WS 在线"无时间戳；新鲜度不随时间刷新（P2-15）；字幕自称实时但链路不存在（P0-3） |
| R-UI-30 条件不满足禁用提交+行内说明 | 设置保存、开播按钮 | ⚠ | 数字越界不拦（P2-11）；未加载设置也能开播（会以空 persona/avatar 上报） |
| R-UI-31 破坏性操作双确认 | 6 处删除/清空 | ❌ | 全站 `confirm` 0 命中（P1-5） |
| R-UI-32 乐观收口回执对齐 | `KouboPanel` 分段编辑 | ❌ | 失败不回滚（P1-8） |
| R-UI-34 轮询统一 `createVisibilityPolling` | `StagePage.vue:138` | ❌ | 无 `utils/polling.ts`，5s 定时器不看可见性 |
| R-UI-35 版本守卫 | `live.ts`（`loadVersion` 定义未用） | ❌ | 慢响应可盖新响应（P1-3） |
| R-UI-36 错误归一 + 引导卡 | `client.ts` 的 `classifyApiError` | ⚠ | 归类有，无 401 引导/无超时（P1-14） |
| R-UI-37 快捷键全局登记 | 无 | ❌ | 无 `utils/hotkeys.ts`、无 F9/F10/命令面板（P1-15） |
| R-UI-40 间距刻度 4/8/12/16/20/24 | 各组件 | ❌ | 出现 1/2/5/6/7/10/14px（如 `StatsBar gap:1px`、`ArchivePanel 5px/7px`、`Rail 4px`） |
| R-UI-41 数字等宽 + tabular-nums | `StatsBar.45` | ⚠ | 有 mono，无 tabular-nums |
| R-UI-43 三级断点 | 全站 | ❌ | 无任何 `@media`（P0-6） |
| R-UI-45 抽屉 360/480/640 | `SettingsDrawer` | ❌ | 720px（P1-17） |
| R-UI-46 面板头三层 | `PanelShell.vue:57-66` | ⚠ | 只有标题 + 操作，无状态徽注/新鲜度 |
| R-UI-49 未保存对比 baseline、操作条可见、未知字段不丢 | `settings.ts`、`SettingsDrawer` | ⚠ | baseline 对比✅、`mergeWithSchema` 保留未知字段✅；无离开提示❌、未知**分组**会被丢弃 |
| R-UI-60 首帧防白闪内联脚本 | `index.html` | ❌ | 无内联脚本、无 html 底色兜底（P2-6) |
| R-UI-61 endpoint 集中、HTTP 只走一处 | `services/api/endpoint.ts` ✅ / `stage.js` ❌ | ❌ | `stage.js` 裸 fetch（P1-12）；`panels.ts` 另有 4 套 fetch |
| R-UI-71 原生语义/关闭/回焦/拖动等价路径 | `NtSelect`/`NtToggle`/`NtDrawer` | ⚠ | 原生 select/toggle 语义✅、Esc✅；无回焦❌、右键删除无键盘等价❌ |

**Quasar Material 皮肤结论**：皮肤本身**已正确禁用**——`framework.plugins: []`、`extras: []`、无 Quasar 组件、无 `q-*` 组件类，视觉全部由 `Nt*` + tokens 承担；不存在"Quasar 组件与自建组件混用"的视觉冲突。**但**构建仍注入 Quasar 全量基础 CSS（3517 个 `.q-*` 选择器），`app.css` 里甚至专门写了 `.q-page{min-height:0}` 去中和它——说明这套 CSS 是纯负担。

---

## 6. 架构审查

### 6.1 分层

```
pages/        StagePage, SettingsPage
features/     （不存在）
components/   stage/ (VideoStage, RightRail, ComposerBar, StatsBar, PanelDock+PanelShell)
              panels/ (6 个业务面板)
              settings/ (schema + drawer + form)
              rtc/ (RtcPlayer + adapter + types)
              ui/ (Nt* 6 个)
services/api/ endpoint, client, live, settings, panels, ws-ticket, types
stores/       live, events(+events-core), transcript, settings, uiPrefs, auth, stage.js(遗留)
```

- **`pages → features → services` 的 `features` 层在方案里存在、在代码里不存在**：业务逻辑直接落在 `pages/StagePage.vue`（组装开播 payload、拼 stats、订阅事件）与 `components/panels/*`（各自 `onMounted(load)` + 直接调 `services`）。方案 §6.4 规定"组件不 fetch、store 管状态"，实际是"面板组件 = 自己的 store + 自己的 fetch"。
- **store 划分**：方案 13 个，实际 7 个（live/events/transcript/settings/uiPrefs/auth + 遗留 stage）。缺失 danmaku/action/koubo/song/refs/choreo/assets/patron/replay 等域 store，导致跨面板共享状态（歌唱中、动作队列、弹幕模式）只能各自轮询、且互不同步。`SongsPanel` 播放后 `ArchivePanel`/`StagePage` 不知道，反之亦然。
- **循环依赖**：无（stores 互不 import）；**但存在"跨层直接改 store 状态"**：`StagePage.vue:230` `@select` 直接 `settings.setField(...)`（这算 action，合规），`StagePage.vue:311` 在模板里执行两条副作用 `live.clearTranscripts(); transcript.clear()`（模板内写副作用，难测）。
- **类型与 proto 一致性**：`int64` 归一化**彻底**——`normalizeRestEvent` 把 `seq/ts_ms/epoch` 统一 `Number()`；`payload_json`（proto `bytes` → protojson base64）**已 base64 解码**；settings 的 `values_json` 同样解码。WS 侧是 `map[string]any` 对象，与 REST 归一后形状一致（`WireEvent` 字段名 `seq/event_id/session_id/type/ts_ms/epoch/request_id/payload` 与 `pkg/eventbus/eventbus.go:14-23` 的 json tag 完全对应）✅。**唯一不一致**：`RtcCreds.roomId` vs 方案里的 `channel`，且 `live.status.rtc` 类型里 `tokenExpireAt:number` 与后端字符串 `"0"`（protojson int64 字符串）——目前靠 `!s.rtc.tokenExpireAt` 的 falsy 判断侥幸成立，若后端返回 `"60"` 也 truthy，但类型声明是错的。

### 6.2 store 风险点

| store | 问题 |
|---|---|
| `events` | 全部状态放在模块级变量（`socket/backoff/pollTimer/...`），Pinia 实例状态只存缓冲；`connect` 无并发互斥（P0-5）；`resync` 无 in-flight 守卫，WS gap + poll gap 可并发触发 |
| `live` | `stableAction` 无重入锁（P1-4）；`loadVersion` 声明未用；`applyEvent` 要求 `status` 已存在，否则 `live.started` 在首帧到达时被丢弃 |
| `transcript` | `applyEvent` 只处理后端永不发布的事件（P0-3）；partial 按位置归并（P2-9） |
| `settings` | 未知**分组**被静默丢弃；`dirtyGroupIds` 用 `JSON.stringify` 比较（键序敏感，通常无害）；`session` 与 `misc` 两组重复定义 `record_enabled`（双源） |
| `uiPrefs` | 6/9 键无效（P1-19） |
| `stage.js` | 遗留、未鉴权、重复请求、死代码（P1-12） |

---

## 7. 实时状态机专项

| 能力 | 结论 | 说明 |
|---|---|---|
| `event_id` 去重 | ⚠ 机制在，但会被 P0-4 清空 | `seen` 以 `event_id` 为键；epoch 跳变清空、容量淘汰即删除（P2-17） |
| `seq` 缺口回补 | ⚠ 有，但会丢事件 | `gap = seq > lastSeq+1` → `resync()` 拉 `listEvents(lastSeq)` 逐条 `accept`；`accept` 只写 `latest`，页面只处理最后一条（P1-1）；且不拒绝 `seq <= lastSeq`（P1-2） |
| `epoch` 变更清空 | ❌ 判据错误 | 后端 `song/refs/action` 恒为 `epoch=0`，与 live 混流（P0-4） |
| 断线退避 | ✅ | `backoff = min(backoff*2, 30000)`，指数退避；`onopen` 重置 + 停轮询 |
| 降级轮询 | ⚠ | `startPolling` 15s、切后台跳过、回前台补拉一次；但 `StagePage` 另有 5s 无条件 status 轮询（P1-3），两条轮询并存 |
| 重复轮询 | ❌ | `disconnect→connect` 的旧 `onclose` 会再 `startPolling`（P0-5），并与 WS 并存至 `onopen` |
| 重复事件 | ❌ | 多 socket 并存（P0-5）+ 去重表被清（P0-4）→ 同一事件可二次进入 |
| 刷新页面竞态 | ⚠ | 刷新后 `lastSeq` 从 0 开始，WS 用 `after_seq=0` 补齐最多 2048 条历史，缓冲上限 300；字幕 `seed` 只接受 `status.transcripts` 首次快照，刷新后历史字幕靠 status 恢复 ✅ |
| 连点开播 | ❌ | 见 P1-4 |
| 切换场次 | ⚠ | 前端不清缓冲；靠 epoch 清（但 epoch 已被 song/action 污染）→ 实际表现为"随机清空/随机不清" |

---

## 8. RTC 播放器专项

| 检查项 | 结论 | 证据 |
|---|---|---|
| 四态定义 | ✅ 类型齐全（`unconfigured/joining/joined/failed`） | `components/rtc/types.ts` |
| 四态可达 | ❌ `joined` 不可达 | `StagePage.vue:43-48` 恒 `null` → 永远 `unconfigured` |
| 缺 SDK | ⚠ 未检测 | 无 `public/vendor`，无 `adapters/*`，无"SDK 缺失"分支文案 |
| token 过期 | ⚠ 只置 failed | `RtcPlayer` 倒计时到 0 → `setState('failed')`，不请求刷新（后端也没有刷新接口） |
| 断流 | ❌ 无处理 | 视频元素无 `stalled/waiting/ended` 监听、无重连 |
| 自动重连 | ❌ 无 | 方案 §6.6 要求"失败重试一次"，未实现 |
| 是否把失败静默成成功 | ❌ 部分 | `emit('autoplay-blocked')`/`emit('error')` 在 `VideoStage` **无人监听**（只接 `@state`），`creds=null` 时 UI 又呈现为"待配置"，用户无法区分"没配"与"配了但失败" |

---

## 9. 面板完整性（对齐后端 54 条面板路由）

后端 `internal/server/panels.go` 注册 **54 条**（与需求口径一致）：TTS 8 + Actions 8 + Choreo 8 + Prompt 3 + Songs 6 + Sessions 3 + Cost 3 + Danmaku 1 + Koubo 14。

**服务层覆盖 52/54**：缺 `POST /api/v1/prompt/type14`、`POST /api/v1/cost/ledger`（后者为接入侧写入口，UI 不需要）。
另有 5 条弹幕 RPC（`GetState/ListPending/Confirm/Dismiss/SetMode`）走 proto 生成路由，**前端全未接**。

| 面板 | 已接路由 | 加载 | 空态 | 错误态 | 保存态 | 校验 | 敏感掩码 | 离开提示 | 缺失 |
|---|---|---|---|---|---|---|---|---|---|
| 声音 Voice | tts 8/8 | ✅ | ✅ | ✅ | 即时写入（无"保存"按钮） | ❌ | —（无密钥） | ❌ | 删除无确认；试听 URL 泄漏；与"设置.音色"双写 |
| 动作 Command | actions 8/8 + choreo 8/8 | ✅ | ✅ | ✅ | 即时 | ❌ | — | ❌ | 动作删除靠右键（无键盘等价、无确认）；编排只有 run/start/stop，缺"组/步骤/间隔/循环/随机/暂停 VIP"编辑 |
| 口播 Koubo | koubo 11/14 | ✅ | ✅ | ✅ | `@change` 即时（无回滚） | ❌ | — | ❌ | **merge / seg-split / actions_all 三按钮未暴露**（服务已备） |
| 参考图 Refs | prompt 2/3 | ✅ | ✅ | ❌ 被 `load()` 清掉 | 即时 | ❌（可重复加同图） | — | ❌ | type14 未接；错误被吞（P1-7） |
| 歌曲 Songs | songs 6/6 | ✅ | ✅ | ✅ | 即时 | ❌ | — | ❌ | 删除无确认；播放状态不轮询/不订阅事件（界面会长期停在"播放中"） |
| 复盘 Archive | sessions 2-3 + cost 2 | ✅ | ✅ | ⚠ 与旧数据并存 | 只读 | — | — | — | 固定 20 条无分页；原生 select；phase 裸渲 |
| **弹幕** | **0/5** | — | — | — | — | — | — | — | **整个面板缺失**（P1-10） |
| 设置 Settings | settings 2 + 12 组 | ✅ | ✅ | ✅ | 保存 + reload | ❌ 数字不校验 | ✅ `NtSecretText` | ❌ | 6 组"假字段"（P1-9） |

**关于口播三个未暴露按钮**：判断为**刻意裁剪但未记录**——服务层函数名与后端路由一一对应，且有注释说明接口可用，但 UI 无任何"v1 不提供"的说明，方案偏差清单里也没有。这种状态下评审无法区分"设计决策"与"漏做"，需在方案/README 中显式登记。

---

## 10. 可访问性与可用性

| 项 | 结论 | 证据 |
|---|---|---|
| 键盘可达 | ⚠ | 所有可点元素都是 `<button>/<input>/<select>/<label>`，Tab 可达；但面板开关无 `aria-pressed`、tab 无 `role=tab`；动作删除仅右键 |
| 焦点管理 | ❌ | 抽屉打开不移动焦点、关闭不回焦、无 focus trap（P2-10） |
| aria | ❌ | 全工程 `aria-live/role=alert/aria-current/aria-selected/aria-pressed` **0 命中**（P1-16）；`aria-modal` 用在非模态内联区块（P1-17）；`<i aria-label>` 用在装饰点（`SettingsDrawer` 脏点）无 `role` |
| 对比度（深色） | ✅ | tokens 的 `text-0/1/2/3` 对 `data-bg/space-bg` 均 ≥5:1；`--nt-danger/--nt-warn` 浅深两套都过 4.5:1（已按 tokens 值核算） |
| 颜色不只靠色相 | ⚠ | 状态胶囊有文字标签 ✅；但 `NtStatusPill` 边框写死暗色 rgba，`data-colorVision='safe'` 下不跟随 |
| 错误可读性 | ⚠ | 文案是中文人话（`classifyApiError`）✅；但 RefsPanel 会吞掉错误（P1-7）、多处 `error` 与 `note` 同时显示 |
| 窄屏 | ❌ | 见 P0-6 |
| 文本/输入框标签 | ⚠ | Bearer 输入框只有 placeholder 无 `<label>`；Composer 文本域无标签 |

---

## 11. 性能

| 项 | 数值/结论 |
|---|---|
| 首屏体积 | JS **194.79 KB**（gzip 68.68）；CSS **250.52 KB**（gzip 41.95）。比 M2 报告里的 171/246 又涨了 ~24KB/4KB |
| CSS 构成 | 约 **240KB 是 Quasar 基础 CSS**（3517 个 `.q-*`），本工程 0 个 Quasar 组件 → 纯浪费（P1-18） |
| 高频事件渲染 | `RightRail` 每次渲染 `events.slice().reverse()`、`lines.slice().reverse()`（新数组）；300 条上限下可接受；但 `StagePage` 的 `watch(latest)` 每事件触发一次 `live.applyEvent` + `transcript.applyEvent`，**字幕 partial 每 token 一次会把整列重新 diff** |
| 大列表虚拟化 | 未做，也不需要（事件 300、字幕 200、场次 20）；但**无上限约束**的列表有：`RefsPanel.pending`（可无界追加）、`CommandPanel.actions`（库不设上限） |
| 轮询成本 | 5s status + 5s ledger + 15s events + 1.5s koubo（播放中）→ 空闲时每分钟 12 次 status、12 次 ledger；开播 + 口播时叠加 |
| 内存泄漏 | 1) WS socket 泄漏（P0-5）；2) `VoicePanel` blob URL + 音频不停止（P2-14）；3) `KouboPanel` 试听 blob URL 不 revoke；4) `PanelShell` 拖动监听有清理 ✅；5) `RtcPlayer` 倒计时 `clearInterval` 有 ✅；6) `StagePage` 两个定时器都有清理 ✅ |

---

## 12. 测试质量

24 例 = `events-core` 5 + `settings` 5 + `SettingsDrawer` 3 + `ComposerBar` 5 + `live(api)` 2 + `uiPrefs` 4。

**覆盖了什么**：事件纯函数（去重/gap/epoch/裁剪）、设置 store（12 组/脏值/保存/未知字段保留）、抽屉渲染与密钥掩码、Composer 禁用逻辑、REST 归一化（int64/base64）、本地偏好持久化。

**漏了什么（且正好是出事的地方）**：
1. `App.vue` 挂载（→ P0-1 白屏）；
2. `events.ts` 状态机（connect/disconnect/resync/poll/退避/并发）→ P0-5、P1-1 全裸奔；
3. `live.ts` 动作流（busy/双击/事件应用）→ P1-4；
4. `transcript.ts`（partial/final 归并）；
5. `RtcPlayer` 四态；
6. `StagePage`（轮询、定时器清理、`creds`）；
7. 6 个业务面板（三态、校验、确认）；
8. `client.ts`（鉴权头、401、超时）；
9. `panels.ts` 的 52 个端点（无契约测试，字段名拼错只能运行时发现）。

**"把 mock 当验证"**：是的。所有用例都显式 `vi.mock` 掉 API 与 store，`vitest.config.ts` 注释直接写明"不启动 Quasar 壳"，因此"24/24 通过"证明的是组件内部逻辑，不是应用可运行。`events-core.test.ts` 更是把**乱序回放**这一缺陷行为断言成了期望值（P1-2）。

---

## 13. 可直接执行的修复清单（按顺序）

**第 1 批（解除阻断，必须当天完成）**
1. 新建 `web/src/boot/pinia.ts`，`quasar.config.js` 改 `boot: ['pinia']`。
2. 新增 `web/src/boot/theme.ts`（或 `index.html` 内联脚本）落实 R-UI-60；给 `html` 加底色兜底。
3. 新增冒烟测试 `web/src/__tests__/app-mount.test.ts`：`mount(App)` + 断言 `#q-app` 有子节点（防止 P0-1 回归）。
4. 删除 `web/src/stores/stage.js`，`App.vue` 顶部状态改为读 `live`/`events`；同时去掉"M0 骨架"文案与 App 级品牌头（P1-12/13）。

**第 2 批（实时链路正确性）**
5. `events.ts`：`connect` 加 in-flight 互斥；`onclose` 加代际判断；`disconnect` 先取局部引用（P0-5）。
6. `events-core.ts`：拒绝 `seq <= lastSeq`；`epoch` 判据改为 `session_id` 变化（或改判 `epoch` 仅在同 session 内比较）；同步修 `events-core.test.ts` 的乱序断言（P0-4/P1-2）。
7. `events.ts` + `StagePage.vue`：把"逐条 accept → latest"改成"事件队列 → 按序消费"，页面按序应用（P1-1）。
8. `StagePage.vue`：status 轮询改为"仅 WS 降级时 + 可见性感知 + 版本守卫"（P1-3）；为此新增 `web/src/utils/polling.ts`。
9. `live.ts`：`stableAction` 加 in-flight 锁；开播按钮绑 `busy` 并复用 `idempotencyKey`（P1-4）。

**第 3 批（功能补齐 / 假功能清理）**
10. RTC：二选一——补 `GetViewerToken` + `adapters/artc.ts` + 过期刷新 + 断流重连；或把画面区改成显式"v1 不交付画面"阻断态并删死代码（P0-2）。
11. 字幕：后端补 `transcript.partial/final`（带 `utterance_id`）；前端按 id 归并（P0-3/P2-9）。若不做，改文案为"历史字幕"。
12. 补 `DanmakuPanel` + 4 条写接口（P1-10）。
13. 口播三按钮：补 UI 或写入"v1 裁剪"说明（P1-11）。
14. 设置：`captions` 的 5 个字段接渲染，或从 schema 删掉；补"清空记忆/应用人设"；删除重复的 `record_enabled`（P1-9）。
15. `uiPrefs`：6 个死键接 UI 或删除（P1-19）。

**第 4 批（交互与规范）**
16. 全站破坏性操作接 `NtConfirmDialog`（6 处，P1-5）。
17. 设置抽屉加"未保存离开"拦截；`SettingsPage` 首挂载跳过 `load()`（P1-6）。
18. `RefsPanel` 修 `catch → load()` 吞错（P1-7）；`KouboPanel` 分段编辑失败回滚（P1-8）。
19. 断点：`<1280` 收 Rail、`<960` 单列、`<720` 卡片；`StatsBar` 窄屏横滚；`PanelShell` clamp + 重置（P0-6/P2-19）。
20. 抽屉宽度 640 + `mode` 区分 modal/inline + focus trap/回焦（P1-17/P2-10）。
21. 裸色值 10 处改用令牌（并就 `color-mix` 补状态边框令牌），新建 `theme/ui-literals-allowlist.json` 与 `ui-literals.test.ts`（P2-1/P2-2）。
22. 枚举映射 `STATUS_MAP`（phase/event.type/EVID 状态）；`duration` 显 `—`；`tabular-nums`（P2-3/4/5）。
23. `aria-live`/`role=alert`/`aria-selected`/`aria-pressed` 全站补（P1-16）。
24. 快捷键登记（F9/F10/`/`/Ctrl+K）+ Composer 的 `/` 指令分流（P1-15）。
25. `client.ts` 加 10s 超时 + 401 引导条（P1-14）。

**第 5 批（体积与收尾）**
26. 评估 `framework: false` 去掉 240KB Quasar CSS（P1-18）；`package.json` 版本改 `0.1.0-m3`；处理 `npm audit` 的 2 个 critical（P2-20）。
27. 补测试：`events` 状态机、`live.start` 并发、`StagePage` 定时器、`RtcPlayer`、`client` 401、面板三态（P1-20）。

---

## 14. 未验证 / 无法验证项（诚实标注）

| 项 | 原因 |
|---|---|
| 真实 RTC 入会、断流、自动重连 | 后端无 token 接口、无 SDK 文件，**无法验证**，只能判定"不存在" |
| 真实 Vidu / OmniVoice 联调 | 后端当前为 fake controller（`Vidu/OmniVoice 未接入` 日志） |
| 157/4K 大屏与 320px 极窄布局的像素级表现 | 应用白屏，无法截图；结论基于 CSS 推导 |
| 浅色主题的实际观感与对比度 | 同上；对比度按 tokens 值计算 |
| 数据库落库 / 重启续播 | 无 PG/Redis，本次仅验证前端契约 |
| 24 例以外的人工交互（点击全部面板） | 白屏阻断；面板逻辑仅做静态审读 |

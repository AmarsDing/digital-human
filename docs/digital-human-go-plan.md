# 数字人直播项目 · 全盘克隆方案（Go + Quasar）

> 版本：**v2.3（B/C/D/E 批完成同步版，结构不变）**；上一版 v2.2。
> 修订依据：`f:\myproject\teamwork\digital-human-go-plan-review.md`（P0×10 / P1×25 / P2×14）与第一～三批验收反馈；v2.1 变更点见 §14。
> 功能蓝本：`f:\myproject\teamwork\cyber-xiaoya-live-main`（Python 3.12 + FastAPI + 单页 stage + Vidu + OmniVoice，AGPL-3.0）。
> 架构参照：`f:\myproject\teamwork\HotCraftFlow`（Go 1.26 + Kratos v2 + Ent + Wire + Proto/Buf + Asynq；Quasar 2.19.3 + Pinia 3.0.2）。
> 前端规范：`f:\myproject\teamwork\NovaTrader\doc\开发文档\前端UI\README.md` 与 `NovaTrader\web\src`（Vue 3.5.30 + Pinia 4 + vue-router 4，**未使用 Quasar**）。
>
> **阶段约束**：本文件只出方案。不写实现代码、不初始化项目目录、不修改上述任何仓库。
>
> **标记约定**：✅ = 本轮已用源码/文档核验；🟡 = 本方案新增的设计（蓝本没有）；⏳ = 待验证（含验证方式、责任与门禁）。
> 文档内所有行号均以本轮实际读取的本地快照为准；`cyber-xiaoya-live-main-aranea-plan.md` / `...-plan-review.md` 中被判“不成立/夸大”的结论未沿用。

---

## 0. 结论先行

**架构一句话**：单进程 Kratos 单体（HTTP + WebSocket，不启 gRPC）承载“控制面 + 实时音频面”，Python OmniVoice 以独立边车进程保留，RTC 画面仍由浏览器 SDK 订阅且 Go 只签发/下发 token；v1 **明确单场**（`SessionManager{max:1}`），数据面用 PostgreSQL + Redis，事件在进程内总线分发并落库支持断线补齐与复盘。

**关键取舍 3 条**

1. **不做代码级 1:1 克隆，做 clean-room 行为对齐**。蓝本是 AGPL-3.0（`LICENSE:1`），且 `NOTICE:6-12` 明确把人物形象、克隆音色、人设/文案/歌单/动作库、品牌排除在授权之外；复制代码、UI 结构或素材会同时踩协议与素材两条线。方案只对齐**可观测行为**，实现与规格分两拨人（§9）。
2. **TTS 保留 Python 边车，不纯 Go 化**。OmniVoice 走 `gradio_client` + `session_hash`（`src/tts/omnivoice.py:3-13`），不是稳定 REST；Go 侧用**重写的薄 HTTP 契约**（二进制 wav + job 取消 + 健康 + 并发=1），把不确定的 Gradio 语义锁在边车内部（§5.2）。
3. **Go 不接管 RTC 拉帧**。蓝本画面由浏览器 AliRTC/TRTC Web SDK 订阅（`stage/index.html:1101-1188`），Go 无对等官方 SDK；Go 只做 token 生成、过期策略与场次生命周期，需要画面帧时走 Vidu 托管录制（§4.8）。

**工期区间**：**16–20 周**（2 Go + 2 前端 + 0.5 QA）。若前端只有 1 人，**20–24 周**。原 10–14 周不成立，差额来源见 §10.2。

**最大风险**：**48B/ms 实时喂流与 Vidu 信令的时序正确性**（P0-9）。它同时耦合节奏、漂移、喂流权抢占、看门狗重连与 TTS 预取；错位表现为“画面在但没声/断流/叠音”，且只能靠真实 Vidu 会话 + 8h 压测证明，属于 P0。

**修订状态**：评审 P0-1～P0-10 已全部关闭；P1 全部逐条处理（2 条部分采纳并给出反证）；P2 14 条全部处理。对照表见 §13。

---

## 1. 事实基线（本轮核验，评审事实底座）

| # | 事实 | 实际值 | 证据 |
|---|---|---|---|
| B1 | bridge 路由数 | **63** 个 `@app.*` | `src/bridge.py` 全文计数 |
| B2 | koubo 路由数 | **14** 个 `@router.*`（不是“14 个动作”） | `src/koubo.py` 全文计数 |
| B3 | Vidu WS 心跳 | `ping_interval=10`、`ping_timeout=20`、`close_timeout=5` | `src/vidu/signaling.py:84-86` |
| B4 | `conn_init` 重试 | 15 次 / 2.0s；`NOT_READY` 重试不重连 | `src/vidu/signaling.py:126-148` |
| B5 | 断流判据 | `idle_recv ≥75s` + ≥3 个采样 + 窗口 ≥20s + 推流速率 **≤0.55x** | `src/engine.py:433-461` |
| B6 | 重连 | 最多 **6** 次、两次之间最小 **20s**；另有“距上次重连 <60s 不判 stall”的抑制窗 | `src/engine.py:463-490`、`456-457` |
| B7 | 闲置 / 硬上限 | **300s / 1800s** | `src/engine.py:634-646` |
| B8 | 看门狗节拍 | 1s；事件循环阻塞 >1.2s 记录，告警 10s 节流 | `src/engine.py:607-633` |
| B9 | 音频守卫 | 每 **5s**；`stale>30s` 且 `hold>30s` 才收回；歌曲任务豁免 | `src/engine.py:492-534` |
| B10 | 创建会话超时 | **120s** | `src/vidu/client.py:66` |
| B11 | 查询/录播/列表/额度超时 | **8 / 15 / 8 / 8s** | `src/vidu/client.py:72,77,83,91` |
| B12 | 列表分页参数 | **`pager.page` / `pager.pagesz`** | `src/vidu/client.py:86` |
| B13 | 三步上传第 2 步 | presigned `PUT`，**不带鉴权头** | `src/vidu/client.py:8,102-105` |
| B14 | 动作库 403 降级 | 去掉 `action` 重建一次 | `src/engine.py:321-335` |
| B15 | 录播 | 仅 managed 且 `enable_recording=true`；轮询上限 **1800s**（代码），间隔 15/30/60s | `src/engine.py:341-346,719-758` |
| B16 | 喂流 | 默认 100ms 分片；单帧 `send` 超时 **3s**；落后 **>300ms** 告警（10s 节流）；**不跳帧、不补发** | `src/engine.py:1703-1761`、`src/tts/audio.py:16-17,75-83` |
| B17 | 动作优先级 | `vip=3 > manual=2 > choreo=1 > 其他(t0/t2/confirm/auto)=0` | `src/engine.py:1168-1171` |
| B18 | 动作节奏 | 同动作 8s、不同动作 2s、唱歌窗口 6s；手动 0s；队列上限 3 | `config.example.json:89-94`、`src/engine.py:1350-1379` |
| B19 | 礼物合并 | 窗口 6s；≥4 人或 ≥50 钻立即；最多 4 名；最小间隔 25s / 紧急 10s / 每分钟 2 次 | `config.example.json:99-108`、`src/engine.py:145-167` |
| B20 | 贵宾 | 阈值 **99 钻**；冷却 30s；模板可配 | `config.example.json:110-115` |
| B21 | 弹幕缓冲 | 最近 **8** 条；**无按文本去重**；`dropped` 计数 | `src/dm_router.py:105-109,900-918` |
| B22 | 弹幕节奏 | `read_interval_ms` 配置 3000、引擎兜底 20000；VIP 5000；冷场首条收集窗 1.2s；批读默认最多 14 条 | `config.example.json:64-70`、`src/engine.py:2381-2401` |
| B23 | 分句 | `soft_min=10`、`hard_max=45`；合并 `min_chars=30`、`first_min=14` | `src/llm.py:47-117` |
| B24 | TTS 队列 | `asyncio.Queue(maxsize=8)`；逐句 + 整段两种模式 | `src/engine.py:1568,1669` |
| B25 | 状态接口 | `status(since)` 的 `since` 是**字幕下标**（`i`），不是全局事件序号；无 `/tr_list` 路由 | `src/engine.py:3006-3075`；`src/bridge.py` 全文 |
| B26 | 设置分区 | **12 组**：会话 / 声音 / 专属会员 / 人设 / LLM / 自备 RTC / 动作库 / 资产库 / Vidu Key / 画面内字幕层 / 弹幕主播模式 / 其他 | `stage/index.html:683-818` |
| B27 | stage 规模 | 3534 行 / 210,162 字节 / **128 个具名函数**；fetch 轮询，**无 WebSocket** | `stage/index.html` |
| B28 | localStorage | **9 个键**（评审说 7 个，实测多 `statsPos`、`kbCmdOpen`）：`s2s.stage.cmdFx`、`s2s.stage.stats`、`s2s.stage.statsPos`、`s2s.stage.rndEvery`、`s2s.stage.rndBias`、`s2s.stage.cmdEdits`、`s2s_songPath`、`s2s.stage.rightW`、`s2s.stage.kbCmdOpen` | `stage/index.html:1560,1231,1483,1869,1871,1955,2723,3123,3346` |
| B29 | 歌词 | 运行时读 `lyrics-cache/*.lrc.json`，否则同名 `.txt`；打轴由 `tools/lyrics_align.py` 离线生成 | `src/engine.py:1857-1931`、`tools/lyrics_align.py` |
| B30 | 舞台面板 | 浮动面板 **5 个**：音色 `#vpanel`、快捷指令 `#cmds`（动作/编排两个 tab）、口播 `#koubo`、参考图 `#refs`、歌单 `#songs`；右栏 `#rightPanel` 只放字幕/事件；贵宾名单在设置组“专属会员”，**不是独立面板** | `stage/index.html:496-533,515,609,634,663,712-719` |
| B31 | 快捷键 | `Enter`（#txt、#txtCmd）、`Esc`（关设置抽屉/歌单）；**无静音快捷键** | `stage/index.html:3297-3299` 等 |
| B32 | NovaTrader Token | `--nt-bg-0:#050d17`、`--nt-bg-1:#0b1b2b`、`--nt-accent:#45e0ff`、`--nt-text-0:#eef6fa`、`--nt-text-3:#85a5bc`、`--nt-up:#ff6b81`、`--nt-down:#60b296` | `NovaTrader/web/src/theme/tokens.css:8-45` |
| B33 | NovaTrader 组件归属 | `ui/` 是 Nt* 基础件（**无 `NtPanel`**）；`NtStatCard`、`NtTimeline`、`NtReportCard` 在 `display/` | `NovaTrader/web/src/ui`、`web/src/display` 文件清单 |
| B34 | HFC proto 数 | **75**（不是 78） | `HotCraftFlow/apps/server/api` 计数 |
| B35 | HFC 构建阻断 | `.gitignore` 忽略 `storage/`；`data.go:20` import `pkg/storage` 但目录不存在 | `HotCraftFlow/.gitignore:16-19`、`apps/server/app/admin/internal/data/data.go:20` |
| B36 | HFC 只有 HTTP | 全仓无 `RegisterGRPCServer`；admin `rest.go` 仅注册 HTTP | `apps/server/app/admin/internal/server/rest.go:184-244` |
| B37 | HFC Wire | 只有 `app/admin/cmd/wire_gen.go`，media 无 wire 生成物 | 全仓 `wire*.go` |
| B38 | 蓝本无鉴权 | `bridge.py` 全文无 `Depends` / Origin / CORS 校验；`stage/index.html` 只显示 key 尾 8 位 | `src/bridge.py`、`stage/index.html:784` |

**评审自身需纠正的 5 处**（本方案已以源码为准，并在 §13 标注）：

| 编号 | 评审说法 | 源码实际 | 处理 |
|---|---|---|---|
| R1 | localStorage 7 键 | **9 键**（多 `s2s.stage.statsPos:1560`、`s2s.stage.kbCmdOpen:3346`） | 采纳“补键”方向，数字改为 9 |
| R2 | `read_interval_s` 默认 4s | 配置 **3000ms**，引擎兜底 **20000ms** | 按源码写两个值 |
| R3 | “60s 冷却无出处” | `engine.py:456-457` 有 `now - last_reconnect < 60 → 不判 stall` 的**抑制窗**，不是重连冷却 | 部分采纳：删除“重连冷却”，保留“stall 抑制窗”并改名 |
| R4 | F44 漏“贵宾面板” | 贵宾是设置组 `stage/index.html:712-719`，编排是 `#cmds` 的 tab（`:530-533`） | 按实际结构落点 |
| R5 | P1-16 称“VoicePanel 不是独立面板，只是设置组” | `#vpanel` 是独立浮动面板（`stage/index.html:515-521`）；同时另有设置组“声音”（`:702-710`） | 部分采纳：两处都要复刻，面板归 `VoicePanel`，设置组归 `VoiceSettings`（`§6.2`） |

---

## 2. 范围与验收

### 2.1 三分法：Go 侧实现 / 依赖外部进程 / 本期不做

| 类别 | 内容 | 判定依据 |
|---|---|---|
| **Go 侧实现** | 场次状态机、Vidu HTTP/WS 客户端、48B/ms 喂流、喂流权、看门狗、弹幕路由、动作调度、礼物/贵宾、编排、口播编排（音频拼接可由 ffmpeg）、设置/人设/素材/Key CRUD、事件流与复盘、鉴权/审计/可观测性 | 纯逻辑或仅依赖 Go 库 |
| **依赖外部进程**（Go 通过受控契约调用） | OmniVoice TTS（Python 边车）、ffmpeg/ffprobe（解码/转码/时长）、`lyrics_align.py` 打轴（离线）、抖音弹幕采集器（第三方）、Vidu 云渲染 | 蓝本本身即进程外/云端（`README.md:47-54`） |
| **本期不做（v1）** | Agora RTC（蓝本也未支持，`stage/index.html:1103`）、Go 侧 RTC 拉帧、多场并发、自动续期 managed RTC token、LLM 流式中断回退、跨场次无限历史 | 见 §2.4、§8 |

### 2.2 1:1 复刻本身不合理的清单

| # | 功能 | 为什么不合理 | 方案处理 |
|---|---|---|---|
| N1 | 浏览器 RTC 播放/订阅 | AliRTC/TRTC 只有 Web/移动 SDK；Go 无对等官方实现（`stage/index.html:1107-1188`） | 保留浏览器订阅；Go 只发 token |
| N2 | 自动播放 | 浏览器策略要求用户手势（`stage/index.html:1190-1197` 的 `play().catch` + 点击兜底） | 前端保留“点击画面开启声音” |
| N3 | OmniVoice Gradio 会话 | `gradio_client` + `session_hash` 是 Python 生态细节，不是公开 REST 契约 | 保留 Python 边车，Go 用自有 HTTP 契约 |
| N4 | Vidu 云端渲染 | 渲染在 Vidu 侧，本地无对应能力 | 仅做客户端 |
| N5 | 抖音弹幕/礼物采集 | 依赖平台私有协议与外部采集器 | 不改采集器；保留 `/api/danmaku` 兼容入口 |
| N6 | 页面 localStorage 偏好 | 浏览器本机特性，服务端无法等价还原 | 前端仍用 localStorage；如需跨端再做服务端 UI 偏好（v2） |
| N7 | managed RTC token 续期 | 平台**不提供**刷新端点，只下发 `token_expire_at`（`engine.py:380-385`） | 到期提示 + 可选重开（会中断）；不承诺无感刷新 |
| N8 | 歌词打轴 | 蓝本用离线 Python 工具生成缓存 | 保留外部工具或重写；v1 先“外部工具 + 缓存契约” |

### 2.3 外部依赖边界

| 依赖 | 端口/入口 | 契约 | 失败可见性 |
|---|---|---|---|
| Vidu OpenAPI | `https://api.vidu.cn` | §5.3 端点表 | 错误码映射 + 面板提示 |
| Vidu WS | `wss://{host}/live/v1/external-lives/{live_id}/stream?conn_id=...` | §4.1 | WS 状态、断流告警、重连计数 |
| OmniVoice 边车 | `127.0.0.1:9877`（蓝本）→ 新边车 `:9880` | §5.2 | `/v1/health` + 队列深度 + 熔断 |
| ffmpeg/ffprobe | 本机二进制 | 解码 24k s16le、转码、时长 | 任务失败事件 |
| 歌词打轴 | `tools/lyrics_align.py` | 输入音频/文本 → `lyrics-cache/*.lrc.json` | 缺失则回退 `.txt` |
| 弹幕采集器 | 外部进程 → `POST /api/danmaku` | **永远 200 + reason**（`bridge.py:737-766`） | `dropped`/`reason` 计数 |
| LLM | OpenAI 兼容（默认 DeepSeek） | SSE/JSON | 降级模型 + 失败提示 |
| RTC | 浏览器 SDK | token/频道 | 入会重试 1 次（`stage/index.html:1141-1155`） |

### 2.4 验收对照表 F01–F51（重写为可测口径）

> 判定原则：每条都有“可观测结果 + 测量方法 + 前置条件”。需要真实 Vidu 会话的条目标注 **[真机]**；其余可用 fake Vidu / 单元测试 / 故障注入完成。
> “落点”列即“xiaoya 功能 → Go 模块 / 前端组件 / 外部进程”的完整映射（与 §12.3 汇总表一致）。

| ID | 蓝本行为（证据） | 落点（Go / 前端 / 外部） | 判定标准（可测） | 测试方法 / 前置 |
|---|---|---|---|---|
| F01 开播 | `phase==live` 时先 `stop("reconnect")` 再建场；创建 120s；403 动作库降级重建一次（`engine.py:303-346`） | `biz/live.StartLive` + `engine/live_engine` + `vidu_client`；前端 `StagePage`/`LiveControlBar` | ① 重复开播：旧场先 hangup，新场 `live_id` 不同且旧场状态 `ended`；② 创建 P95 ≤120s；③ UI 200ms 内进入 `starting` 并有阶段文案；④ 403 动作库时自动去 `action` 重建成功并产生 `action_library_degraded` 事件 | 单测（fake server 注入 403/慢响应）+ **[真机]** 真账号换 key 后开播 |
| F02 收播 | 发 hangup → sleep 0.4s → finalize；无场次时幂等返回（`engine.py:693-703`） | `biz/live.StopLive` + `engine` | ① 收播 P95 ≤5s 进入 `ended`；② 重复收播 200 且 `note=没有进行中的会话`；③ `starting` 阶段 Stop 取消建场请求且不残留 `live_id` | 单测 + 注入慢创建 |
| F03 状态/增量 | `status(since)`：`since` 是**字幕下标**；无事件 seq；返回 phase/ws/stats/dan/acts/choreo 等（`engine.py:3006-3075`） | `biz/live.GetLiveStatus` + `data.SessionEvent`；前端 `useLiveStore` | ① `transcript_since` 与 `event_seq` 两个游标独立；② 断线 5s 后按 `after_seq` 补齐，字幕无重复、无空洞（事件以 `event_id` 去重）；③ 单次响应 P95 ≤50ms（本机） | 单测 + WS 断线回放；事件落库前置 |
| F04 WS 生命周期 | 建连 → `conn_init`（15×2s）→ `init_ok`；心跳 10/20s；`type=2` 成功才算就绪（`signaling.py:80-148`） | `engine/vidu_ws` | ① 20s 无 pong 触发断连（日志时间戳与配置一致）；② `conn_init` 在 `NOT_READY` 时不重连 WS、只重发；③ 未就绪前上行的音频/type7/type11 被丢弃并计数 | PoC：本地 WS server 模拟 pong 缺失 |
| F05 断线/看门狗 | 75s 无回包 + ≥3 采样 + ≥20s 窗口 + 速率 ≤0.55x → 原地重连；6 次、间隔 ≥20s；距上次重连 <60s 不判 stall（`engine.py:433-490`） | `engine/watchdog` | ① 限速到 0.3x 时 75–95s 内触发重连；② 6 次后不再重连并产生 `ws_reconnect_exhausted`；③ 两次重连间隔 ≥20s；④ 纯 TTS 静默 252s 不误判（速率 ≥0.55x 时） | 故障注入（限速/静默/关闭写端）+ 长稳 |
| F06 异常清理 | `finalize` 清缓冲、song_epoch++、取消说话、清 owner；任务可重入（`engine.py:648-691`） | `engine` + `biz/live` | ① 结束 2s 内 goroutine 数回落到基线（goleak）；② 同一 `event_id` 重复投递只处理一次；③ 重复 finalize 不重复写结束事件 | `go test -race` + goleak；重复事件注入 |
| F07 创建会话 | body：`model` +（`image_uri` 或 `avatar_id`）+ `action` + `managed_rtc{mode,provider,enable_recording}` 或 `customer_rtc{...}`（`engine.py:258-301,318-335`） | `vidu_client` + `biz/live` | ① 缺形象直接报 `avatar_missing`；② managed 请求含 `enable_recording=true`（开启时）；③ customer 请求含 `channel_id/user_id/token/app_id`；④ 403 降级路径有单测 | 单测（golden body）+ **[真机]** |
| F08 查询/录播/额度/列表 | `GET lives/{id}` 8s、`recording` 15s、`lives` 8s（`pager.page/pagesz`）、`ent/v2/credits` 8s（`show_detail=false`）（`client.py:72-96`） | `vidu_client` + `biz/billing` | ① 四个端点的超时参数在代码常量表中；② 列表分页参数名为 `pager.page`/`pager.pagesz`；③ 404/410 映射 `live_not_found` 而非 500 | 契约单测 + **[真机]** |
| F09 资产/头像/动作库 | 三步上传；第 2 步 PUT 不带 Token；图片资产平台 90 天后自动删（`client.py:99-198,140`） | `vidu_client` + `biz/assets`；前端 `AssetSettings` | ① 第 2 步请求头**无** `Authorization`；② etag 缺失报 `upload_no_etag`；③ 资产列表显示“90 天回收”提示；④ 删除本地记录失败可回滚（先标 `pending_delete`） | 单测 + **[真机]** 上传 |
| F10 WS 信令 | 类型 1/2/5/6/7/9/10/11/12/14；本地补 `-1` 表示关闭（`signaling.py:39-52,90-124,150-194`） | `engine/vidu_ws` + `eventbus` | ① 10 种类型全有编解码单测；② 对端关闭时产生 `-1` 事件，且引擎不再认为会话存活 | 表驱动单测 |
| F11 24k s16le | `SAMPLE_RATE=24000`、`BYTES_PER_MS=48`（`tts/audio.py:16-17`） | `pkg/audio` | ① 任意 PCM 长度换算毫秒 = `len/48`；② 非 24k 输入在解码阶段被重采样 | 单测 |
| F12 节奏/漂移 | `t0` 绝对时基；`delay=t0+bytes/48/1000-now`；仅 `delay>0` sleep；`<-0.3s` 告警；`<-0.05s` 统计；**不跳帧**（`engine.py:1703-1761`） | `engine/feeder` | ① 100ms 分片下，8h 累计漂移 ≤100ms（首末 deadline 差）；② 单片唤醒 p99 ≤20ms；③ 落后 >300ms 产生告警且不丢帧；④ 代码中**不存在** 0.25s/1s 跳过阈值 | Windows 目标机 8h 压测（§4.3.3） |
| F13 喂流权抢占 | `preempt/respect_song/wait_only`；4 轮后强制；等旧 owner ≤1.5s；排队 25s 上限、持有人在喂流则续等；抢占发 type7 + 0.25s（`engine.py:831-938`） | `engine/ownership` | ① 并发 Acquire 只有一个 owner（race 测试 10k 次）；② 歌曲中打字聊天不打断歌（`respect_song`）；③ 卡死持有者 25–35s 内被收回并产生 `feed_owner_force_cancel`；④ 抢占后 300ms 内无叠音 | 并发单测 + **[真机]** 叠音听测 |
| F14 发送超时 | 单帧 `wait_for(send,3s)`；失败终止本轮、不重发（`engine.py:1733-1740`） | `engine/feeder` | ① 注入写阻塞 3s：本轮 ≤3.2s 退出；② 上游 TTS 生产被取消、队列被清空；③ `feed_send_timeout_total` 计数 +1 | 故障注入 |
| F15 LLM 流式 | 逐句流式；`soft_min=10`、`hard_max=45`；SSE（`llm.py:47-98`） | `biz/chat` + `llm_client` | ① 首句从请求到入 TTS P95 ≤3s（本机网络）；② 单句 ≤45 字；③ SSE 中断**不做“续播”**，直接结束本轮并记录 `llm_stream_aborted` | 注入断流；**[真机]** LLM |
| F16 短句合并 | 合并 `min_chars=30`、`first_min=14`（`llm.py:99-117`） | `biz/chat` | ① 首块 ≥14 字才合成；② 后续块 ≥30 字；③ 40+ 字无标点被硬切且不超 45；④ Markdown/表情/URL 清理有固定用例 | 表驱动单测 |
| F17 TTS 队列 | `Queue(maxsize=8)`；逐句与整段；生产阻塞（`engine.py:1568,1669`） | `engine/tts_client` + 边车 | ① 队列满时生产者阻塞而非丢弃，`tts_queue_depth` ≤8；② 整段模式只 1 个 job；③ 合成失败该句跳过并记录，不阻塞后续 | 单测 + 边车契约测试 |
| F18 整段/音频文件 | `script` 字幕、同名 `.txt` 兜底、点播即置 `_song_playing=True`（`engine.py:1857-1931`） | `biz/song` + `pkg/blob` | ① 有 `.lrc.json` 用时间轴，否则 `.txt` 逐行；② 点播受理 3s 内 `song_playing=true`（旧歌 epoch 失效）；③ 字幕与音频进度偏差 ≤300ms | **[真机]** 音频 |
| F19 type10 口播字幕 | 仅在 `captions.output_sentences=true` 时逐句上报（`engine.py:1569-1600`、`stage/index.html:708`） | `biz/chat` + `eventbus` → 前端 `CaptionOverlay` | ① 开关关闭时 `output_transcription` 事件为 0；开启时每句 1 条且顺序与音频一致；② `msg_id` 单调递增 | 单测 + WS 采集 |
| F20 音色/预览/文件 | `/api/tts/{files,upload,import-folder,delete,voices,voice,preview}` 8 个接口（`bridge.py:587-826`） | `biz/tts` + 边车；前端 `VoicePanel`/`VoiceSettings` | ① 切换音色**下一句生效**，不打断当前句；② 上传同名覆盖需显式确认；③ 预览 10s 超时并释放并发位；④ 显式删除不自动清理 | 单测 + 边车 |
| F21 弹幕接入 | `/api/danmaku` **永远 200 + `{ok,reason}`**；礼物同入口 `kind=gift`（`bridge.py:737-766`） | `biz/danmaku.Ingest` + 兼容层；前端 `DanmakuFeed` | ① 非法 JSON/未知 kind 返回 200 且 `ok=false`；② `mode=off` 时 `ok=false,reason=off`；③ 采集器不因 4xx 重试风暴 | 契约测试（沿用采集器） |
| F22 T0 精确命中 | 屏蔽词 + `auto_only_words` + 长度/冷却（`dm_router.py:659-705`） | `biz/danmaku` | ① 命中词表即 `t0` 直通（≤300ms 入队）；② 屏蔽词命中 `ok=false,reason=blocked`；③ `auto_only_words` 非命中词不自动答 | 表驱动单测 |
| F23 T2 模式 | `off/confirm/auto`；`set_config` 只切 confirm/auto（`dm_router.py:8,104,707`、`bridge.py:507-534`） | `biz/danmaku` + 前端 `DmModeSwitch`/`DmConfirmCard` | ① `off` 时全部丢弃并计数；② `confirm` 产生待确认卡片，`auto` 直接入 LLM；③ API 传 `off` 返回 200 且 `changed=false` | 单测 |
| F24 批读/风控 | 8 条缓冲；取最新；`dropped`/`t2_skipped`；间隔 3s（配置）/20s（兜底）；VIP 5s；冷场首条 1.2s；最多 14 条（`dm_router.py:105-109,900-918`、`engine.py:2381-2401`） | `biz/danmaku` + `engine/dm_worker` | ① 30 条/秒灌入 10s：无 panic、`accepted+dropped=total`；② 批读最多 14 条；③ 两条相同文本**均保留**（蓝本无去重）；④ 冷场首条延迟 1.2±0.2s | 压力测试 |
| F25 VIP 优先 | VIP 独立队列；最多 2 条、间隔 5s；VIP 优秀不承诺“显著”（`engine.py:2402-2438`） | `biz/danmaku` | ① VIP 与普通同时到达时 VIP 先进 LLM；② VIP 连续 10 条被限流为 5s/条；③ 指标口径为“VIP 批 P95 延迟 ≤ 普通批 P95”，**不写“显著”** | 时序单测 |
| F26 动作优先级 | `vip=3 > manual=2 > choreo=1 > 其他=0`；同动作 8s/不同 2s/唱歌 6s；手动 0s；队列 3（`engine.py:1168-1171,1350-1379`、`config.example.json:89-94`） | `biz/action` + `engine/action_queue` | ① 严格按 3/2/1/0 出队；② 冷却命中时入队最多 3 个，超限丢弃并计数；③ VIP 动作可打断普通动作，反之不可 | 时序单测 |
| F27 礼物合并 | 6s 窗口；≥4 人或 ≥50 钻立即；最多 4 名；间隔 25/10s；每分钟 2 次；最多 12 条（`engine.py:145-167`、`config.example.json:99-108`） | `biz/gift` + `engine/gift_worker` | ① 6s 内 5 个礼物合并为 1 次播报且名单 ≤4；② 第 5 个（第 4 人）触发立即 flush；③ 每分钟超过 2 次被延迟而非丢失 | 时序单测 |
| F28 礼物欢迎/贵宾礼 | 阈值 99 钻；冷却 30s；模板可配（`config.example.json:110-115`、`engine.py:2717-2815`） | `biz/gift` | ① 99 钻触发贵宾礼，98 不触发；② 30s 内同人只触发一次；③ 关闭总开关后 0 次 | 单测 |
| F29 贵宾名单 | 名单/IDs/保存/加入/欢迎（`bridge.py:878-979`） | `biz/patron`；前端 `PatronSettings` | ① 增删改查幂等；② 欢迎语模板变量渲染；③ “once_per_session” 生效 | 单测 |
| F30 编排 | 组/步骤/间隔/单次/循环/随机/暂停 VIP；VIP 跳过编排（`bridge.py:981-1089`、`engine.py:1417`） | `biz/choreo` + `engine/choreo_runner` | ① 循环次数准确；② 随机不连续重复；③ “暂停 VIP”时 VIP 不插队；④ Stop 后 ≤2s 停止 | 时序单测 |
| F31 口播编排 | **14 条路由**：state/split/seg/merge/seg/split/tts/tts_all/select/action/actions_all/play/play_all/stop/audio；`project.json` 持久化（`koubo.py:306-640`） | `biz/koubo` + `data.KouboProject`；前端 `KouboPanel` | ① 14 条路由全部有对应 RPC；② 重启后工程可恢复；③ TTS 串行锁（并发 2 请求时后者等待） | 单测 + 重启 |
| F32 歌曲点播 | 列表/上传/导入/解码/歌词缓存/字幕叠层（`engine.py:1857-1931`、`bridge.py:587-680`） | `biz/song` + `pkg/audio` + `pkg/blob` | ① 点播受理到出声 ≤3s（≤3 分钟文件）或 ≤10s（>10 分钟）；② 歌词与音频偏移 ≤300ms；③ 无歌词时逐行字幕 | **[真机]** 音频 + 听测 |
| F33 参考图 | 1–3 张；switch/remove；成功无 ack、失败 type14；不去重（`signaling.py:173-183`） | `biz/refs` + `engine/vidu_ws` | ① 发送 1/2/3 张均被接受；② 14 错误码回显到 UI；③ **不重发**（单测断言只发 1 次） | **[真机]** 参考图 |
| F34 录播 | managed + `enable_recording`；轮询 15/30/60s；上限 30 分钟（代码 1800s，docstring 写 10 分钟为不一致）；customer 不可录 | `biz/record` + `vidu_client` | ① managed 结束后轮询取得 URL；② 30 分钟停止轮询并标 `recording_timeout`；③ customer 模式提示不可用 | **[真机]** |
| F35 人工流水/事件 | 无 `/api/tr_list`；只有 `status(since)`；`interrupt`/`clear`/`stop`/`act/interrupt`（`bridge.py:437-467`） | `biz/live` + `data.SessionEvent` + `EventService` | ① 不提供“事件列表”HTTP；② `ClearTranscript` 只清字幕不删事件；③ 4 个中断接口语义各自可测 | 单测 |
| F36 统计/成本 | 额度：仅 live 时轮询、间隔 ≥30s（`credits_poll_s` 默认 30，代码 `max(30,...)`）、首测基线；统计 tokens/记忆/钻石/礼物；**不做播放量/TTS 成本**（蓝本无） | `biz/billing` + `data.CostLedger`；前端 `StatsBar` | ① 开播重置基线、下播停止轮询；② 额度下降量 = 基线 − 当前；③ 成本页只列 credits 与 tokens，其他标“估算/不提供” | 单测 + **[真机]** |
| F37 设置聚合 | `/api/settings` GET/POST + `/reload`；三文件（config/appconf/personas）（`bridge.py:828-877`） | `biz/settings`；前端 `SettingsDrawer` | ① 12 组字段全覆盖（§6.8 表）；② 生效时机逐项标注：**音色/字幕层/弹幕间隔即时**（`stage/index.html:683,802,810`），**其余下次连线生效**；③ 保存后**不重启**，按①的矩阵逐项验证：即时项 ≤3s 生效，连线项在 `stop→start` 后生效 | 契约测试 + 手工清单 |
| F38 人设 | 列表/选择/保存/删除；LLM 记忆重置（`bridge.py:536-547,1090-1144`） | `biz/persona` + `data.Persona`；前端 `PersonaSettings` | ① 切换人设后 system prompt 生效；② ResetLlm 清空历史且返回条数；③ 删除当前人设被拒绝 | 单测 |
| F39 头像 | 列表/上传/使用/删除；`avatar_id` 落配置（`bridge.py:1359-1457`） | `biz/avatar` + `vidu_client` | ① 使用后下次开播 body 含该 id；② 删除使用中头像被拒绝 | 单测 |
| F40 动作库 | 库 CRUD + 动作 CRUD（`bridge.py:1145-1305`） | `biz/action` + `vidu_client` | ① 每个动作有 `instruction` 与 `name`；② 删除后本地引用置空；③ 403 降级可重建 | 单测 |
| F41 音频素材 | 列表/上传/导入/删除/预览（`bridge.py:587-826`） | `biz/tts` + `pkg/blob` | ① 上传即解码校验 24k；② 删除被引用文件需确认；③ 列表含时长 | 单测 |
| F42 Vidu Key | 多 key/尾 8 位/新增/激活/删除/额度（`bridge.py:1458-1531`、`stage/index.html:772-786`） | `biz/vidukey` + `data.ViduKey` | ① 日志与响应永不出现完整 key；② 激活新 key 只影响下一次建场；③ 删除最后一个 key 被拒绝 | 单测 + 日志扫描 |
| F43 舞台主区 | 视频 `#video`、状态行、开播/收播、点击开启声音、全屏（`stage/index.html:430-495`） | `StagePage` + `VideoStage` | ① 16:9 自适应；② 状态行字段与 `status` 一致；③ 点击画面后 `video.muted=false` 且无报错 | E2E（Playwright） |
| F44 面板与右栏 | 5 个浮动面板（音色/快捷指令/口播/参考图/歌单）+ 右栏字幕事件；贵宾在设置组（`stage/index.html:496-533,609,634,663,683-818`） | `VoicePanel`/`CommandPanel`/`KouboPanel`/`RefsPanel`/`SongsPanel`/`RightRail`/`PatronSettings` | ① 6 个入口全部存在且可独立开关；② 拖动位置记忆；③ 右栏只放字幕/事件，不混设置 | E2E |
| F45 底部输入条 | `#txt` 说话、`#txtCmd` 指令、发送/停止/打断/清空按钮（`stage/index.html:838-900`） | `ComposerBar` | ① Enter 发送、Shift+Enter 换行；② 三按钮各自调对应接口；③ 空输入不发送 | E2E |
| F46 设置抽屉 | 12 组；key 尾 8 位；资产 90 天；清空记忆；弹幕模式；字幕开关；`read_interval`（秒） | `SettingsDrawer` + 12 个分组组件 | ① 12 组标题与字段逐项对照（§6.8）；② 敏感字段只显示尾 8 位；③ 保存前面板有未保存提示 | E2E + 截图比对（结构而非像素） |
| F47 面板拆分 | stage **128 个具名函数**、3534 行、无框架 | 前端模块化（§6.3） | ① 每个原函数有归属模块（映射表 100% 覆盖）；② 无 `window.xxx` 全局函数残留（除显式兼容层） | 静态映射检查 |
| F48 本地预设 | **9 个 localStorage 键**（B28） | `uiPrefs` store + `localStorage` | ① 9 键读写；② 清空站点后可恢复默认；③ 不与服务端配置混用 | E2E |
| F49 RTC 播放/token | managed 无刷新、只给 `token_expire_at`；customer 支持 ARTC 3 种形态 + TRTC UserSig；到期仅提示（`engine.py:380-385`、`rtc_tokens.py`、`stage/index.html:1101-1188`） | `biz/rtc` + `engine/rtc_token`；前端 `RtcPlayer` | ① managed 到 `token_expire_at` 前 15 分钟提示；② customer 三种 token 均能入会；③ 到期后自动重连 1 次，失败给“重新连线”按钮 | **[真机]** RTC |
| F50 复盘页 | 蓝本无此页（新增） | `biz/replay` + `ReplayPage` | ① 场次摘要（时长/峰值在线/礼物/收入/额度消耗）；② 事件时间轴可导出 JSON/CSV；③ 录像可回放（有 recording 时） | 单测 + E2E |
| F51 健康/指标 | `/api/health` 字段（`bridge.py:340-352`）；无 Prometheus | `server/healthz` + `metrics`；前端 `HealthPill` | ① 字段与蓝本对齐（§4.9）；② 新增 `feed_lag_ms`/`ws_reconnect_total` 等 P0 指标；③ `/metrics` 仅本机可访问 | 单测 + 抓取 |

### 2.5 评审指出的 30 条遗漏行为（G01–G30）

| ID | 遗漏行为 | 蓝本证据 | 落点 | 判定标准 |
|---|---|---|---|---|
| G01 | 开播后播开场白（文案+可用文件），失败只记日志 | `engine.py:352-365` | `engine/greeting` | 开场白发 type9/入场后 ≤5s 出声；失败产生 `greeting_failed` |
| G02 | managed 记录 `token_expire_at`，界面显示到期时间 | `engine.py:380-385` | `biz/rtc` | status 含 `token_expire_at`；UI 显示 |
| G03 | 额度轮询只在 live，首测基线，间隔 ≥30s | `bridge.py:79-124` | `biz/billing` | 下播后无 credits 请求（日志断言） |
| G04 | 人设含 `avatar_note`，切换提示需重新开播 | `src/personas.py`、`bridge.py:1105-1120` | `biz/persona` | 切换后 status 含 `restart_required=true` |
| G05 | 性格 prompt 与 `system_prompt` 双来源合并 | `src/personas.py` | `biz/persona` | 合并顺序固定且有 golden 输出 |
| G06 | LLM 流式超时与降级模型 | `llm.py:47-117` | `biz/chat` | 主模型 10s 无首 token → 降级；记录 `llm_fallback` |
| G07 | 清空对话记忆只清 LLM，不清字幕 | `bridge.py:536-547` | `biz/chat` | 调用后 `llm_turns=0`，字幕仍在 |
| G08 | 动作库 403 降级重建（去 `action`） | `engine.py:321-335` | `biz/live` | 产生降级事件，开播仍成功 |
| G09 | 开场白可用音频文件优先于 TTS | `engine.py:352-365` | `engine/greeting` | 配置有文件时走文件播放，TTS 0 次 |
| G10 | 播放歌单音频不经 TTS（直接 PCM 喂流） | `engine.py:1857-1931` | `biz/song` | ffmpeg 解码 → 24k PCM，TTS 调用 0 次 |
| G11 | 歌词 `.lrc.json` / `.txt` 回退 | `engine.py:1857-1931` | `biz/song` | 两种输入均出字幕 |
| G12 | 歌词字幕优先级（歌词优先于口播字幕） | `engine.py:1857-1931` | `engine/subtitle` | 同时有歌词与口播时只显示歌词 |
| G13 | 唱歌时新歌切歌：`song_epoch++` 使旧喂流静默退出 | `engine.py:1849-1856` | `engine/feeder` | 切歌后旧 epoch 任务 ≤1 帧退出 |
| G14 | 随机表演：间隔/偏置可调、可开关 | `stage/index.html:813-818`、`engine.py` | `biz/choreo` | 间隔与偏置生效；关闭后 0 次 |
| G15 | 弹幕模式“主播模式”保存到配置 | `stage/index.html:805-812` | `biz/settings` | 保存后重启仍生效 |
| G16 | 字幕层开关（画面内字幕） | `stage/index.html:787-804` | `engine/subtitle` | 关闭后不再上报 type10 |
| G17 | 资产 90 天自动删除提示 | `stage/index.html:762-771`、`client.py:140` | `AssetSettings` | 列表显示剩余天数（按上传时间） |
| G18 | Vidu Key 只显示尾 8 位 | `stage/index.html:784` | `ViduKeySettings` | DOM/响应中无完整 key |
| G19 | 拖拽面板位置与宽度记忆 | `stage/index.html:1560,3123` | `uiPrefs` | 刷新后位置不变 |
| G20 | 右栏事件逐条追加，最多 N 条 | `stage/index.html:838-900` | `RightRail` | 上限后滚动丢弃最旧 |
| G21 | 停止动作与清空动作队列是两个接口 | `bridge.py:442-467` | `biz/action` | StopActions 与 ClearActions 行为可区分 |
| G22 | 打断（interrupt）同时终止 TTS 生产与喂流 | `engine.py:943-980` | `engine` | 打断后 1s 内无新音频帧 |
| G23 | `read_interval` 在 UI 以“秒”展示，配置以毫秒存储 | `stage/index.html:806-809` | `SettingsDrawer` | 3s ↔ 3000ms 双向一致 |
| G24 | 观众加入（patron join）转发欢迎 | `bridge.py:912-979` | `biz/patron` | join 触发一次欢迎 |
| G25 | `once_per_session` 欢迎去重 | `config.example.json:116-120` | `biz/patron` | 同场同人只欢迎一次 |
| G26 | 礼物名过滤（空名/超长截断） | `engine.py:145-167` | `biz/gift` | 空名丢弃，>20 字截断 |
| G27 | 口播 take 音频缓存在本地并可复用 | `koubo.py:391-450` | `biz/koubo` + `pkg/blob` | 同文本二次合成命中缓存 |
| G28 | 口播工程并发 TTS 锁（避免爆显存） | `koubo.py:391-450` | `biz/koubo` | 并发 2 请求串行，第二个等待 |
| G29 | 设置 `reload` 与 `save` 分离 | `bridge.py:828-877` | `biz/settings` | Save 不自动 reload；Reload 有审计 |
| G30 | 停止后保留最后一帧/字幕，不黑屏 | `stage/index.html:430-495` | `VideoStage` | `ended` 后画面保留，状态改为“已下播” |

### 2.6 可验收的边界定义

- **“1:1”定义**：字段名、状态值、路由语义、默认配置值、错误语义对齐；**不要求**像素级 UI、不要求 Python 内部函数结构、不要求 Gradio 协议。
- **UI 对等**：功能入口、文案含义、操作路径一致；视觉按 NovaTrader（§6.7）。像素差异不计入验收。
- **性能对等**：单场 1080p 预览、24k 喂流、100ms 分片；本机 i7/32GB 目标（§4.3.3）。

---

## 3. 后端架构

### 3.1 选型与对 HFC 的纠偏

| 维度 | 采用 | 理由 / 反证 |
|---|---|---|
| 语言 | Go 1.26.x | 与 HFC `go.mod:4` 对齐；本机 Windows 需确认 toolchain 可用（⏳V1） |
| 框架 | Kratos v2 | HFC `go.mod` 依赖；transport 用 HTTP + 自定义 WS |
| ORM | Ent | HFC 76 schema；本方案只建必需实体（§7） |
| DI | Wire | **只给 `cmd/dh` 一条 wire**（HFC 也只有 admin 一套 wire，media 没有） |
| 契约 | Proto + Buf | 75 个 proto（B34），按域拆文件 |
| 队列 | Asynq | 用于**可重试的非实时任务**，不用于喂流（§3.6） |
| 缓存/队列后端 | **Redis 必选** | Asynq 与幂等键都依赖 Redis；HFC `conf/data.yaml` 已配 Redis |
| 事件 | v1 进程内 bus + 落库 | NATS 留到多实例；HFC 虽有 NATS 但单实例无必要 |
| gRPC | **不启用** | HFC 全仓无 `RegisterGRPCServer`（B36）；本机单进程 HTTP+WS 足够 |

**不 vendor HFC**：`apps/server/pkg/storage` 因 `.gitignore:16-19` 被忽略且从未提交（B35），admin 无法构建；直接派生会把缺口继承过来。本方案只借用其**分层形状**与依赖版本。

### 3.2 目录骨架（目标形态，本阶段不创建）

```text
digital-human/
├─ api/
│  ├─ live/v1/            live.proto, danmaku.proto, action.proto, event.proto
│  ├─ media/v1/           tts.proto, koubo.proto, song.proto, refs.proto
│  ├─ config/v1/          settings.proto, persona.proto, avatar.proto, vidukey.proto
│  ├─ ops/v1/             choreo.proto, patron.proto, gift.proto, record.proto
│  ├─ rtc/v1/             rtc.proto
│  └─ gen/go/             buf 生成（不手改）
├─ cmd/dh/main.go         wire 组装 + 启动
├─ internal/
│  ├─ conf/               config.go（yaml → struct，含默认值表）
│  ├─ server/
│  │  ├─ http.go          Kratos HTTP server + 路由注册
│  │  ├─ ws_events.go     /ws/v1/events（HandleFunc 挂载，先鉴权后升级）
│  │  ├─ middleware_auth.go
│  │  ├─ middleware_origin.go
│  │  └─ healthz.go       /healthz /metrics（仅本机）
│  ├─ service/            薄层：proto ↔ biz 参数转换（参考 HFC task_service.go:19-23）
│  ├─ biz/
│  │  ├─ live/            start/stop/status/transcript
│  │  ├─ danmaku/         ingest/t0/t2/batch/vip
│  │  ├─ action/          优先级/冷却/队列
│  │  ├─ gift/            合并/贵宾礼
│  │  ├─ choreo/          编排执行器
│  │  ├─ koubo/           口播工程
│  │  ├─ song/            点播/歌词
│  │  ├─ tts/             边车编排（队列/取消/熔断）
│  │  ├─ chat/            LLM 流式/分句
│  │  ├─ persona|avatar|assets|vidukey|patron|settings|record|replay|billing
│  │  └─ rtc/             token 生成/到期策略
│  ├─ data/
│  │  ├─ data.go          Ent client + Redis + Blob
│  │  ├─ ent/schema/*.go  §7
│  │  └─ repo/*.go        只在 data 层出现 Ent API
│  └─ engine/
│     ├─ live_engine.go   会话对象（**一进程一活跃场**）
│     ├─ vidu_http.go     端点 + 错误码映射
│     ├─ vidu_ws.go       信令类型 + 单写者
│     ├─ feeder.go        48B/ms 节奏
│     ├─ ownership.go     喂流权状态机
│     ├─ watchdog.go      75s/6 次/音频守卫
│     ├─ subtitle.go      type10/歌词
│     └─ greeting.go
├─ pkg/
│  ├─ eventbus/           进程内订阅 + envelope
│  ├─ audio/              PCM/WAV/重采样/ffmpeg 调用
│  ├─ blob/               本地文件存储（可替换 S3）
│  ├─ secrets/            DPAPI / AES-GCM
│  ├─ logx/               结构化日志 + 脱敏
│  └─ metrics/            Prometheus 注册
├─ third_party/omnivoice-sidecar/   Python 边车（独立进程，独立许可边界）
├─ configs/config.yaml + config.local.yaml
├─ deploy/docker-compose.yml        PG/Redis（不是应用容器）
├─ test/{acceptance,fault,soak}/     验收脚本与故障注入
└─ web/                             Quasar SPA
```

**关键工程约束**（吸取 HFC 教训）：

1. `.gitignore` **不得**使用未锚定的 `storage/`；本方案写 `/storage/`（仅根目录）并对 `pkg/blob` 显式 `!` 例外。
2. 所有 `internal/data` 之外的包**不得** import `ent`；`service` 不得 import `data`。
3. `cmd/dh` 是唯一 wire 入口；新增依赖必须过 `wire_gen`。

### 3.3 分层、依赖图与对象生命周期

```text
HTTP/WS  →  server  →  service  →  biz  ─┬→ data(repo) → Ent/Redis/Blob
                                          ├→ engine(ports) → Vidu HTTP/WS, TTS sidecar, ffmpeg
                                          └→ eventbus → WS 订阅者 + SessionEvent 落库
```

- **service**：只做参数校验与 proto 映射，不写业务分支（对齐 HFC `task_service.go:19-23` 的薄层约定）。
- **biz**：纯业务，依赖接口而非具体客户端；对 engine 通过 `LiveController` 接口注入，便于 fake。
- **data**：唯一持有 Ent client；仓储方法返回领域结构，不泄漏 `ent.*`。
- **engine**：实时域，**每场一个 `LiveSession` 实例**，由 `SessionManager` 持有：
  ```go
  type SessionManager struct { mu sync.Mutex; cur *LiveSession; max int } // max=1（§8）
  ```
  所有实时状态（owner、epoch、队列、t0）都在 `LiveSession` 内，**禁止包级全局变量**（蓝本的 `engine` 单例问题，`engine.py:1-40` 风格）。
- **并发边界**：engine 内部用 goroutine + channel，**不用全局锁**；OmniVoice 串行只锁在边车（§5.2）。

### 3.4 Transport 选择

| 通道 | 决策 | 说明 |
|---|---|---|
| 业务 API | **HTTP/1.1 JSON**（Kratos `http.Server`） | 本机单用户；与前端 `fetch`/`axios` 简单 |
| 实时事件 | **WebSocket `/ws/v1/events`** | 用 Kratos `HandleFunc` 挂载（同 HFC media `rest.go:90`） |
| gRPC | **不启用** | 无跨进程调用方；减少 buf/proto 生成面与调试成本 |
| Vidu 上行音频 | **WS 二进制帧**（不是 gRPC） | 协议固定（§4.1） |
| TTS 边车 | **HTTP/1.1 + 二进制 wav**（§5.2） | 比 gRPC 更易实现/调试；单机无性能压力 |
| 指标 | `/metrics`（Prometheus 文本） | 仅 127.0.0.1 暴露 |

WS 握手必须：① 先过鉴权中间件；② 校验 `Origin`（局域网模式）；③ 用**一次性 ticket**，不接受 URL 明文长期 token（§3.9）。

### 3.5 事件总线与事件信封

v1 用进程内 `eventbus`（fan-out + 环形缓冲），同时**每条事件写 `session_events`**：

```json
{
  "event_id": "evt_01J...",        // ULID，全局唯一
  "session_id": "ses_01J...",
  "seq": 42,                       // 单场单调递增，落库唯一约束 (session_id, seq)
  "type": "feed.owner_changed",
  "ts_ms": 1730000000123,
  "epoch": 7,                      // 喂流/歌曲代际
  "payload": { }
}
```

- **断线补齐**：前端带 `after_seq` 重连，服务端回放 `seq > after_seq`（最多 500 条），再转入实时。
- **去重**：前端以 `event_id` 去重；服务端发布前对同一 `event_id` 幂等。
- **seq 语义（v2.1 明确）**：`seq` **按 `session_id` 单调递增**，唯一约束 `(session_id, seq)`；换场次从 1 重新计数。实现上是 `eventbus.seqBySession`，不是全局自增序号，避免跨场次串号。
- **epoch 语义（v2.1 明确）**：`epoch` 表示喂流/歌曲代际，切歌、重连或重新持有喂流权时 `+1`；前端观察到 `epoch` 变化即清空旧喂流相关 UI 状态。`session_id + epoch` 共同决定事件归属，`request_id` 只用于单次请求链路追踪。
- **REST 与 WS 同源（v2.1 修正）**：`GET /api/v1/live/events`（`ListEvents`）与 WS 广播共用同一 sink。`eventbus.Bus.SetAppender(sink)` 让所有 `Publish`/`PublishScoped` 都写入同一 sink；此前 song/refs/action 仅进 bus 不进 sink，属真实不同源缺陷，已修复并由 `TestRESTListAndWSBroadcastShareSource` 锁定。
- **降级期补写（最小 outbox，v2.1 新增）**：`SwitchableSink` 启动写内存；PG 接上后切换并一次性补写内存快照（按 seq 顺序，唯一约束冲突视为已写）。补写失败即停止并保留内存，重启后以 DB 为准；内存上限 4096 条，超出部分不补写，是降级期已知丢失边界。
- **契约单一事实来源（v2.1 新增）**：`web/src/services/api/events.contract.json` 为唯一事实来源，Go `pkg/eventcontract` 与前端 `contract.ts` 各自测试校验字段集合。
- **与蓝本区别**：蓝本是 `status(since=字幕下标)` 轮询（B25），**没有事件序号**；本方案把“字幕游标”和“事件游标”拆成两个字段（`transcript_since` / `event_seq`），这是必要的架构升级，不改变 UI 行为。
- **NATS**：仅当出现“多进程/多实例”时才替换实现；接口 `Publisher/Subscriber` 不变（§8）。

### 3.6 Asynq 的使用边界

| 场景 | 是否用 Asynq | 理由 |
|---|---|---|
| 喂流/信令/动作/弹幕（毫秒级） | **否** | 走 engine goroutine；队列延迟不可控 |
| TTS 预合成（离线/口播工程） | **是**（`critical` 队列） | 可重试、可观测；不与实时喂流抢同一队列 |
| 录播拉取/转码 | **是**（`default`） | 长任务、可重试 |
| 歌词打轴（离线） | **是**（`low`） | 分钟级 |
| 复盘导出/统计聚合 | **是**（`low`） | 批量 |
| 会话崩溃修复扫描 | **否** | 启动时同步执行（§3.10） |

> Redis 因此**必选**（Asynq + 幂等键 + ticket）。HFC `conf/data.yaml` 已含 Redis；本方案不引入 NATS 作为 v1 依赖。

### 3.7 关键接口清单（proto 级）

> 统一前缀 `/api/v1`；为兼容采集器保留 `/api/danmaku` 与 `/api/health` 两个**旧路径别名**。
> `WatchEvents` 不是 RPC，是 WS 升级端点；proto 里只定义 `Event` 消息。

| 服务 | RPC / 端点 | 请求要点 | 返回要点 |
|---|---|---|---|
| LiveService | `StartLive` | `persona_id?`, `avatar_id?`, `greeting?` | `session_id`, `phase=starting/live`, `live_id?` |
| | `StopLive` | `session_id?`, `reason` | `phase=ended`, `recording_state` |
| | `GetLiveStatus` | `session_id?`, `transcript_since`, `event_seq` | phase/ws/feed/dan/acts/choreo/stats/rtc/captions |
| | `Say` | `text`, `mode=sentence/whole`, `source` | `utterance_id` |
| | `SendCommand` | `text`（等同旧 `/api/cmd`） | `accepted` |
| | `Interrupt` / `ClearTranscript` / `StopActions` / `ClearActions` | — | `ok`, `cleared` |
| | `ResetLlm` | `session_id?` | `cleared_turns` |
| | `ListEvents`（复盘用，HTTP） | `session_id`, `after_seq`, `limit` | `events[]` |
| DanmakuService | `Ingest`（`POST /api/danmaku` 别名） | `kind=chat/gift/join`, `user`, `text`, `diamond` | **永远 200**：`{ok,reason}` |
| | `SetMode` / `Confirm` / `Dismiss` | `off/confirm/auto`；`pending_id` | `changed`, `pending[]` |
| | `ListPending` | — | `items[]` |
| TtsService | `ListVoices` / `SetVoice` / `Preview` | `voice_id`, `text<=200` | `audio_url`/`pcm_seconds` |
| | `ListAudio` / `UploadAudio` / `ImportFolder` / `DeleteAudio` | 文件/目录 | `items[]` |
| KouboService | 14 个：`GetState` `Split` `SplitSegment` `MergeSegment` `SegmentSplit` `TtsSegment` `TtsAll` `SelectTake` `SetSegmentAction` `SetAllActions` `Play` `PlayAll` `Stop` `GetAudio` | `project_id`, `segment_id` | 工程/分段/takes |
| ChoreoService | `Get` / `Save` / `Start` / `Stop` / `RunOnce` | groups/steps/interval/loop/random | `running` |
| PatronService | `List` / `Save` / `Join` / `Welcome` / `Delete` | 名单 | `items[]` |
| PersonaService | `List` / `Select` / `Save` / `Delete` | persona | `active_id`, `restart_required` |
| ActionService | `ListLibraries` / `CreateLibrary` / `DeleteLibrary` / `ListActions` / `AddAction` / `UpdateAction` / `DeleteAction` | `library_id`, `name`, `instruction` | `items[]` |
| AvatarService | `List` / `Upload` / `Use` / `Delete` | 图片 | `avatar_id` |
| SettingsService | `Get` / `Save` / `Reload` | 12 组字段 | `saved`, `reload_required[]` |
| ViduKeyService | `List` / `Add` / `Activate` / `Delete` / `Credits` | `key` | 仅尾 8 位；`credits` |
| RecordService | `Get` / `SetEnabled` / `Fetch` | `session_id` | `url`, `state` |
| RtcService | `GetRtcToken`（`GET /api/v1/live/rtc-token`，v2.1 已实现；早期名为 `GetViewerToken`） | `session_id` | `mode=managed/unconfigured`, `provider`, `token`, `expires_at`(ms), `room_id`, `user_id`, `ok` |
| EventService | `GET /ws/v1/events` | `after_seq`, ticket | `Event` 流（WS） |
| Health | `GET /healthz` / `GET /metrics` | — | 字段见 §4.9 |

### 3.8 场次状态机与重复开播语义

```text
idle ──StartLive──▶ starting ──create ok + conn_init ok──▶ live
  ▲                   │                                      │
  │                   └──StopLive / create fail──▶ stopping ──┘
  │                                                        │
  └────────────────── ended ◀── finalize(0.4s) ◀───────────┘

live ──StartLive（重复开播）──▶ stop("reconnect") ──▶ creating ──▶ live(new)
```

- **重复开播 = 蓝本语义**：`engine.py:305-306` 在 `phase=="live"` 时先 `await self.stop("reconnect")`，再走创建；Go 必须复刻，**不能返回 409**。
- `starting` 阶段再开播：取消进行中的创建（context cancel），清理 `live_id`，再创建。
- **收播幂等**：`stop` 在 `idle/ended` 返回 `ok=true, note=没有进行中的会话`。
- **强制中断**：`Interrupt` 只影响当前语音/喂流，不改变 phase。
- **状态值**：`idle|starting|live|stopping|ended|error`；`error` 带 `error_code/message`，下一次 Start 可覆盖。
- **引擎模式（fail-closed，v2.1 落地）**：`engine.mode=real`（默认）下缺 `DH_VIDU_KEY` / `DH_OMNIVOICE_ADDR` 或真实适配器未装配时，`StartLive` 直接返回 **503 `engine_not_configured`**，不得退回 fake 成功；只有显式 `engine.mode=dev` 才装配 `FakeController`，并由日志与 `/healthz.capabilities` 标注。

### 3.9 全站鉴权与密钥管理（新增）

**威胁模型**：本机单用户是主场景；但蓝本默认监听 `0.0.0.0:8795` 且**无任何鉴权**（B38），任何同网段设备可开播/删素材/读 Key。Go 版必须默认收口。

**三种运行模式**（配置 `security.mode`）：

| 模式 | 监听 | 鉴权 | 适用 |
|---|---|---|---|
| `local`（默认） | `127.0.0.1:8795` | 无 Bearer，但**必须**源地址是 loopback；WS 仍需 ticket | 单人本机 |
| `lan` | `0.0.0.0:8795` | Bearer（管理员密码派生）+ Origin 白名单 + HTTPS/WSS | 局域网第二屏 |
| `collector` | 只对采集器网段开放 `/api/danmaku` | HMAC-SHA256（`X-Dh-Ts` + `X-Dh-Sign`，5 分钟窗口） | 弹幕机 |

**写接口清单（全部要求鉴权，`local` 模式下按源地址放行）**：
`StartLive/StopLive/Say/SendCommand/Interrupt/Clear*/StopActions/ResetLlm`、`Danmaku.SetMode/Confirm/Dismiss`、`Tts.*` 写、`Koubo.*` 写、`Choreo.*` 写、`Patron.*` 写、`Persona.*` 写、`Action.*` 写、`Avatar.*` 写、`Settings.Save/Reload`、`ViduKey.*`、`Record.*` 写、`Rtc.GetViewerToken`。
**读接口**：`GetLiveStatus/ListEvents` 同样鉴权（含 Key 尾号、额度、观众名单）。

**WS 握手**：
1. 前端先 `POST /api/v1/ws-ticket`（Bearer/loopback），服务端发一次性 ticket（30–60s，Redis `SETNX`，用后即焚）；
2. WS 升级使用 `Sec-WebSocket-Protocol: dh.v1, ticket.<ticket>`（**不放 URL query**，避免日志泄漏）；
3. 握手校验 `Origin`（`lan` 模式）、ticket、会话绑定；
4. 断线重连带 `after_seq`，ticket 可复用同一会话的短期凭证（5 分钟）。

**密钥存储**：

| 密钥 | 存储 | 读取 |
|---|---|---|
| Vidu API Key（多把） | **Windows DPAPI**（`CryptProtectData`，当前用户） | 仅 `biz/vidukey` 解密，内存中 `[]byte` |
| 当前激活 key 指针 | PG `vidu_keys.is_active` | 启动加载 |
| ARTC `app_key` / TRTC `secret_key` | DPAPI 密文文件（`configs/secrets.bin`） | `pkg/secrets` |
| LLM `key_file` | 沿用文件（`config.example.json:25`），权限收紧 | `biz/chat` |
| 管理员密码 | Argon2id 哈希（PG `settings`） | 登录换 Bearer |
| 采集器 HMAC secret | DPAPI | 中间件 |

**硬性规则**：
- 完整 key **永不**出现在响应、日志、事件、`config_snapshot`；统一 `maskTail8`（对齐 `stage/index.html:784`）。
- 日志中间件对 `authorization`、`api_key`、`token`、`secret` 字段做结构化脱敏。
- 导出/备份不含 DPAPI 密文以外的明文；`config_snapshot` 只存非敏感字段（§7）。

### 3.10 幂等、崩溃恢复与对账

**幂等**：
- 所有写接口接受 `Idempotency-Key`（可选）；`StartLive/StopLive/Say/Danmaku.Ingest` 必选。
- Redis 键 `idem:{route}:{key}` → 结果快照，TTL 24h；重复请求返回同一结果（含 `session_id`）。
- `SessionEvent` 唯一约束 `(session_id, seq)`；`event_id` 全局唯一。

**崩溃恢复（启动扫描）**：
1. 查 PG `live_sessions` 中 `phase ∈ {starting, live, stopping}` 且 `updated_at < now-60s`；
2. 标 `phase=error, error_code=crash_recovered`，写 `session_crashes`；
3. 对 managed 会话尝试 `GET /live/v1/lives/{id}` 对账：仍 live → 尝试发 `type5 hangup`（若可建 WS），失败则记录“云端可能仍计费”；
4. **绝不自动恢复喂流**（音频上下文已丢，恢复会叠音）；
5. 清理临时文件、释放 blob 租约、重置 owner 与 epoch；
6. 若 `recording_state=pending`，投递 Asynq 任务补拉录播。

**对账任务**（Asynq `low`，每小时）：`list_lives` 最近 24h vs 本地 `live_sessions`，补齐缺失 `live_id`、修正时长与额度消耗。

### 3.11 可观测性

**日志**（JSON，`logx`）：每条带 `ts, level, msg, request_id, session_id, event_id, conn_id, feed_epoch, route`；P0 事件（重连、抢占、超时、崩溃）单独 `warn/error`。

**指标**（Prometheus）：

| 指标 | 类型 | 用途 |
|---|---|---|
| `dh_feed_lag_ms` | histogram | 漂移（p50/p99/max） |
| `dh_feed_send_ms` | histogram | 单帧写耗时 |
| `dh_feed_bytes_total` | counter | 上行字节 |
| `dh_feed_owner_state` | gauge | 0 idle/1 acquiring/2 owned/3 releasing |
| `dh_feed_force_cancel_total` | counter | 强制抢占 |
| `dh_ws_state` / `dh_ws_reconnect_total` | gauge/counter | 信令健康 |
| `dh_ws_last_recv_age_ms` | gauge | 断流预警 |
| `dh_tts_queue_depth` / `dh_tts_fail_total` / `dh_tts_restart_total` | gauge/counter | 边车健康 |
| `dh_danmaku_total{dropped=...}` | counter | 弹幕风控 |
| `dh_action_queue_depth` | gauge | 动作积压 |
| `dh_credits_balance` / `dh_credits_delta` | gauge | 成本 |
| `dh_rtc_token_expire_seconds` | gauge | 到期预警 |
| `dh_event_publish_total` / `dh_event_persist_ms` | counter/histogram | 事件链路 |

**告警阈值（本机）**：`feed_lag_ms p99 > 300` 持续 30s；`ws_state=0` 持续 30s；`tts_queue_depth>6` 持续 60s；`credits_balance < 45`（对齐 `stage/index.html:817`）；`rtc_token_expire < 15min`。

**健康检查**：字段与蓝本对齐（phase/ws/stats/dan/acts/choreo/credits/tts），新增 `feed` 子对象与 `recording` 状态；`/metrics` 仅 loopback。

---

## 4. 实时音频与信令（最核心）

### 4.1 Vidu WS 协议事实（以蓝本为准）

| 项 | 事实 | 证据 |
|---|---|---|
| URL | `wss://{host}/live/v1/external-lives/{live_id}/stream?conn_id=...` | `signaling.py:80-84` |
| 鉴权 | 服务端场景 `Authorization: Token {api_key}`；浏览器才用 `client_secret` query | `signaling.py:82-83` |
| 帧 | 文本帧 = JSON 信封；二进制帧 = 裸 PCM（s16le/24k/mono） | `signaling.py:188-194` |
| 信封 | `{type,live_id,conn_id,seq_id,payload}` | `signaling.py:50-52` |
| 类型 | `1 conn_init` / `2 ack` / `5 hangup` / `6 force_hangup` / `7 interrupt` / `9 input_text` / `10 output_text` / `11 prompt_operation` / `12 prompt_op_ack` / `14 prompt_op_err`；本地补 `-1 ws_closed` | `signaling.py:39-44,90-124` |
| 心跳 | 库级 `ping_interval=10`、`ping_timeout=20`、`close_timeout=5` | `signaling.py:84-86` |
| 就绪 | `conn_init_ack.success=true`；`NOT_READY` 重发（**不重连**）；其他错误直接失败 | `signaling.py:126-148` |
| 重试 | 15 次 × 2.0s | `signaling.py:126` |
| 关流 | 对端关闭时 `async for` 正常结束，必须补 `-1` 事件 | `signaling.py:90-124` |
| 参考图 | 成功**不回 ack**，失败 `type=14`；**不去重**、不重发 | `signaling.py:173-183` |
| 音频 | 24kHz、s16le、单声道；`48B = 1ms` | `tts/audio.py:16-17` |

> 注意：蓝本注释写“服务端约每 5s 发 ping、15s 未回 pong 断开”，但**实际库参数是 10/20**（`signaling.py:84-86`）。以代码为准，注释不作为验收依据。

### 4.2 Go WS 客户端设计

- 库：`github.com/coder/websocket`（HFC `go.mod` 已用 v1.8.15）。理由：context 友好、支持读超时、无隐式全局状态；`gorilla/websocket` 也可，但需要自己管理并发写。
- **单写者**：所有 `Write` 只在一个 goroutine（`writer`）执行；业务通过 channel 投递，避免并发写 panic。
- **压缩关闭**（音频不需要，压了反而增加 CPU/延迟）。
- **读循环**独立 goroutine：每次收到任何帧刷新 `lastRecv`；`type=2` 只用于就绪；`type=6` 触发 session 结束；未知类型记 `unknown_type` 计数。
- **写超时**：单帧 `context.WithTimeout(3s)`；超时由上层终止本轮 utterance（同 F14）。
- **心跳**：`coder/websocket` 没有自动 ping；需自建 10s ticker 发 ping、20s 未见 pong/任何帧则关闭并进入重连流程（与 B3 对齐）。
- **关闭取证**：记录 `close_status.code/reason`、谁先关（本地/对端/超时），写入事件 `ws.closed`。
- **未就绪保护**：`initOK=false` 时音频/type7/type11 **直接丢弃并计数**（蓝本服务端会丢弃，客户端不应假装成功）。

### 4.3 喂流节奏实现（48B/ms）

#### 4.3.1 公式

```text
BYTES_PER_MS = 48
chunk_ms     = 100（默认；可选 20）
chunk_bytes  = 4800（100ms）
utterance_t0 = 开始喂第一帧前的单调时钟（per utterance）
played_ms   += len(frame) / 48
deadline     = utterance_t0 + played_ms/1000
delay        = deadline - now
if delay > 0: sleep(delay)
else: 立即发送（不跳帧、不补发）
```

证据：`engine.py:1703-1761`；分片函数 `tts/audio.py:75-83`。

**关键点**：`t0` 是**每轮语音独立**的绝对时基，不是全局时钟；静默期不喂流，因此不能用“全局连续流”假设。

#### 4.3.2 Windows 定时器精度（新增设计）

Go 的 `time.Ticker`/`time.Sleep` 在 Windows 上的实际粒度通常 **0.5–15.6ms**，且受系统负载影响；直接 `Sleep(1ms)` 会累积误差。方案：

1. **分片 100ms**：单次误差被 100ms 预算吸收（p99 抖动 ≤20ms 时 8h 累计 <100ms）。
2. **绝对时基补偿**：每帧按 `deadline - now` 计算，误差不累积（蓝本同款）。
3. **混合等待**：`delay > 20ms` 用 `time.Sleep(delay-15ms)`，剩余用 `runtime.Gosched` + 忙等（只在最后 15ms）；避免长时间忙等烧 CPU。
4. **可选提升定时器精度**：Windows 下调用 `timeBeginPeriod(1)`（`golang.org/x/sys/windows`），进程退出 `timeEndPeriod(1)`；是否启用由实测决定，**默认不启用**（避免全局副作用）。
5. **20ms 分片仅在实测通过后启用**：p99 抖动 ≤5ms 且 CPU <3% 才允许；否则保持 100ms。

⏳ **V2 验证**：目标机 8h 实测，记录 `dh_feed_lag_ms` 的 p50/p99/max 与累计 deadline 偏移；门禁见 §4.10。

#### 4.3.3 8h 漂移测试（可测）

- 环境：目标 Windows 机器（i7/32GB），无其他负载；使用 fake Vidu WS（本地 server 只读不写）。
- 方法：以 100ms 分片喂 8h 合成 PCM；每帧记录 `deadline - now`。
- 通过标准：① 累计 deadline 偏移（首帧 t0 与末帧 `t0+played_ms` 的墙钟差）**≤100ms**；② p99 单帧抖动 **≤20ms**；③ 丢帧 0；④ 内存增长 <50MB。
- 失败动作：降到 20ms 分片 + `timeBeginPeriod(1)`，重测；仍失败则改“音频块预生成 + 系统高精度等待”方案（⏳V3）。

### 4.4 喂流权状态机（重写）

```text
                 Acquire(gen++)
   Idle ─────────────────────────────▶ Acquiring
     ▲                                   │  CAS 成功
     │                                   ▼
     │                                Owned ──Release──▶ Releasing ──▶ Idle
     │                                   │
     │         Preempt(gen++)            │ 强制收回（4 轮后）
     └───────────────────────────────────┘
```

| 状态 | 允许动作 | 不变量 |
|---|---|---|
| `Idle` | Acquire | `owner=nil`，无喂流 goroutine |
| `Acquiring` | 等待/取消 | 只有一个 waiter 能赢；`gen` 单调 |
| `Owned` | Feed/Release/Preempt | 同一 `gen` 才能写 WS；旧 `gen` 写入前检查并退出 |
| `Releasing` | 仅清理 | 新 Acquire 需等清理完成（≤300ms） |

**抢占算法（复刻蓝本）**：

1. 取当前 owner；若无 → 直接 Acquire。
2. `respect_song`：请求者是聊天/弹幕且 owner 是歌曲 → 进入等待（`wait_only`）。
3. 非强制路径最多 **4 轮**：请求取消 → 等旧 owner 退出 **≤1.5s**。
4. 4 轮未退出 → **强制收回**：`gen++`、发 `type7 interrupt`、`sleep 250ms`、再 `gen++` 并接管。
5. `wait_only` 的总等待上限 **25s**；到点后若旧 owner 最近 5s 内**仍在喂流**则继续等，否则强制收回（歌曲/聊天持有者例外）。
6. 释放时 `gen++`，确保任何在途帧失效。

证据：`engine.py:831-938`（4 轮、1.5s、25s、5s、type7+0.25s）。

**与蓝本一致但需显式写入的语义**：

- 抢占后 **300ms 内不得发送任何旧音频帧**（否则叠音）。
- 歌曲播放中，打字聊天**不抢占**（`respect_song=True`）；但“打断”按钮可强制。
- 抢占事件必须落库：`feed.owner_changed{from,to,reason,gen}`。

### 4.5 背压、退避与熔断参数

| 场景 | 参数 | 动作 |
|---|---|---|
| 边车 TTS 队列 | 深度 8（蓝本） | 满则生产者阻塞，**不丢句** |
| 喂流 chunk 通道 | cap=4（≈400ms） | 满则生产者等待；不丢帧 |
| 单帧写超时 | 3s | 终止本轮 utterance，取消上游，释放 owner |
| 连续写超时 | 2 次/10s | 熔断 30s：暂停新 utterance，保留连接 |
| WS 断流 | 75s + 0.55x | 原地重连 |
| 重连退避 | ≥20s，最多 6 次 | 6 次后 `ws_reconnect_exhausted`，进入 `error` |
| 重连后 stall 抑制 | 60s（距上次重连） | 抑制期内不因速率低再次判 stall（`engine.py:456-457`） |
| 音频守卫 | 5s tick；stale>30s 且 hold>30s | 取消当前 TTS 任务 + type7（歌曲豁免） |
| Loop 阻塞 | >1.2s | 记录 warn（10s 节流），不自动重启进程 |
| 闲置 / 硬上限 | 300s / 1800s | 自动收播 |
| 边车连续失败 | 3 次/60s | 熔断 60s，健康检查失败 → 重启边车（最多 3 次/小时） |

**明确不做**：不跳帧、不重发、不做“1s 后重新同步”、不做固定 0.25s 丢弃（蓝本均无；`engine.py:1733-1750`）。

### 4.6 重连与启动/停止时序

**WS 重连**（不重建会话）：

```text
stall 判定 → close(old) → connect(new WS, 同 live_id/conn_id)
→ conn_init 6 次 × 1.5s → 恢复 owner/epoch（音频上下文丢失则不自动续播）
```

- `conn_init` 失败 → 标 `ws_error`，UI 提示“会话在但信令不可用”；**不自动新建会话**（避免重复计费）。
- 重连成功 → 发布 `ws.reconnected`，前端重建 RTC 播放器（token 可能仍有效）。

**开播时序**（复刻 `engine.py:303-365`）：

```text
StartLive → (live? stop("reconnect")) → create(120s) → 保存 live_id
→ WS connect → conn_init → token_expire_at 记录
→ phase=live → 播开场白（TTS 或文件）→ 启动 watchdog/credits/audio guard
```

**收播时序**（`engine.py:693-703`）：

```text
StopLive → hangup(type5) → sleep 0.4s → finalize
finalize: 取消任务 → owner release → song_epoch++ → 清缓冲 → 停轮询 → phase=ended
```

### 4.7 TTS 预取与取消语义（与边车配合）

- 逐句模式：LLM 每产出一句 → 入队（深度 8）→ 边车合成 → 返回 wav/PCM → 喂流。
- **预取**：当前句播放时，下一句已在边车合成（生产者不等待当前句结束）；蓝本即此结构（`engine.py:1568,1669`）。
- 整段模式：`Say(mode=whole)` 一次合成整段，只有 1 个 job。
- 取消：`Interrupt` / 抢占 / 收播 → `context cancel` + 边车 `POST /v1/jobs/{id}:cancel`；已返回但未播的 PCM 丢弃。
- 失败：单句失败跳过并记录（不阻塞后续）；连续 3 次失败触发边车熔断（§4.5）。

### 4.8 RTC 订阅、token 生成与过期

**事实**（`stage/index.html:1101-1188`、`engine.py:380-385`、`rtc_tokens.py`）：

- 播放端是浏览器：AliRTC（`artc`）与 TRTC（`trtc`）Web SDK；Agora 未支持。
- `managed` 模式：Vidu 返回 token 与 `token_expire_at`；**平台无刷新端点**，所以不能“续期”，只能提示/重连/重开会话。
- `customer` 模式：蓝本用 `rtc_tokens.py` 本地签发：
  - ARTC 支持三种形态：`authinfo_b64` / `concat_json` / `raw`（前端下拉选择）；
  - TRTC 为用户 `UserSig`（HMAC 派生，TTL **24h**）。
- **Go 版**：`biz/rtc` 用同一“规范化 payload → 三种编码”实现；算法以官方 SDK 文档为准，**用蓝本工具的输出做黑盒测试向量**（不复制实现，§9.3）。

**过期策略（新增，可验收）**：

| 时间点 | 行为 |
|---|---|
| 到期前 15 分钟 | 前端黄条 + `rtc_token_expiring` 事件 |
| 到期前 5 分钟 | 前端红条；`GetViewerToken` 重新签发（**仅 customer**） |
| 到期 | 播放器停止；自动重建 1 次（customer）；managed 提示“重开会话” |
| 重开 | 走 `StartLive` 重复开播语义（会中断当前语音） |

**实现状态（v2.1）**：RPC 为 `GetRtcToken`（`GET /api/v1/live/rtc-token`），配置 `rtc.mode=disabled|dev|managed`：

| `rtc.mode` | 实际行为 |
|---|---|
| `disabled`（默认） | HTTP 200 + `mode=unconfigured, ok=false`；前端显示“RTC 未配置”，不当作错误 |
| `dev` | 本地 HMAC 签发，密钥只从 `DH_RTC_API_KEY` / `DH_RTC_API_SECRET` 注入；每次签发轮换 nonce，前端按 `expires_at` 提前刷新 |
| `managed` | 需装配厂商签发器；未装配时仍返回 `unconfigured`（fail-closed） |

前端刷新失败保留旧凭据并显示降级提示，不静默清空播放器。

⏳ **V4**：managed token 是否可续、TRTC UserSig 是否与蓝本完全一致，需真机对拍（§4.10）。

### 4.9 健康字段对照（兼容蓝本）

```json
{
  "ok": true,
  "phase": "live",
  "live_id": "...",
  "ws": {"connected": true, "init_ok": true, "idle_send": 1.2, "idle_recv": 0.4},
  "stats": {"audio_seconds": 1234.5, "audio_bytes": 59256000},
  "dan": {"mode": "auto", "pending": 0, "dropped": 3, "t2_skipped": 1},
  "acts": {"queue": 0, "last": "wave"},
  "choreo": {"running": false},
  "credits": {"balance": 320, "delta": 12},
  "tts": {"queue": 0, "healthy": true},
  "feed": {"owner": "tts", "gen": 7, "lag_ms": 12, "late_max_ms": -40},
  "recording": {"state": "pending", "url": null}
}
```

证据：`bridge.py:340-352`、`engine.py:3006-3075`。`feed`/`recording` 为新增字段（🟡）。

### 4.10 库选择与取舍

| 需求 | 选择 | 备选 | 取舍 |
|---|---|---|---|
| WS 客户端 | `coder/websocket` | `gorilla/websocket` | coder 的 context/关闭语义更清晰；HFC 已在用 |
| HTTP 客户端 | `net/http` + `httpx` 风格封装 | `resty` | 少依赖；只需超时/重试/错误映射 |
| PCM/WAV | 标准库 + 自写重采样 | `go-audio` | 只处理 s16le 24k，自写更可控 |
| ffmpeg | 外部进程 | `ffmpeg-go` 绑定 | 蓝本即外部进程；避免 CGO |
| RTC | **无 Go SDK** | pion/mediasoup | 与蓝本播放端不兼容；不引入 |
| 定时器 | `time` + 可选 `timeBeginPeriod` | `golang.org/x/time/rate` | 节奏需绝对时基，不用 rate |

### 4.11 待验证清单（V1–V12，带责任与门禁）

| ID | 问题 | 验证方式 | 责任 | 截止 | 失败判据（kill criteria） |
|---|---|---|---|---|---|
| V1 | Windows 上 Go 1.26 toolchain / Kratos 构建 | 空项目 `go build` + `wire` | 后端 | M0 第 1 周 | 无法构建 → 降 Go 1.24/1.25 |
| V2 | 100ms 分片 8h 漂移 | §4.3.3 压测 | 后端 | M1 中 | 累计 >100ms → 启 20ms+高精度 |
| V3 | `timeBeginPeriod` 副作用 | 对照 A/B 压测 | 后端 | M1 中 | CPU 飙升 >5% → 不启用 |
| V4 | managed token 是否可续 | 真机会话观察 2h | 后端 | M1 末 | 不可续 → 只做提示 |
| V5 | TRTC UserSig 与蓝本一致 | 黑盒向量 20 组 | 后端 | M1 末 | 不一致 → 以官方文档重写并回归 |
| V6 | ARTC 三种 token 形态 | 真机入会 | 后端 | M1 末 | 任一形态失败 → 只支持可用形态 |
| V7 | Vidu WS 10/20 与真实服务端行为 | 真机 30min | 后端 | M0 末 | 频繁 1011 → 调整 ping/降级 |
| V8 | 参考图成功无 ack | 真机 10 次 | 后端 | M2 | 有 ack → 按实际实现，但禁止重发 |
| V9 | 边车并发=1 的显存/延迟 | 4090/3090 各 10 次 | TTS | M1 | OOM → 强制串行 + 队列 4 |
| V10 | 浏览器自动播放策略 | Chrome/Edge 实测 | 前端 | M2 | 必须手势 → 保留点击开启 |
| V11 | 局域网 HTTPS/WSS 证书 | 自签 + 信任 | 后端 | M3 | 无法信任 → 文档化手动导入 |
| V12 | 录像 URL 有效期 | 真机取 3 次 | 后端 | M2 | 短期过期 → 落盘代理缓存 |

---

## 5. 外部边界：TTS 边车与 Vidu 客户端

### 5.1 两种取舍

| 方案 | 做法 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| **A. 保留 Python OmniVoice 边车** | Go 通过自有 HTTP 契约调用；Python 进程独立部署/重启 | 保留克隆音色与现有模型；Gradio 不确定性隔离；许可边界清晰（独立进程） | 多一个进程与部署步骤；Windows 需管理子进程 | **推荐** |
| B. 纯 Go 调外部 TTS 服务 | Go 直接调云端 TTS（如火山/Azure） | 无本地 GPU、部署简单 | **丢失克隆音色**，与蓝本不等价；成本/延迟/合规新增 | 不推荐（仅作为 B 计划） |
| C. Go 重写 OmniVoice 推理 | 用 Go 绑定模型 | 单进程 | 需 CGO/ONNX 重写，工作量大、音质不确定 | v1 不做 |

推荐 A 的核心理由：TTS 是**唯一无法在 Go 侧等价实现**的能力（`omnivoice.py:3-13` 的 Gradio 会话语义、`_lock/_loop_lock` 的串行假设 `:88-89`），保留边车是成本最低且行为最接近的选择。

### 5.2 OmniVoice 边车契约 v2（新增，必须落地）

> 该契约是**新写的**，不沿用蓝本的 Gradio 内部调用；边车只暴露 HTTP，Go 不感知 Gradio。

**传输**：HTTP/1.1，仅 `127.0.0.1:9880`；请求头 `X-Sidecar-Token`（启动时由 Go 生成并注入环境变量）。
**音频格式**：`audio/wav`（PCM s16le / 24kHz / mono），避免 Go 侧再解码；若边车只能产出 mp3，则回退 ffmpeg（与蓝本一致）。

| 端点 | 方法 | 说明 |
|---|---|---|
| `/v1/health` | GET | `{ok, model, device, queue, vram_mb, version}`；模型未加载时 `ok=false` |
| `/v1/voices` | GET | `[{voice_id,name,lang,preview_url?}]` |
| `/v1/jobs` | POST | `{text, voice_id, speed?, format:"wav", request_id}` → `202 {job_id, queue_pos}`；文本为空/超 2000 字 → `422` |
| `/v1/jobs/{id}` | GET | `{state: queued/running/done/failed/cancelled, error_code?, duration_ms?}` |
| `/v1/jobs/{id}/audio` | GET | 成功返回 `audio/wav`；未完成 `409`；失败 `410` |
| `/v1/jobs/{id}:cancel` | POST | **总是 200**，返回 `{cancelled:bool}`（见取消语义） |
| `/v1/preview` | POST | 同步预览，`text<=200`，10s 超时，返回 wav |

**并发 / 队列**：
- 合成 worker **固定 1**（蓝本全局锁的等价物，`omnivoice.py:88-89`）。
- 排队上限 **8**（对齐 `engine.py:1568`）；超过返回 `429` + `Retry-After: 2`。
- Go 对 `429` **阻塞重试**（不丢句），最多等到 30s；仍失败则该句跳过。

**超时**：
- Go 端总 deadline **120s**；边车内部 **110s** 终止并返回 `failed{error_code=timeout}`。
- 蓝本 `timeout_sec=120` 只保存未真正生效（`omnivoice.py:__init__` 赋值、`predict` 未使用），这是**已知缺陷**，Go 版必须真正执行。

**取消语义（必须写清）**：
- 取消是**逻辑取消**：`queued` 立即出队并 `cancelled`；`running` 标记 `cancel_requested`，等当前推理结束（可能数秒）后丢弃结果。
- 不承诺 GPU 抢占；Go 侧收到 `cancelled` 即释放 utterance，不等待音频。
- 取消后 **不得** 把结果写回缓存。

**失败语义**：

| 情况 | 边车 | Go 行为 |
|---|---|---|
| 文本过长 | `422 text_too_long` | 切分后重试（>500 字按句切） |
| 模型未加载 | `503 model_loading` | 等待健康（最多 60s），期间 UI 显示“TTS 启动中” |
| 推理异常 | `500 synth_failed` | 该句跳过，`dh_tts_fail_total++` |
| 连续 3 次/60s | — | 熔断 60s + 健康探测；仍失败则重启边车（3 次/小时上限） |
| 边车进程退出 | — | 监督器 1s/5s/30s 退避重启；不自动重开直播 |

**缓存**：键 `sha256(voice_id|text|format|sample_rate)`；LRU 1GB；命中直接返回 wav；`Interrupt` 不删缓存。

**健康与启动**：Go 启动时探测 `/v1/health`；若边车未启动且配置 `tts.autostart=true`，由 Go 拉起子进程（工作目录、日志重定向、优雅终止 10s）。

### 5.3 Vidu 客户端端点覆盖与错误码

#### 5.3.1 端点表

| 能力 | 方法/路径 | 超时 | 幂等 | 重试 |
|---|---|---|---|---|
| 创建会话 | `POST /live/s_avatar/component`（`create_path` 可配） | **120s** | 否（有副作用） | **不重试** |
| 查询场次 | `GET /live/v1/lives/{id}` | 8s | 是 | 1 次（1s 抖动） |
| 查询录播 | `GET /live/v1/lives/{id}/recording` | 15s | 是 | 1 次 |
| 历史列表 | `GET /live/v1/lives?pager.page=&pager.pagesz=` | 8s | 是 | 1 次 |
| 额度 | `GET /ent/v2/credits?show_detail=false` | 8s | 是 | 1 次 |
| 上传 1 | `POST /tools/v2/files/uploads` | 60s | 否 | 不重试 |
| 上传 2 | `PUT {put_url}`（**无鉴权头**） | 300s | 是（覆盖） | 1 次 |
| 上传 3 | `PUT /tools/v2/files/uploads/{id}/finish` | 60s | 是 | 1 次 |
| 头像列表/创建/删除 | `/live/v1/avatars` | 8/300/30s | 创建否 | 创建不重试 |
| 动作库列表/创建/删除 | `/live/v1/action-libraries` | 8/30/30s | 创建否 | 创建不重试 |
| 动作列表/新增/删除 | `/live/v1/action-libraries/{id}/actions` | 8/120/30s | 新增否 | 新增不重试 |

证据：`client.py:66-198`。

#### 5.3.2 错误码映射

| 上游 | 内部 `error_code` | 处理 |
|---|---|---|
| 400 | `vidu_bad_request` | 原样回显 `code/message`（保留响应体前 500 字，同 `client.py:_check`） |
| 401 | `vidu_auth_failed` | 标记当前 key `invalid`，提示切换 key；不自动轮换 |
| 402 | `vidu_insufficient_credits` | 停开播入口，显示额度不足 |
| 403（创建带 `action`） | `vidu_action_forbidden` | **降级重建一次**（去 `action`），对齐 `engine.py:321-335` |
| 403（其他） | `vidu_forbidden` | 提示权限/Key 不匹配 |
| 404/410 | `vidu_live_not_found` | 视为已结束，走 `finalize` |
| 429 | `vidu_rate_limited` | 读 `Retry-After`；**创建不重试**，提示稍后 |
| 5xx | `vidu_upstream_error` | GET 重试 1 次；POST 创建不重试 |
| 超时（120s） | `vidu_create_timeout` | 标失败，不重试；提示“可能已创建，请到控制台确认” |
| DNS/TLS | `vidu_network` | 提示网络/证书 |

**鉴权**：`Authorization: Token {api_key}`；上传第 2 步**不带**（`client.py:8,102-105`）。

### 5.4 歌词打轴归属

- 蓝本：`tools/lyrics_align.py` 离线生成 `lyrics-cache/*.lrc.json`，运行时只读（`engine.py:1857-1931`）。
- Go 版 v1：**保留外部工具**（Python 或独立 CLI），契约是“输入音频+文本 → 输出 lrc.json”；Go 只做缓存读取与回退 `.txt`。
- v2 可把打轴做成 Asynq `low` 任务，但不在本期。

---

## 6. 前端设计（Quasar + NovaTrader 视觉）

### 6.1 路由与页面

| 路由 | 页面 | 说明 |
|---|---|---|
| `/stage` | `StagePage` | 默认页；主视频 + 右栏 + 底部 + 5 个浮动面板 + 设置抽屉 |
| `/sessions` | `SessionsPage` | 场次列表（本地 + `list_lives` 对账）、状态、时长、额度消耗 |
| `/replay/:id` | `ReplayPage` | 复盘（新增）：摘要 + 事件时间轴 + 录像 |
| `/assets` | `AssetsPage` | 素材：音频、头像、参考图、动作库 |
| `/choreo` | `ChoreoPage` | 编排：组/步骤/间隔/循环/随机/暂停 VIP |
| `/settings` | `SettingsPage` | 设置全量（12 组）；舞台内抽屉复用同一组件 |
| `/login` | `LoginPage` | 仅 `lan` 模式 |

> 舞台页保持“单页可完成直播”的蓝本体验；其余页面是管理入口，不替代舞台。

### 6.2 组件树

```text
AppShell (Quasar QLayout 仅做壳)
├─ StagePage
│  ├─ VideoStage (RtcPlayer + 状态行 + 开播/收播 + 点击开启声音 + 全屏)
│  ├─ RightRail (字幕流 + 事件流, 上限 N 条)
│  ├─ ComposerBar (SayInput, CommandInput, Send/Stop/Interrupt/Clear)
│  ├─ FloatingPanels (drag/resize, localStorage)
│  │  ├─ VoicePanel（音色列表/试听/切换）
│  │  ├─ CommandPanel（动作 tab + 编排 tab）
│  │  ├─ KouboPanel（分段/合并/合成/播放）
│  │  ├─ RefsPanel（1–3 张参考图 switch/remove）
│  │  └─ SongsPanel（点播/上传/导入/歌词）
│  └─ SettingsDrawer (12 groups)
├─ SessionsPage → SessionTable + SessionFilters + SessionDetailDrawer
├─ ReplayPage → SessionSummaryCards + EventTimeline + RecordingPlayer + ExportButton
├─ AssetsPage → AudioAssets / AvatarAssets / ActionLibraries / RefImages
├─ ChoreoPage → ChoreoGroupEditor + StepEditor + RuntimeControls
└─ SettingsPage → SettingsDrawer（同组件，整页布局）
```

**组件分层（遵守 R-UI-10）**：`ui/`（Nt* 基础件，禁 store/router）、`display/`（业务展示件，props/emit）、`panels/`（薄壳，装配）、`stores/`（状态）。`StagePage` 只做装配，不写业务分支（R-UI-11）。

### 6.3 210KB 单页拆分映射（128 个具名函数）

| 原区域（行号） | 内容 | 目标模块 |
|---|---|---|
| `430-495` | 视频/状态/开播收播/全屏 | `VideoStage.vue` + `useLiveStore` |
| `496-533` | 音色面板、快捷指令（动作/编排 tab） | `VoicePanel.vue` / `CommandPanel.vue` |
| `545-640` | 指令、歌曲、参考图面板 | `CommandPanel` / `SongsPanel` / `RefsPanel` |
| `640-837` | 设置抽屉 12 组 | `SettingsDrawer/*`（12 个子组件） |
| `838-900` | 底部输入条/事件流 | `ComposerBar.vue` / `RightRail.vue` |
| `1067-1116` | RTC 初始化（ARTC/TRTC） | `RtcPlayer/adapters/*` |
| `1126-1200` | 入会/播放/重试 | `RtcPlayer/index.vue` |
| `1500-1700` | 状态轮询/合并 | `useLiveStore.pollStatus`（v1 仅作 WS 失效兜底） |
| `1857-1931` | 歌词/字幕 | `useSubtitle` + `CaptionOverlay` |
| `2800-3080` | 统计/额度/事件 | `useStatsStore` + `StatsBar` |
| `3123-3350` | 拖拽/尺寸/快捷键 | `usePanelLayout` + `useHotkeys` |

> 拆分验收：原 128 个函数**逐个登记归属**（表在实施期维护），不允许“重写时漏功能”；以 F43–F48 的 E2E 脚本为准。

### 6.4 Pinia store 划分

| store | 责任 | 数据源 |
|---|---|---|
| `useLiveStore` | phase/live_id/ws/feed/stats/rtc | `GetLiveStatus` + WS |
| `useEventStore` | 事件缓冲、`after_seq`、去重、epoch | `/ws/v1/events` |
| `useTranscriptStore` | 字幕（含歌词优先）、上限 | WS `transcript.*` |
| `useDanmakuStore` | 模式、待确认、丢弃计数 | `DanmakuService` + WS |
| `useActionStore` | 动作库、队列、优先级展示 | `ActionService` + WS |
| `useKouboStore` | 工程/分段/takes | `KouboService` |
| `useChoreoStore` | 编排配置/运行态 | `ChoreoService` + WS |
| `useSettingsStore` | 12 组配置、脏标记、reload | `SettingsService` |
| `useAssetsStore` | 音频/头像/动作库/参考图 | 资产接口 |
| `usePatronStore` | 贵宾名单/欢迎 | `PatronService` |
| `useReplayStore` | 场次摘要/事件时间轴 | `Replay/ListEvents` |
| `useUiPrefs` | 9 个 localStorage 键、面板布局 | 本机 |
| `useAuthStore` | 模式、Bearer、ticket | `AuthService` |

**规则**：store 不直接 `fetch`；统一走 `services/api/*`；WS 只写 `useEventStore`，其余 store 通过订阅派生（避免多源写同一状态）。

### 6.5 WS 事件订阅与断线补齐

- 连接：`GET /api/v1/ws-ticket` → `new WebSocket('/ws/v1/events', ['dh.v1','ticket.'+t])`。
- 首帧：`{type:"hello", after_seq, session_id}`；服务端回 `{type:"snapshot", last_seq, phase}` 再回放。
- 顺序：`seq` 单调；若收到 `seq` 跳跃 → 主动 `resync`（拉 `ListEvents(after_seq)`），**不猜**。
- 合并：`event_id` 去重；同一 `type` 的增量事件合并到 store；`epoch` 变化时清空旧喂流相关 UI 状态（对应 `song_epoch++`）。
- 降级：WS 连不上 → R-UI-34 的可见性轮询（页面可见才轮询），并在 UI 标“降级（轮询）”。
- 心跳：客户端 30s 发 `ping`，服务端回 `pong`；60s 无 pong 重连。

### 6.6 RtcPlayer 封装

```text
RtcPlayer/
├─ index.vue          统一 API：join(creds) / leave() / mute / fullscreen / on(event)
├─ adapters/artc.ts   AliRTC Web SDK 适配（动态加载本地 vendor）
├─ adapters/trtc.ts   TRTC Web SDK 适配
└─ types.ts           RtcCreds { mode, provider, appId, channel, userId, token, expireAt }
```

- **适配器接口**：`join/leave/mute/screenshot?/on('state'|'error'|'autoplay-blocked')`；页面不直接 import SDK。
- **动态加载**：SDK 文件放 `web/public/vendor/`（自托管），避免 CDN 与 CSP 冲突（R-UI-61）。
- **自动播放**：`play()` 失败 → 显示“点击画面开启声音”遮罩（对齐 `stage/index.html:1190-1197`），点击后 `muted=false` 并重试。
- **token 过期**：`expireAt` 由服务端下发；`RtcPlayer` 只负责倒计时与触发 `GetViewerToken`（customer）或提示重开（managed）。
- **失败重试**：入会失败最多 1 次（与蓝本一致，`stage/index.html:1141-1155`），仍失败显示错误码 + “复制诊断信息”。

### 6.7 NovaTrader 规范条目与本方案落点（以源码为准）

> 出处：`NovaTrader/doc/开发文档/前端UI/README.md`；行号为本轮核验值。**评审 v1 的编号/行号有误，本节全部重列**。

| 规则 | 原文要点（行号） | 本方案落点 | 判定 |
|---|---|---|---|
| R-UI-01 | 方向色唯一出口 `toneOf/pnlClass`（`:503`） | 直播不涉涨跌；状态色统一 `toneOf`（成功/失败/警告） | Grep 无裸色 |
| R-UI-02 | 颜色只写 `var(--nt-*)`（`:504`） | 所有 SFC 仅用令牌；新增色必须进 `tokens.css` | `ui-literals` 测试 |
| R-UI-03 | 外部配色必须语义反转（`:505`） | 不从蓝本 stage 复制色值；一律重映射到 `--nt-*` | 评审 |
| R-UI-04 | 状态先入 `ui/status-map.ts`（`:506`） | 场次/WS/TTS/录制状态全部入册（中文文案） | `status-map` 单测 |
| R-UI-05 | 材质/动效上限（`:507`） | 玻璃仅壳层；数据区不发光；`data-motion` 控制 | 走查 |
| R-UI-07 | 正文对比度 ≥4.5:1（`:509`） | 字幕/表格正文用 text-1/2，不用 text-3 | 对比度测试 |
| R-UI-08 | 三模式由 settings 驱动（`:510`） | `useUiPrefs.motion` 单源 | 走查 |
| R-UI-10 | `ui/viz/display` 禁 store/router（`:516`） | 组件三层；数据 props/emit | lint 边界 |
| R-UI-11 | `panels/*.panel.vue` 只装配（`:517`） | `StagePage`/各 Panel 薄壳 | 评审 |
| R-UI-12 | 不引入 UI 组件库（`:518`） | **与 Quasar 要求冲突** → 见 §6.9 的偏差申请 | 评审通过偏差 |
| R-UI-13 | 单 SFC >800 行必须拆（`:519`） | 12 组设置拆子组件；Panel ≤400 行 | 行数检查 |
| R-UI-14 | popout 白名单（`:520`） | 本方案无 popout；浮窗仅舞台内 | 不适用 |
| R-UI-15 | 面板 id 带业务参数（`:521`） | `panelId = stage:{sessionId}:{panel}` | 评审 |
| R-UI-20 | 缺位显示“—”+原因（`:527`） | 额度/在线数/时长缺失一律“— + 原因徽注” | 缺数走查 |
| R-UI-22 | 枚举不直渲，走 STATUS_MAP（`:529`） | `starting/live/error` 不裸露 | 评审 |
| R-UI-23 | 指标卡缺数灰显保留（`:530`） | 统计卡不消失 | 走查 |
| R-UI-24 | “—”与“***”分槽，用 `NtSecretText`（`:531`） | Vidu Key/频道 ID 用 `***`；缺失用“—” | 评审 |
| R-UI-26 | “实时”必须有时间戳/新鲜度（`:533`） | WS 状态旁显示 `last_event_at` | 走查 |
| R-UI-30 | 条件不满足内联禁用（`:539`） | 未开播禁用“说话/动作”；额度<45 禁用开播 | 走查 |
| R-UI-31 | 破坏性操作二次确认（`:540`） | 删除素材/Key/人设/清空队列必须确认，按钮写后果 | 走查 |
| R-UI-32 | 乐观更新以回执为准（`:541`） | 动作/设置保存失败回滚 | 评审 |
| R-UI-34 | 轮询走可见性封装（`:543`） | 仅作 WS 降级；页面隐藏停轮询 | 评审 |
| R-UI-35 | 版本守卫，慢响应不覆盖（`:544`） | 状态请求带 `versionToken` | 评审 |
| R-UI-36 | 错误归一 `classifyApiError`（`:545`） | 映射 §5.3.2 错误码到用户文案 | 评审 |
| R-UI-37 | 全局快捷键登记（`:546`） | `useHotkeys` 单表；Enter/Esc 在此注册 | 评审 |
| R-UI-40 | 间距 4/8/12/16/20/24（`:553`） | 全部用 spacing 令牌 | 走查 |
| R-UI-41 | 数字等宽 tabular-nums（`:554`） | 时长/额度/延迟数字 | 评审 |
| R-UI-43 | 断点 <1280/<960/<720（`:556`） | 舞台右栏在 <1280 收起 | 走查 |
| R-UI-45 | 抽屉宽度 360/480/640（`:558`） | 设置抽屉 480；复盘详情 640 | 走查 |
| R-UI-46 | 面板头三层（`:559`） | 标题 + 状态/新鲜度 + 操作区 | 走查 |
| R-UI-49 | 配置区分本机/共享/启动文件（`:567`） | `useUiPrefs`（本机）/ `SettingsService`（共享）/ `config.yaml`（启动）三档明示 | 走查 |
| R-UI-50 | K 线 lightweight-charts，其余 ECharts（`:573`） | 复盘图用 ECharts（按需） | 依赖评审 |
| R-UI-61 | CSP 不扩，地址集中 `endpoint.ts`（`:583`） | RTC SDK 自托管；WS 地址唯一出口 | 评审 |

**Token 实际值（唯一来源）**：`--nt-bg-0:#050d17`、`--nt-bg-1:#0b1b2b`、`--nt-bg-2:#10263a`、`--nt-bg-3:#18364d`、`--nt-text-0:#eef6fa`、`--nt-text-1:#d3dee6`、`--nt-text-2:#adc8da`、`--nt-text-3:#85a5bc`、`--nt-accent:#45e0ff`、`--nt-accent-2:#769dff`、`--nt-warn:#ffe852`、`--nt-danger:#ff5c7c`、`--nt-success:#35dfb1`、`--nt-info:#66b5ff`、`--nt-ai:#c792ea`、`--nt-up:#ff6b81`、`--nt-down:#60b296`（`NovaTrader/web/src/theme/tokens.css:8-45`）。

> v1 草案写的 `#071016/#0d1b22/#4fd1c5/#e6f1f5/#8fa6b2` **全部作废**，以本节为准。

### 6.8 12 组设置字段与生效时机（重写）

> 蓝本抽屉头明确：“保存后下次连线生效；音色即时生效”（`stage/index.html:683`）；字幕层与弹幕间隔另有“即时生效”（`:802,810`）。

| # | 组 | 关键字段（行号） | 生效时机 |
|---|---|---|---|
| 1 | 会话 | 模型 `vidu-s2/s1`、RTC 模式 `managed/customer`、托管/自备 provider、`avatar_id`、形象图片、开场白、闲置挂断、单场上限（`685-699`） | 下次连线 |
| 2 | 声音 | 音色预设、试听文本、喂流帧长(ms)、输出按句 type10（`702-709`） | 音色即时；帧长下次连线 |
| 3 | 专属会员 | 昵称/称呼/备注、添加、测试欢迎、名单（`712-719`） | 即时（名单文件） |
| 4 | 人设 | 当前人设、应用、刷新、保存文本框（`722-728`） | 下一条回复生效，且清空记忆 |
| 5 | LLM | base_url、model、key 文件、temperature、max_tokens、system、清空记忆（`731-739`） | 下次连线（Reset 即时） |
| 6 | 自备 RTC | ARTC AppID/AppKey、token 形态（3 种）、TRTC AppID/Key（`742-748`） | 下次连线 |
| 7 | 动作库 | 启用、库选择、新建库、加动作（`751-759`） | 下次连线 |
| 8 | 资产库 | 拉取资产、传图建形象、列表、启用（`762-769`） | 下次连线 |
| 9 | Vidu Key | 当前生效（尾 8 位）、备用列表、启用/删除、新 Key（`772-784`） | 下次连线 |
| 10 | 字幕层 | 显示、下移 px、行数、字号、淡出、显示事件、隐私打码、阴影（`787-802`） | **即时** |
| 11 | 弹幕主播模式 | 播报间隔、VIP 间隔（`805-810`） | **即时** |
| 12 | 其他 | 积分余额、查询、保存（`813-817`） | 保存按钮 |

### 6.9 Quasar 与 NovaTrader 的冲突处理（必须显式偏差）

| 冲突 | NovaTrader 规则 | 本方案 |
|---|---|---|
| 引入 Quasar（组件库） | R-UI-12 禁 UI 库（`:518`） | **偏差申请**：Quasar 仅作 AppShell（路由/布局/平台/构建），**不启用 Material CSS**，不使用 `QBtn/QCard/QInput/QTable/QDialog/QMenu` 默认皮肤 |
| 视觉原子 | R-UI-02/05/07/40/41 | 全部自建 `Nt*`（按钮/输入/表格/抽屉/徽章/新鲜度/密钥文本），令牌取自 `tokens.css` |
| 抽屉宽度 | R-UI-45（360/480/640） | 自定义 Drawer，宽度 480；不用 `QDrawer` 默认 |
| 主题切换 | `data-theme/data-density/data-motion` | Quasar 的 Dark 插件**关闭**，由 `data-*` 单源驱动 |
| CSP | R-UI-61 | `script-src 'self'`；RTC SDK 自托管；禁止 CDN |
| 图表 | R-UI-50 | 仅复盘用 ECharts 按需引入；不引入第二家 |

**结论**：视觉验收以 NovaTrader 为准，Quasar 只承担“壳与工程化”。若评审认为偏差不可接受，替代方案是去掉 Quasar、直接 Vue3+Vite（与 NovaTrader 同栈），代价是放弃用户指定的 Quasar。

### 6.10 UI 本地偏好（9 键）

| 键 | 内容 | 默认 |
|---|---|---|
| `s2s.stage.cmdFx` | 指令动效开关 | on |
| `s2s.stage.stats` | 统计条显示项 | 全部 |
| `s2s.stage.statsPos` | 统计条位置 | 底部 |
| `s2s.stage.rndEvery` | 随机表演间隔 | 0（关） |
| `s2s.stage.rndBias` | 随机偏置 | 0 |
| `s2s.stage.cmdEdits` | 指令编辑缓存 | 空 |
| `s2s_songPath` | 上次歌单路径 | 空 |
| `s2s.stage.rightW` | 右栏宽度 | 360 |
| `s2s.stage.kbCmdOpen` | 键盘指令面板开合 | 关 |

证据：`stage/index.html:1560,1231,1483,1869,1871,1955,2723,3123,3346`。

---

## 7. 数据模型（Ent）

### 7.1 实体

| 实体 | 关键字段 | 索引/约束 | 说明 |
|---|---|---|---|
| `LiveSession` | `id, live_id, phase, mode, persona_id, avatar_id, started_at, ended_at, duration_s, config_snapshot(json), stats(json), recording_state, recording_url, error_code` | `live_id` 唯一；`phase` 索引 | 一场一行；`config_snapshot` 只存非敏感字段 |
| `SessionEvent` | `id, session_id, seq, event_id, type, ts_ms, epoch, payload(json)` | 唯一 `(session_id,seq)`；`event_id` 唯一 | 断线补齐/复盘 |
| `Persona` | `id, slug, name, tagline, voice_id, avatar_note, system_prompt, is_active` | `slug` 唯一 | 对齐 `src/personas.py` |
| `ActionLibrary` / `Action` | `library_id, name, instruction, sort` | `name` 唯一/库 | 动作库 |
| `Asset` | `id, kind(image/audio/avatar/ref), name, path, sha256, bytes, duration_ms, uploaded_at, expires_at, ref_count` | `sha256` 唯一 | 90 天 `expires_at` |
| `DanmakuEvent` | `id, session_id, kind(chat/gift/join), user_id, user_name, text, diamond, source, decision(t0/t2/confirm/blocked/dropped), created_at` | `(session_id,created_at)` | 风控与复盘 |
| `GiftBatch` | `id, session_id, users(json), diamond_total, merged_at` | — | 礼物合并 |
| `TtsJob` | `id, session_id, text_hash, text, voice_id, state, started_at, duration_ms, error_code, cache_hit` | `text_hash` 索引 | 成本/排障 |
| `KouboProject` | `id, name, project_json, updated_at` | — | 迁移自 `project.json` |
| `KouboTake` | `id, project_id, segment_id, text_hash, audio_path, duration_ms` | `(project_id,segment_id,text_hash)` | 复用缓存 |
| `Song` | `id, name, path, duration_ms, lrc_json, updated_at` | `path` 唯一 | 歌单 |
| `ChoreoConfig` | `id, config_json, updated_at` | — | 编排 |
| `Patron` | `id, nick, alias, note, created_at` | `nick` 唯一 | 贵宾 |
| `VipConfig` | `threshold_diamond, cooldown_s, template, once_per_session` | — | 贵宾礼 |
| `BlockWord` / `DmRule` | `word/pattern, action` | — | 弹幕风控 |
| `ViduKey` | `id, cipher(bytea), tail8, is_active, added_at, last_ok_at, last_error` | `is_active` 部分唯一 | **密文**存 DPAPI blob |
| `RtcCredential` | `mode, provider, cipher(bytea), updated_at` | — | customer 密钥 |
| `Setting` | `key, value_json, updated_at, updated_by` | `key` 唯一 | 12 组中共享项 |
| `CostLedger` | `session_id, credits_start, credits_end, tokens_in, tokens_out, diamonds, gift_count, estimated_cost` | `session_id` 唯一 | 复盘 |
| `SessionCrash` | `session_id, detected_at, reconcile, actions` | — | 崩溃恢复 |
| `AuditLog` | `actor, route, action, target, ip, ts` | `ts` 索引 | 写操作审计 |
| `Recording` | `session_id, state, url, fetched_at, expires_at` | `session_id` 唯一 | 录播 |

### 7.2 不落库清单（明确）

| 内容 | 原因 | 存放 |
|---|---|---|
| 原始 PCM/每帧字节 | 体积大、可重建 | 不存 |
| 喂流 `owner/gen/t0`、动作冷却、队列 | 进程态 | 内存 |
| TTS 中间 wav | 可重建 | 缓存 LRU（文件） |
| 弹幕 pending 队列 | 瞬时 | Redis/内存 |
| RTC token 明文 | 安全 | 不存（`expire_at` 可存） |
| 面板位置/宽度/动效偏好 | 本机 | localStorage（9 键） |
| 逐帧 lag 样本 | 高基数 | 指标聚合，不落库 |
| 字幕中间态 | 只保留最终句 | `SessionEvent`（`transcript.final`），30 天清理 |
| 完整 API Key | 安全 | 仅 DPAPI 密文 + tail8 |

**保留策略**：`SessionEvent` 90 天；`DanmakuEvent` 30 天；`TtsJob` 30 天；`AuditLog` 180 天；`CostLedger` 永久（可聚合）。

---

## 8. 并发与多场次

### 8.1 蓝本现状（证据）

- `engine = Engine()` 在模块导入时创建，全局唯一：`src/bridge.py:56`。
- Vidu 客户端配置是模块级全局：`src/vidu/client.py:21`（`CONF` 与 `_client`）。
- OmniVoice 使用全局锁与事件循环锁：`src/tts/omnivoice.py:88-89`。
- 看门狗、owner、`song_epoch` 都是实例字段，但实例本身只有一份。

**结论：蓝本物理上只能跑一场。**

### 8.2 v1 决策：单场（明确写死）

- `SessionManager{max:1}`；`StartLive` 时若已有 active，走**重复开播**语义（先 `stop("reconnect")`），**不并行**。
- 所有实时状态挂在 `LiveSession` 上（非包级变量），为 v2 多场留接口，但 v1 **不启用**。
- 前端只显示一个场次；`/sessions` 是历史，不是并发。

### 8.3 多场隔离方案与代价（v2 备选，不做）

| 维度 | 方案 | 代价 |
|---|---|---|
| 会话隔离 | `map[sessionID]*LiveSession`，各自 owner/epoch/队列 | +1–2 周改造与测试 |
| 配置隔离 | 每场 `config_snapshot`，禁全局 `CONF` | +1 周 |
| TTS 隔离 | 边车按会话公平队列（或每场一边车） | **GPU 无法并行**；同卡只能串行，延迟翻倍；多卡才真并行 |
| RTC | 每场独立 channel/token | 平台额度与计费叠加 |
| 事件 | 按 `session_id` 分区 | 需要 NATS/多实例 |
| 运维 | 端口/日志/指标多维 | +1 周 |
| **总计** | — | **+3–6 周**，且受 GPU 数量硬约束 |

**建议**：v1 明确单场；若业务确需多场，先做“多进程部署”（每进程一场 + 反向代理），比在单进程内做多场更便宜、更安全。

---

## 9. 许可与 clean-room

### 9.1 三条路线

| 路线 | 做法 | 许可后果 | 工作量 | 结论 |
|---|---|---|---|---|
| A 派生开源 | fork xiaoya，保留 AGPL-3.0，Go/前端作为衍生作品 | **整个分发物必须 AGPL**；网络提供服务也要提供源码；NOTICE 排除素材仍适用；UI 复制也属衍生 | 最小 | 若公司接受 AGPL 且只自用，可选；不推荐用于商业闭源 |
| B 自研重写（clean-room） | 实现团队不看蓝本源码，只读行为规格 | 无 AGPL 传染；不复制表达 | 最大 | **推荐**（本方案） |
| C 仅行为对齐 | 黑盒观察 + 官方协议 + 自测，重写实现，UI 重设计 | 同 B；需证据链证明未复制 | 大 | 与 B 合并执行 |

**推荐 C（= clean-room 行为对齐）**：功能对等、表达独立、许可干净。代价是工期（已计入 §10）。

### 9.2 明确禁止复制的对象

| 类别 | 禁止复制 | 允许的替代 |
|---|---|---|
| **UI 结构** | `stage/index.html` 的 DOM 层级、class 名、CSS 规则、像素尺寸、图标/emoji 排布 | 按功能重画布局；用 `tokens.css` 令牌重写样式 |
| **UI 文案** | 蓝本独有中文文案、提示语、按钮组合 | 重写文案（通用词如“开播/收播”可用） |
| **代码** | 任何 `.py`/`.js`/`.html` 片段、函数体、注释 | 只读行为，重写；注释独立撰写 |
| **命名** | 函数名（`_own_audio`、`_feed_pcm`、`_dm_worker`…）、变量名、文件名、内部常量名 | 采用本方案命名（`AcquireFeedOwnership`、`FeedPacer`…） |
| **常量** | 内部调参（75s/6 次/0.55x/25s/1.5s/6s 等）**不作为“事实”直接抄**；需黑盒复测或由本方案定义为产品参数 | 以“行为参数表”重新定义，并在规格中标注来源=实测/产品决定 |
| **协议字段** | 蓝本**自有** API 字段（如 `tr_list` 本就不存在）、内部事件名 | Vidu 官方字段（`conn_init`、`seq_id`）属第三方协议事实，可用；自有 API 用新命名 |
| **素材** | 人物形象、克隆音色、人设文案、歌单、动作库、品牌名、图标、字体 | 用户自备；示例用公开可商用素材 |
| **数据** | `config.example.json` 的具体内容、`patrons.json`、`personas/` | 重新设计默认值并以测试固定 |

> **协议字段的例外**：为实现互操作而必须一致的字段（Vidu WS 的 `type/conn_id/seq_id`、HTTP 路径）属于功能事实，不属于 xiaoya 的表达；但仍需以**官方文档**为依据，而不是抄蓝本代码。

### 9.3 clean-room 证据链（评审与自查如何证明）

**两件套模型**：

| 产物 | 内容 | 持有人 | 用途 |
|---|---|---|---|
| **评审基线**（本文件） | 含蓝本行号、行为表、验收口径 | 评审/架构，不进实现组工作区 | 可追溯性、评审 |
| **实现规格** | 去掉行号/函数名/代码片段/蓝本独有文案；只有“输入→输出→约束” | 实现组 | 唯一实现依据 |

**执行规则**：
1. 实现组签署“未阅读蓝本源码”声明；仓库/共享盘不挂载蓝本目录（评审机器除外）。
2. 规格评审时检查：是否含蓝本函数名、变量名、注释原文、CSS 类名；命中即打回。
3. 代码提交扫描：
   - 标识符 denylist（`_own_audio/_feed_pcm/_dm_worker/envelope/...`）Grep 必须 0；
   - 文本相似度（`jscpd`/`dupl`）阈值 <5%；
   - UI：禁止 `stage` 的 class 名；截图只做“功能有无”核对，不做像素级还原。
4. 每个功能条目在实现规格中标注**来源**：`官方文档` / `黑盒观察` / `产品定义`；不允许“源码行号”。
5. 第三方素材/协议单独登记：Vidu 文档、RTC SDK 文档、ffmpeg 许可、模型许可。
6. 交付时附《未复制声明 + 扫描报告 + 素材清单》。

**本文件的定位**：它是评审基线，含行号是**为了评审可核验**；进入实施前必须生成去引用的实现规格（§9.3 表），否则 clean-room 不成立。

### 9.4 HFC / NovaTrader 的许可风险

| 仓库 | 许可事实 | 风险 | 建议 |
|---|---|---|---|
| HotCraftFlow | 根目录**无 LICENSE**（`HotCraftFlow-profile.md:49-51`），NOTICE 只是第三方登记 | 默认“保留所有权利”，**不能复制代码**；派生即侵权 | 只借鉴分层/命名/工程模式（思想不受版权保护）；不 vendor、不 fork |
| NovaTrader | 根目录**无 LICENSE**（本轮核验） | 不能复制源码、CSS、设计文档文本 | 只提炼“规则条目”作为内部设计约束，样式用令牌值重写；文档引用注明出处 |
| cyber-xiaoya | AGPL-3.0（`LICENSE:1`）；NOTICE 排除素材（`NOTICE:6-12`） | 复制代码/UI 即传染；素材本就无授权 | 按 §9.1 C 执行；如需复用 TTS 模型代码，作为**独立 AGPL 边车**并履行源码义务 |

---

## 10. 里程碑与工期（重估）

### 10.1 假设

- 团队：**2 Go + 2 前端 + 0.5 QA**；产品/评审兼职。
- 环境：真实 Vidu 账号与额度、OmniVoice GPU 机器、目标 Windows 机器，**从 M0 第 1 周可用**。
- 范围：v1 单场；不含多场、不含移动端、不含 Agora。
- 联调与实时音频调试**已计入**，不额外剔除；每周留 0.5 天排障。

### 10.2 与 10–14 周的差额

| 增量 | 周数 |
|---|---|
| 鉴权/密钥/审计（§3.9） | +1 |
| 幂等/崩溃恢复/对账（§3.10） | +1 |
| 可观测性（§3.11） | +0.5 |
| 边车契约/取消/熔断 + Vidu 错误码（§5） | +1 |
| 验收体系与故障注入脚本（F/G 全量） | +1 |
| 前端 12 组设置 + 6 面板 + R-UI 门禁 | +1 |
| 真机联调/8h 漂移/长稳缓冲 | +1.5 |
| **合计** | **+7**（原 10–14 → **16–20**） |

> 若只做“能跑”的 10–14 周版本，必须裁掉：鉴权（仅 loopback）、崩溃对账、可观测性、复盘页、30 条遗漏行为中的 8 条；这会显著降低可运维性，**不建议**。

### 10.3 里程碑表

| 阶段 | 周期 | 交付物（可演示） | 退出条件（可测） |
|---|---|---|---|
| **M0 骨架可跑** | 3 周 | Kratos+Ent+Wire 空跑；`/healthz`；WS echo + ticket 鉴权；事件信封与落库；fake Vidu（创建/WS/额度）；CI（lint/test/build）；前端 Quasar 壳 + 登录 | `go test ./...` 绿；WS 断线 5s 后按 `after_seq` 补齐；`docker compose` 起 PG/Redis；V1/V7 关闭 |
| **M1 开播闭环** | 5–6 周 | 真实 Vidu 创建/WS/挂断；48B/ms 喂流；owner 状态机；看门狗；TTS 边车契约；开场白；RTC token；F01–F18 验收脚本 | 真机连续 30min 有声；8h 漂移 ≤100ms；故障注入（限速/静默/写阻塞/抢占）全部通过；V2–V6 关闭 |
| **M2 前端对等** | 5–6 周 | 舞台完整（视频/右栏/底部/5 面板/设置抽屉）；12 组设置；RTC 播放器；9 键偏好；快捷键；F43–F49 E2E | Playwright 用例绿；R-UI 走查 0 阻断项；自动播放兜底可用；V10 关闭 |
| **M3 编排/复盘/收口** | 4–5 周 | 弹幕 T0/T2/批读/VIP；动作/礼物/贵宾；编排；口播 14 接口；歌曲/歌词；参考图；录播；复盘页；指标/告警；崩溃恢复演练 | F19–F42、F50–F51 全绿；4h 长稳；kill -9 后重启无幽灵场次；审计/脱敏扫描通过 |
| **缓冲** | 1–2 周 | — | 处理真机抖动与评审遗留 |

**合计**：**17–20 周**（按 §0 取整为 **16–20 周**；单前端则 20–24 周）。

### 10.4 阶段门禁（每阶段必须过的“证据包”）

1. 自动化测试报告（单测 + 契约 + E2E）。
2. 真机录屏/日志（有音频的场次 ≥30min）。
3. 指标截图（`feed_lag_ms` p99、重连次数、TTS 失败率）。
4. 变更清单 + 回归范围。
5. clean-room 扫描报告（标识符/相似度）。

---

## 11. 风险清单

### P0（阻塞上线）

| # | 风险 | 触发信号 | 影响 | 规避动作 |
|---|---|---|---|---|
| P0-1 | 实时音频时序错误（漂移/抢占/叠音） | `feed_lag p99>300ms`、用户听出双声、断流 | 直播不可用 | 8h 压测门禁；owner race 测试；真机 30min × 10 次；录音回放比对 |
| P0-2 | Vidu WS 行为与文档不一致（类型/心跳/关闭码） | 1011/异常关闭、conn_init 卡住 | 无法开播/中途断 | V7 真机先行；协议层加“未知类型计数 + 原始帧留档 10 分钟”；不猜测重发 |
| P0-3 | managed RTC token 到期不可续 | 直播 2h 后画面黑 | 长时间直播失败 | 事前提示；到期重开流程；产品确认单场时长上限（建议 ≤2h） |
| P0-4 | OmniVoice OOM/崩溃 | 边车重启、合成失败率升高 | 无声 | 串行 worker=1；VRAM 监控；熔断 + 退避重启；准备 B 计划（云端 TTS 降级音色） |
| P0-5 | AGPL/素材侵权 | 法务评审不通过 | 项目停止 | clean-room 证据链；素材清单；不复用蓝本 UI/文案；法务在 M0 签字 |
| P0-6 | 局域网暴露 Key/无鉴权 | 扫描到 0.0.0.0 监听 | 密钥泄漏/被开播 | 默认 loopback；lan 模式强制 Bearer+Origin+HTTPS；启动自检拒绝弱配置 |
| P0-7 | 崩溃后云端会话继续计费 | 重启后额度下降 | 直接资损 | 启动对账；尝试 hangup；`session_crashes` 告警；额度阈值告警 |

### P1（影响体验/运维）

| # | 风险 | 规避动作 |
|---|---|---|
| P1-1 | Windows 定时器抖动导致漂移 | §4.3.2 混合等待；20ms 分片作为 B 计划；CI 定时任务跑短版压测 |
| P1-2 | ffmpeg 路径/编码差异 | 启动时探测 `ffmpeg -version`；失败则禁用歌曲/口播并提示 |
| P1-3 | LLM 输出不稳定/超时 | 降级模型 + 首 token 超时；提示词版本化 |
| P1-4 | 弹幕采集器依赖 200 语义 | 兼容层固定 200+reason；契约测试用采集器真实请求样本 |
| P1-5 | 前端范围膨胀 | 冻结 M2 页面清单；R-UI 门禁；变更走评审 |
| P1-6 | HFC 无法构建被误当底座 | 明确“不 vendor”；只抄目录形状；CI 不依赖 HFC |
| P1-7 | RTC SDK CSP/自动播放 | 自托管 SDK；点击开启声音；错误码提示 |
| P1-8 | 数据保留/PII（昵称、弹幕） | 保留期与脱敏；导出需审计；不存原始音频 |
| P1-9 | 验收环境不稳定（Vidu 限流/额度） | 专用测试账号；额度阈值；fake 覆盖 80% 用例 |
| P1-10 | Quasar 与 R-UI-12 偏差不被接受 | M0 前评审拍板；备选去 Quasar |

---

## 12. 自检（v2）

### 12.1 数量与结构复核（逐个对照）

| 项 | 蓝本实际 | v2 自检结论 |
|---|---|---|
| HTTP 路由 | **63**（`bridge.py`）+ **14**（`koubo.py`）= **77** | 全部有对应 RPC 或兼容层（§3.7）；无遗漏；`/tr_list` 不存在（B25） |
| 设置分区 | **12 组**（`stage/index.html:683-818`） | §6.8 逐组登记；生效时机按蓝本（多数下次连线） |
| 浮动面板 | **5 个**（音色/快捷指令/口播/参考图/歌单）+ 右栏 | §6.2 全部覆盖；贵宾在设置组，不虚构独立面板 |
| localStorage | **9 键** | §6.10 全部登记（评审的 7 键已纠正） |
| stage 函数 | **128 个具名函数** | §6.3 区域映射；实施期逐函数登记，防止漏功能 |
| WS 类型 | 10 种 + 本地 `-1` | F10 全部编解码；无“静音工具”这类臆造 |
| 音频参数 | 24k / s16le / 48B/ms / 100ms | F11/F12；无 0.25s 跳过（已删除） |
| 弹幕 | 无文本去重；200+reason | F21/F24；已纠正 |
| 优先级 | vip3/manual2/choreo1/其他0 | F26；已纠正 |
| 动作/礼物/贵宾/编排/口播 | 见 B18–B20、F26–F31 | 全部落点 |
| 明确不做 | Agora、Go 拉帧、多场、token 自动续 | §2.1、§2.2、§8、§4.8 |

**未决项**：V1–V12（§4.11）全部标 ⏳，未当已确定写死；§13 中列为“未决”的仅 3 项（managed token 续期、TRTC UserSig 对拍、Quasar 偏差审批）。

### 12.2 仍标记“待验证”的结论（不得当作已定）

| 结论 | 当前状态 | 影响章节 |
|---|---|---|
| Go 1.26 在目标 Windows 可用 | ⏳V1 | §3.1 |
| 100ms 分片 8h 漂移 ≤100ms | ⏳V2 | §4.3 |
| `timeBeginPeriod` 是否启用 | ⏳V3 | §4.3.2 |
| managed token 不可续 | ⏳V4 | §4.8 |
| TRTC UserSig 完全一致 | ⏳V5 | §4.8 |
| ARTC 三形态均可用 | ⏳V6 | §4.8 |
| Vidu WS 真实心跳/关闭行为 | ⏳V7 | §4.1 |
| 参考图成功不回 ack | ⏳V8 | F33 |
| 边车并发=1 的显存边界 | ⏳V9 | §5.2 |
| 浏览器自动播放策略 | ⏳V10 | §6.6 |
| 局域网证书可信任 | ⏳V11 | §3.9 |
| 录像 URL 有效期 | ⏳V12 | F34 |

### 12.3 完整映射表（xiaoya 功能 → Go 模块 / 前端组件 / 外部进程）

| 功能组 | ID | Go 模块 | 前端组件 | 外部进程 |
|---|---|---|---|---|
| 开播/收播/重复开播 | F01 F02 | `biz/live`, `engine/live_engine`, `engine/vidu_http` | `StagePage`, `LiveControlBar` | Vidu |
| 状态/事件/断线补齐 | F03 F35 | `biz/live`, `data/repo.SessionEvent`, `server/ws_events` | `useLiveStore`, `useEventStore`, `RightRail` | — |
| WS 生命周期/类型 | F04 F10 | `engine/vidu_ws` | `useLiveStore.ws` | Vidu WS |
| 看门狗/重连/清理 | F05 F06 | `engine/watchdog`, `engine/live_engine` | `HealthPill`, `useLiveStore` | — |
| 创建/查询/录播/额度/列表 | F07 F08 F34 | `engine/vidu_http`, `biz/record`, `biz/billing` | `StagePage`, `RecordingPlayer` | Vidu |
| 资产/头像/动作库 | F09 F39 F40 | `biz/assets`, `biz/avatar`, `biz/action`, `engine/vidu_http` | `AssetsPage`, `AvatarSettings`, `ActionSettings` | Vidu |
| 24k PCM / 节奏 / 漂移 | F11 F12 | `pkg/audio`, `engine/feeder` | 仅状态显示 | ffmpeg（解码） |
| 喂流权/发送超时 | F13 F14 | `engine/ownership`, `engine/feeder` | 事件时间轴 | — |
| LLM/分句/字幕上报/降级 | F15 F16 F19 G06 | `biz/chat`, `engine/subtitle` | `useTranscriptStore`, `CaptionOverlay` | LLM |
| TTS 队列/整段/音频文件 | F17 F18 | `biz/tts`, `engine/tts_client` | `VoicePanel` | OmniVoice 边车 |
| 音色/预览/文件管理 | F20 F41 | `biz/tts`, `pkg/blob` | `VoicePanel`, `AudioAssets` | 边车 + ffmpeg |
| 弹幕接入/T0/T2/批读/VIP | F21 F22 F23 F24 F25 | `biz/danmaku`, `engine/dm_worker` | `DanmakuFeed`, `DmModeSwitch`, `DmConfirmCard` | 弹幕采集器 |
| 动作优先级/调度 | F26 | `biz/action`, `engine/action_queue` | `CommandPanel` | — |
| 礼物合并/欢迎/贵宾 | F27 F28 F29 | `biz/gift`, `biz/patron` | `PatronSettings`, `RightRail` | 采集器 |
| 编排 | F30 | `biz/choreo`, `engine/choreo_runner` | `ChoreoPage`, `CommandPanel` | — |
| 口播 | F31 | `biz/koubo`, `data.KouboProject` | `KouboPanel` | 边车 + ffmpeg |
| 歌曲/歌词 | F32 G10 G11 G12 G13 | `biz/song`, `pkg/audio`, `pkg/blob` | `SongsPanel`, `CaptionOverlay` | ffmpeg + 打轴工具 |
| 参考图 | F33 | `biz/refs`, `engine/vidu_ws` | `RefsPanel` | Vidu |
| 统计/成本 | F36 | `biz/billing`, `data.CostLedger` | `StatsBar`, `SessionSummaryCards` | Vidu credits |
| 设置聚合/12 组 | F37 F46 | `biz/settings`, `data.Setting` | `SettingsDrawer`（12 子组件）, `SettingsPage` | — |
| 人设/头像 | F38 F39 | `biz/persona`, `biz/avatar` | `PersonaSettings`, `AvatarSettings` | Vidu |
| Vidu Key | F42 | `biz/vidukey`, `pkg/secrets`, `data.ViduKey` | `ViduKeySettings` | DPAPI |
| 舞台/面板/底栏/快捷键 | F43 F44 F45 F47 F48 | `server` 提供状态 | `VideoStage`, 5 Panels, `ComposerBar`, `usePanelLayout`, `useHotkeys` | — |
| RTC 播放/token/到期显示 | F49 G02 | `biz/rtc`, `engine/rtc_token` | `RtcPlayer/adapters`, `HealthPill` | 浏览器 SDK |
| 复盘 | F50 | `biz/replay`, `data.SessionEvent` | `ReplayPage` | 录像 URL |
| 健康/指标 | F51 | `server/healthz`, `pkg/metrics` | `HealthPill` | — |
| 开场白/额度轮询/记忆/降级 | G01 G03 G07 G08 | `engine/greeting`, `biz/billing`, `biz/chat`, `biz/live` | `LiveControlBar`, `SettingsDrawer` | Vidu |
| 人设 prompt/资产提示/key 尾号 | G04 G05 G17 G18 | `biz/persona`, `biz/assets`, `biz/vidukey` | `PersonaSettings`, `AssetSettings`, `ViduKeySettings` | — |
| 文件优先/歌词/切歌/字幕 | G09 G10–G13 G16 | `engine/greeting`, `biz/song`, `engine/subtitle`, `engine/feeder` | `SongsPanel`, `CaptionOverlay` | ffmpeg |
| 随机表演/主播模式/拖拽 | G14 G15 G19 G20 | `biz/choreo`, `biz/settings` | `useUiPrefs`, `RightRail`, `CommandPanel` | — |
| 动作停止/打断/帧长 UI | G21 G22 G23 | `biz/action`, `engine/live_engine`, `biz/settings` | `ComposerBar`, `SettingsDrawer` | — |
| 贵宾 join/去重/礼物名/口播缓存/锁 | G24 G25 G26 G27 G28 | `biz/patron`, `biz/gift`, `biz/koubo` | `PatronSettings`, `KouboPanel` | 边车 |
| reload/停止保帧 | G29 G30 | `biz/settings`, `engine/live_engine` | `VideoStage`, `SettingsDrawer` | — |

> 该表覆盖 F01–F51 与 G01–G30 全部条目（同一条目可能出现在多行，但无遗漏）。每行的验收标准见 §2.4/§2.5。

---

## 13. 审阅意见 → 采纳/部分采纳/不采纳 → 理由 → 文档位置

### 13.1 P0（10 条，全部处理）

| 编号 | 处理 | 理由 | 位置 |
|---|---|---|---|
| P0-1 前端规范臆造 | 采纳 | 已按 `NovaTrader/.../README.md` 与 `tokens.css` 重列 32 条规则与真实 Token；删除不存在的 `NtPanel` | §6.7、B32/B33、§1 R5 |
| P0-2 无鉴权/密钥管理 | 采纳 | 新增三模式、全部写接口清单、WS ticket、DPAPI、脱敏与审计 | §3.9、§7.1 |
| P0-3 重复开播语义错误 | 采纳 | 复刻 `engine.py:305-306` 的 `stop("reconnect")` 后重开；不返回 409 | F01、§3.8 |
| P0-4 心跳参数错误 | 采纳 | 改为 `ping_interval=10/ping_timeout=20`（`signaling.py:84-86`） | B3、§4.1、§4.2 |
| P0-5 RTC token 设计缺失 | 采纳 | 写明 managed 不可续、customer ARTC 三形态/TRTC UserSig、到期策略 | §4.8、F49 |
| P0-6 弹幕去重/优先级臆造 | 采纳 | 删除“按文本去重”；优先级改为 3/2/1/0；保留 8 条缓冲与 dropped | F24、F26、B17/B21 |
| P0-7 `tr_list`/`since` 语义错误 | 采纳 | 删除 `tr_list`；拆分 `transcript_since` 与 `event_seq` | B25、F03、F35 |
| P0-8 边车契约缺失 | 采纳 | 新增完整契约（取消/并发/超时/失败/熔断/缓存） | §5.2 |
| P0-9 0.25s 跳帧/优先级数字臆造 | 采纳 | 删除 0.25s 跳过；补状态机图与 4 轮/1.5s/25s/5s 真实算法 | §4.3、§4.4、B16 |
| P0-10 自检数字错误 | 采纳 | 路由改 77、设置 12 组、localStorage 改 9 键、面板改 5+右栏 | §12.1、B1/B2/B26/B28/B30 |

### 13.2 P1（25 条）

| 编号 | 处理 | 理由 | 位置 |
|---|---|---|---|
| P1-1 开播 120s/starting 10s | 采纳 | F01 已写创建 120s、UI 200ms 进入 starting | F01 |
| P1-2 看门狗参数 | **部分采纳** | 0.55x/6 次/20s 采纳；评审称“60s 冷却无出处”不准确：`engine.py:456-457` 确有“距上次重连 <60s 不判 stall”的**抑制窗**，已改名并写入 | §4.5、B6、§1 R3 |
| P1-3 3s 发送超时 | 采纳 | F14 以 3s 为唯一阈值，补失败后果 | F14、§4.5 |
| P1-4 type7+0.25s | 采纳 | 写入抢占算法第 4 步，并加“300ms 内不得发旧帧” | §4.4 |
| P1-5 无跳帧 + p99/8h | 采纳 | 删除跳帧；补 8h 指标与通过标准 | F12、§4.3.3 |
| P1-6 “流中断可回退”臆造 | 采纳 | 删除“回退”；改为“中断即结束本轮” | F15 |
| P1-7 打断回退 | 采纳 | 同上，并加 `llm_stream_aborted` 事件 | F15、G22 |
| P1-8 整段音频来源 | 采纳 | 明确 `.lrc.json`/`.txt` 回退与点播语义 | F18、G10/G11 |
| P1-9 点播出声目标 | 采纳 | 给出 ≤3s/≤10s 的可测目标 | F32 |
| P1-10 弹幕 200 语义 | 采纳 | 固定“永远 200 + reason”，并保留旧路径 | F21 |
| P1-11 口播 14 路由 | 采纳 | 14 条逐条映射；`project.json` → DB | F31、§3.7 |
| P1-12 会话/转写游标缺口 | 采纳 | 事件 seq 与字幕下标分离，补 `ListEvents` | F03/F35 |
| P1-13 成本口径 | 采纳 | 只列 credits/tokens；其他标“不提供/估算” | F36 |
| P1-14 设置 12 组 | 采纳 | 逐组字段与生效时机 | §6.8 |
| P1-15 头像单选 UI | 采纳 | 资产列表用“● 当前”而非 radio | §6.8 组 8 |
| P1-16 VoicePanel 是否存在 | **部分采纳** | 评审称“不是独立面板”不准确：蓝本同时有 `#vpanel`（`stage/index.html:515-521`）与设置组“声音”（`:702-710`）；两者都复刻 | §6.2、§6.8、§1 R5 |
| P1-17 面板/9 键 | 采纳 | 5 浮动面板 + 右栏；9 键 | §6.2、§6.10、B28/B30 |
| P1-18 Redis 必选 | 采纳 | Asynq/幂等/ticket 都依赖 Redis，已明确 | §3.1、§3.6 |
| P1-19 SessionManager 抽象 | 采纳 | 新增 `SessionManager{max:1}` 与每场 `LiveSession` | §3.3、§8.2 |
| P1-20 数据模型缺口 | 采纳 | 补 ViduKey/RtcCredential/DmRule/BlockWord/VipConfig/AuditLog/SessionCrash/Song/KouboTake | §7.1 |
| P1-21 clean-room 自相矛盾 | 采纳 | 两件套模型：评审基线 vs 去引用实现规格 | §9.3 |
| P1-22 工期 16–20 | 采纳 | 重估 16–20（单前端 20–24），给出 +7 周差额表 | §10 |
| P1-23 V 表缺责任/门禁 | 采纳 | V1–V12 含责任/截止/kill criteria | §4.11 |
| P1-24 行号缺失 | 采纳 | 全文事实均带路径+行号 | 全篇 |
| P1-25 R-UI 条目需重列 | 采纳 | 32 条逐条落点 + 判定方式 | §6.7 |

### 13.3 P2（14 条）

| 编号 | 处理 | 理由 | 位置 |
|---|---|---|---|
| P2-1 proto 数 78→75 | 采纳 | 实际 75 | B34、§3.1 |
| P2-2 超时精确值 | 采纳 | 120/8/15/8/60/300/30/120 | §5.3.1、B10–B12 |
| P2-3 事件总线不是 NATS | 采纳 | v1 进程内 + 落库；NATS 留到多实例 | §3.5 |
| P2-4 “裁剪 4 类 + 新增 1 项”表述 | 采纳 | 改为三分法（Go/外部/不做） | §2.1 |
| P2-5 token 行号 | 采纳 | 引用 `tokens.css:8-45` | B32、§6.7 |
| P2-6 tokens.css 实际更长 | 采纳 | 只引颜色段，不声称“全文 107 行” | B32 |
| P2-7 `NtPanel` 不存在 | 采纳 | 改为 `NtStatCard`/`NtTimeline`（`display/`） | B33、§6.7 |
| P2-8 12 组 key | 采纳 | 逐组字段表 | §6.8 |
| P2-9 健康字段 | 采纳 | 补 `feed`/`recording` | §4.9 |
| P2-10 采样窗口 | 采纳 | 8h、每 100ms 采样 | §4.3.3 |
| P2-11 topic→store 映射 | 采纳 | §6.4/§6.5 表 | §6.4 |
| P2-12 设置模板 | 采纳 | 12 组 + 生效时机矩阵 | §6.8 |
| P2-13 压测机器 | 采纳 | i7/32GB/无其他负载 | §4.3.3、§10.1 |
| P2-14 local/prod 边界 | 采纳 | 三模式（local/lan/collector） | §3.9 |

### 13.4 未决事项（不在本轮关闭）

| 事项 | 状态 | 关闭时间 |
|---|---|---|
| managed token 是否可续 | ⏳V4 | M1 末 |
| TRTC UserSig 是否完全一致 | ⏳V5 | M1 末 |
| Quasar 与 R-UI-12 的偏差是否获批 | 待评审拍板 | M0 前 |
| 是否保留复盘页（F50） | 待产品确认 | M0 前；若裁掉，工期 −1 周 |

---

## 14. 修订记录

| 版本 | 日期 | 变更 |
|---|---|---|
| v1.0 | 2026-11-03 | 初稿（10–14 周） |
| v2.0 | 2026-11-03 | 按独立深审修订：P0×10 / P1×25 / P2×14 全量处理；修正前端规范/心跳/重复开播/断言臆造；新增鉴权、边车契约、Vidu 错误码、Go 节奏与 Windows 定时器、喂流权状态机、幂等/崩溃恢复、可观测性、clean-room 证据链、V 表；验收表改为可测口径；工期 16–20 周 |
| v2.1 | 2026-11-03 | 验收整改同步（不改结构）：§3.5 明确 seq 按场次单调与 epoch 语义、REST/WS 同源（`Bus.SetAppender`）、降级期最小 outbox 与 4096 条丢失边界、事件契约单一事实来源；§3.7 RTC 行改为已实现的 `GetRtcToken`；§3.8 补 fail-closed 引擎模式（503 `engine_not_configured`）；§4.8 补 `disabled/dev/managed` 实际行为 |
| v2.2 | 2026-11-03 | 验收整改 A 批（两路代码审查报告 P1/P2 处置）：采纳并修复后端 P1-2/6/7/9/10/11/12/13/14(部分)/15/16/17/19(部分)/20 与 P2-1~13，前端 P1-1~9/12~17/19/20 与 P2-1~19/21；不采纳与部分采纳项见 §15 处置清单，均含理由与风险 |
| v2.3 | 2026-11-03 | 实现批次完成同步：B 批真实适配器（Vidu HTTP/WS 客户端 + Feeder + 喂流权 + 看门狗 + OmniVoice 边车）、C 批持久化（Ent 11 表 + switchable sink + 最小 outbox + Redis ticket/幂等/nonce）、D 批验证（故障注入 ×4 + feeder 漂移长跑 + E2E，结论见 §16）、E 批文档（`digital-human/docs/architecture.md`、`ops.md`、`verification.md` 三件套）均已完成；新增「最终状态总览」、§16、§17 |

---

## 15. 验收审查处置清单（A 批，两路报告 P1/P2）

> 仅收录**不采纳 / 部分采纳**项及需在文档留痕的语义裁决；其余条目均为「采纳」并已落码，证据见当批报告。

### 15.1 后端

| 条目 | 处置 | 理由 / 风险 |
|---|---|---|
| P1-6 `ResetLlm` 记忆清零 | 部分采纳 | Interrupt/StopActions/ClearActions 语义已收敛（中断发言 / 中断动作语音 / 仅清队列，单次事件）；ResetLlm 保留计数=0：LLM 模块未交付（P1-18），无记忆可清。**风险**：LLM 上线后忘记接线 → 交付 LLM 时必须把 ResetLlm 接入记忆存储（已列入 M2 清单） |
| P1-9 弹幕 LLM 工位（auto/worker 进 LLM、VIP/节奏） | 部分采纳 | 已采纳：结构过滤（控制符/零宽/长度 100 runes/重复折叠）、pending 上限 200 丢最旧、模式落库。不采纳 LLM 路由：LLM 未交付，现在写路由是假功能。**风险**：弹幕 auto 模式下暂不产生回复；M2 交付 LLM 时补齐 |
| P1-14 面板响应信封统一 | 不采纳 | panels 51 条路由的 `{ok,error}` 信封前端已全量依赖；改写为 proto 信封需前后端同步大改且收益仅风格统一。**风险**：两套信封长期并存，新接口必须沿用 `{ok,error}` 并在评审时说明 |
| P1-18 缺失核心模块（LLM 编排/礼物/积分/暂停/动作网关拆分） | 不采纳（本期） | 属 M2/M3 功能排期而非缺陷修复；F36 额度轮询落账、F34 云录制在 B/C 批按用户指令交付。**风险**：M2 前功能缺口仍在，验收口径需按批次核对 |
| P1-19 `wsConns` 死字段 | 部分采纳 | `ws_total` 已接真实 gauge；`ws_conns` 数组删除（前端用 `ws.state`，无人消费该字段） |
| P2-14 type14 硬推 | 部分采纳 | 校验已改为实推（FakeSignaler 去掉硬编码 type15），真实 type14 依赖 Vidu 信令联调，列入待真机验证 |

### 15.2 前端

| 条目 | 处置 | 理由 / 风险 |
|---|---|---|
| P1-15 Ctrl+K 命令面板 | 不采纳 | v1 无命令注册表，做空壳面板是假功能；已交付 F9 开播 / F10 打开下播确认（显式确认后停止，Esc/取消保持 live）/ `/` 聚焦 + 命令前缀。**风险**：无；后续若做命令面板需先有注册表 |
| P1-20 dev/prod 冒烟 | 部分采纳 | 已补 `app.test.ts` 挂载冒烟（`#q-app` 非空）与 events/live store 状态机测试；Playwright 级 dev/prod 冒烟列入 D 批 E2E |
| P2-6 CSP/内联脚本冲突 | 部分采纳 | index.html 已加 CSP meta 与同步主题脚本；`script-src` 含 `'unsafe-inline'`（同步脚本必需），**风险**：XSS 防线弱一层，后续可把脚本哈希化 |
| P2-16 导出超过 10 万行 | 不采纳 | 后端导出上限 10000 事件且已显式 `truncated=true`；10 万行场景不存在。**风险**：上限调整时需重估 |
| P2-20 升级 quasar/jsdom | 不采纳 | 验收冻结期不升依赖（`overrides` 在 jsdom 22 上未实测）。**风险**：npm 审计 20 个漏洞仍在，交付前需专项升级窗口 |

### 15.3 语义裁决（两路审查分歧/易错点，已锁定）

1. **崩溃恢复时间窗**：Recover 判据是「归属」不是时间窗——除当前进程在场 session 外，所有 `starting/live/stopping` 行均标记 `crash_recovered`（含刚更新几秒的场次）；原 60s 窗口废弃（会把刚崩溃场次漏成幽灵 live）。落点：`internal/biz/live/live.go` `Recover`、README「崩溃恢复」段、`TestRecoverMarksAllUnfinishedExceptCurrentSession` / `TestRecoverSkipsCurrentSession`。
2. **口播拆分保护规则**：`protectRE` 复刻蓝本 `_PROTECT`（英文单词/URL/数字不可切断），整段单 token 文本任何内部切点都报 `ErrInvalidSplit`（诚实拒绝），不为放行短英文串改规则。落点：`split.go`、`TestSplitSegmentUniqueIDs`（改用可切分词组）+ `TestSplitSegmentRejectsUnbreakableToken`（锁定语义）。
3. **字幕归并键**：partial/final 双方都带 `utterance_id` 时按 id 归并（交错发言不串行），任一方缺 id 退回 `index`。落点：`web/src/stores/transcript.ts` `sameUtterance`。

---

## 最终状态总览（v2.3）

> 证据锚点：`digital-human/docs/verification.md`（D 批实测）、`digital-human/docs/architecture.md`、`digital-human/docs/ops.md`。数值均为实测，未回填估计值。

### A. 已实现及验证状态

| 项 | 状态 | 证据锚点 |
|---|---|---|
| 引擎三模式 fail-closed（`disabled/dev/managed`，未配置一律 503 `engine_not_configured`） | ✅ | `docs/ops.md` §1；§3.8 |
| Vidu 适配器（HTTP/WS 客户端）+ Feeder 48B/ms 喂流 + 喂流权状态机 + 看门狗 | ✅（dev 路径；真机联调见 B 栏） | `docs/architecture.md` §6.1–6.4；§4.2–4.6 |
| OmniVoice Python 边车（薄 HTTP 契约：wav + 取消 + 健康 + 并发=1） | ✅（契约路径） | `docs/architecture.md` §6；§5.2 |
| Ent 11 表 + switchable sink + 最小 outbox（PG 掉线切内存、恢复补写） | ✅ | `docs/architecture.md` §3、`docs/ops.md` §4；§7.1 |
| Redis ticket / 幂等 / nonce（缺失时降级语义明确） | ✅ | `docs/ops.md` §4；§3.10 |
| 额度轮询落账（F36） | ✅ | §15.1 P1-18 处置；`docs/ops.md` |
| 崩溃恢复归属判据（除当前进程在场 session 外全部 `crash_recovered`，60s 窗口废弃） | ✅ | `docs/architecture.md` §5；§15.3.1 |
| WS ticket 一次性 + 断线 `after_seq` 补发（无丢失无重复） | ✅ | `docs/verification.md` §1 `TestWSReconnectAfterSeqBackfillsMissedEvents` |
| 故障注入 4 条（WS 补写 / Recover 接管 / 边车熔断 / PG 降级恢复，各 `-race` ×10 稳定） | ✅ | `docs/verification.md` §1 |
| Feeder 真实时钟 1h 漂移长跑 | ✅ | `docs/verification.md` §2.1：**120 轮 / 36000 帧 / 172800000 字节 / 迟到 0 / 告警 0 / 平均轮末漂移 474.17µs / 实测 3624s** |
| Feeder 加速 8h 等效 | ✅ | `docs/verification.md` §2.2：**288000 tick / 960 轮 / 墙钟 4m48.5s / 单轮最大滞后 3.7149ms** |
| E2E（Playwright 真实浏览器，dev 引擎）全链路 | ✅ 2/2 通过，退出码 0 | `docs/verification.md` §3：F9/按钮均先 preflight 后 Start；F10 打开下播确认，Esc/取消保持 live，显式确认后停止；事件 seq 1–8 连续 |
| WS 1h 真实时钟长跑 `TestSoakWSEventBus` | ✅ | `docs/verification.md` §5：**242 事件 / seq 连续至 242 / 断开 0 次 / 36001 样本最大抖动 2.0066ms / 实测 3630s** |

### B. 待真机验证（无真实凭据/环境，fail-closed 下明确不可用）

| 项 | 阻塞原因 | 落点 |
|---|---|---|
| Vidu 真实建场 / 心跳 / 断流重连 | 无真实 Vidu Key 与场次 | §4.11 V 表 |
| OmniVoice 真实合成 | 无真实边车模型服务 | §5.2 |
| ffmpeg 解码播放 | 本机 ffmpeg 不在 PATH | §2.3 |
| 云录制出片（F34） | 依赖 managed 场次真实录制产物 | §15.1 P1-18 |
| PG 真实落库与 Recover 演练 | 本批 PG 路径为注入模拟 | `docs/verification.md` §1 |
| Redis 持久（非降级） | 无 Redis 实例 | `docs/ops.md` §4 |
| RTC managed 真实订阅 | 无平台凭据 | §4.8 |
| 8h 墙钟整机长跑（feeder+WS 联合，真实引擎） | 需真机时段与真实凭据 | `docs/verification.md` §2.2 注 |

---

## 16. D 批验证结论

> 单一事实来源：`f:\myproject\teamwork\digital-human\docs\verification.md`；本节只索引与摘录，数值改动以该文件为准。

### 16.1 故障注入 4 条（各 `-race` ×10 稳定）

| 场景 | 测试 |
|---|---|
| WS 断连按 `after_seq` 补写遗漏事件 | `TestWSReconnectAfterSeqBackfillsMissedEvents`（`internal/server/ws_reconnect_test.go`） |
| 引擎崩溃后新 Manager 接管（标记 `crash_recovered`，不自动恢复） | `TestRecoverAfterCrashNewManagerTakesOver`（`internal/biz/live/recover_takeover_test.go`） |
| OmniVoice 边车挂起时超时生效并触发熔断 | `TestSidecarHangTimeoutTripsBreaker`（`internal/engine/tts/breaker_timeout_test.go`） |
| PG 掉线切内存、恢复后切回 Ent 并补写降级期事件 | `TestSwitchableSinkPGOutageDegradeAndRecovery` + `TestSwitchableCostStoreDegradeThenSwitch`（`internal/data/repo/switchable_outbox_test.go`） |

### 16.2 本批修复的两个真实缺陷

1. **WS 握手 500（真机）**：`auditFilter` 的 `statusRecorder` 未透传 `http.Hijacker`，gorilla Upgrade 取不到 Hijacker 直接 500。修复：recorder 实现 Hijack 透传；回归测试 `TestWSHandshakeThroughProductionFilters`（`internal/server/ws_long_test.go`）走完整生产 filter 链做握手。
2. **`TestContextCancelPropagatesToSidecar` 偶发失败**（`internal/engine/tts/synth_test.go`）：测试在边车尚未入队（`statusQ` 为空）时就 cancel，断言路径随机落空。修法：等 `statusQ > 0` 再 cancel，覆盖真实传播路径。

### 16.3 数值项

Feeder 长跑与 E2E 数值见「最终状态总览」A 栏；WS 1h 长跑已完成（242 事件、seq 连续、断开 0、最大抖动 2.0066ms，见 `docs/verification.md` §5）。

---

## 17. F/G 批范围（进行中）

### 17.1 F 批：验收覆盖补齐

- 覆盖 review 的 **41 条部分/未覆盖**项，另加表外新发现：`/api/chat`、`/api/record` 用户控制面、`/api/credits` 实时额度、6 个 localStorage 键。
- 不采纳项（留痕）：
  - **G09「开场白文件优先」**：经全库 grep 证实蓝本无此机制，判报告误判，不采纳。
  - `rndEvery` / `rndBias` / `cmdEdits` / `songPath`：蓝本历史遗留键，不采纳。

### 17.2 G 批：形象配置页 v3

- 范围 M1–M5，依据 `temp/digital-human/plan/avatar-config-plan-v3-2026-11-03.md`。
- 进行中，完成标准与验收口径以该方案为准。




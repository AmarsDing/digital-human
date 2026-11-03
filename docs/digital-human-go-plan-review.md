# 数字人直播项目 Go 克隆方案 · 独立深度评审

> 评审对象：`digital-human-go-plan.md`（F01–F51 对照表、目录骨架、proto 清单、里程碑、P0/P1 风险、V1–V8 待验证项）
> 功能蓝本：`cyber-xiaoya-live-main`（Python/FastAPI/单页 stage/Vidu/OmniVoice）
> 架构参照：`HotCraftFlow`（Go+Kratos+Ent+Wire+Proto+Asynq）
> 前端规范：`NovaTrader/doc/开发文档/前端UI/README.md`、`NovaTrader/web/src`
> 评审方式：只读核对源码与规范；行号均为本次实际核验所得。此前 `cyber-xiaoya-live-main-aranea-plan-review.md` 中判为“不成立/夸大”的结论未反向引用。

---

## 0. 总体判定

**结论：修订后可实施（当前版本不得直接进入实现）。**

方向是对的：HTTP+WS、不引入 gRPC、单进程单场、OmniVoice 保留 Python 边车、ffmpeg 外置、视频订阅留在浏览器、Ent/Wire/Kratos 分层、`pkg/blob` 规避 HFC 的 `storage/` 坑，这些都和蓝本与 HFC 的实际结构一致。

但当前文档存在三类硬问题：

1. **校验性事实错误**：§6.7 的 NovaTrader 规范与 Token 值大面积张冠李戴；F04 心跳参数与 §4.1 自相矛盾；§12.1 的路由数、设置组数、面板数均与源码不符。
2. **行为臆造**：F01“重复开播被拒”、F24“弹幕去重”、F26“t0 > t2”、F49“token 临近过期刷新”在蓝本中不存在或语义相反。
3. **重项被两句带过**：鉴权、密钥存储、录音 30 分钟轮询、歌词打轴生产链、边车取消/超时/重启、喂流权状态机、崩溃恢复，均没有可实施设计。

进入实施的门槛：关闭 P0-1～P0-10，重写 F01–F51 中 8 个 ❌ 行与 24 个 ⚠ 行，补齐“安全与鉴权”整章，并给 V1–V8 加上时间点/负责人/门禁。

---

## 1. 实际核验基线（本评审的事实底座）

| 事实 | 实际值 | 证据 |
|---|---|---|
| bridge.py 路由数 | **63** 个 `@app.*`（不是方案 §12.1 的 70） | `src/bridge.py` 全文计数 |
| koubo.py 路由数 | **14** 个 `@router.*`（不是“14 个口播动作”） | `src/koubo.py:306-...` |
| Vidu WS 心跳 | `ping_interval=10`、`ping_timeout=20` | `src/vidu/signaling.py:84-86` |
| conn_init 重试 | 15 次 / 2s | `src/vidu/signaling.py:126-148` |
| 断流判据 | 75s 无回包 + 20s 采样窗口 + 0.55x 实时推流阈值 | `src/engine.py:433-490` |
| 重连 | 最多 6 次、固定 20s 间隔；**无 60s 冷却** | `src/engine.py:433-490` |
| 闲置/硬上限 | 300s / 1800s | `src/engine.py:607-646` |
| 创建会话超时 | **120s** | `src/vidu/client.py:66` |
| 查询/录播/列表/额度超时 | 8s / 15s / 8s / 8s | `src/vidu/client.py:72,77,83,91` |
| 列表分页参数 | **`pager.page` / `pager.pagesz`**（不是 `page`/`size`） | `src/vidu/client.py:86` |
| 三步上传第 2 步 | PUT presigned，**不带鉴权头** | `src/vidu/client.py:8,102-105` |
| 创建 403 动作库降级 | 去掉 `action` 重建一次 | `src/engine.py:321-335` |
| 录播 | managed RTC 才录；轮询最长 30 分钟，15s→30s→60s | `src/engine.py:341-346,707-758` |
| 喂流 | 100ms 分片、3s 单帧超时、落后 >300ms 告警、不跳帧 | `src/engine.py:1703-1761` |
| 动作优先级 | `vip=3 > manual=2 > choreo=1 > 弹幕/t0/t2/auto/confirm=0` | `src/engine.py:1168-1171` |
| 礼物合并 | 6s 窗口；≥4 人或 ≥50 钻立即；最多 4 个名字 | `src/engine.py:145-167` |
| 弹幕缓冲 | 最近 8 条、`read_interval_s` 默认 4s、20s 播报闸门；**无按文本去重** | `src/dm_router.py:900-918`、`src/engine.py:2385-2392` |
| 分句 | `soft_min=10`、`hard_max=45`；合并 `min_chars=30`、`first_min=14` | `src/llm.py:47-117` |
| TTS 队列 | `maxsize=8`；存在“整段模式” | `src/engine.py:1568,1669` |
| 状态接口 | `status(since)` 的 `since` 是**字幕下标**，不是全局事件序号；无 `tr_list` 路由 | `src/engine.py:3006-3075`、`src/bridge.py` 全文 |
| 设置分区 | **12 个**（会话/声音/专属会员/人设/LLM/自备 RTC/动作库/资产库/Key/字幕层/弹幕主播模式/其他） | `stage/index.html:683-837` |
| stage 规模 | 3534 行 / 210,162 字节 / **128 个具名函数** / fetch 轮询、无 WebSocket | `stage/index.html` |
| localStorage | 7 个键：`s2s.stage.cmdEdits`、`s2s.stage.cmdFx`、`s2s.stage.rightW`、`s2s.stage.rndBias`、`s2s.stage.rndEvery`、`s2s.stage.stats`、`s2s_songPath` | `stage/index.html` |
| 歌词 | 运行时读 `lyrics-cache/*.lrc.json`，否则同名 `.txt`；打轴由 `tools/lyrics_align.py` 离线生成 | `src/engine.py:1857-1931`、`tools/lyrics_align.py` |
| NovaTrader 实际 Token | `--nt-bg-0:#050d17`、`--nt-bg-1:#0b1b2b`、`--nt-accent:#45e0ff`、`--nt-text-0:#eef6fa`、`--nt-text-3:#85a5bc` | `NovaTrader/web/src/theme/tokens.css:8-23` |
| HFC proto 数 | **75**（不是 78） | `apps/server/api` 计数 |
| HFC `pkg/storage` | 目录不存在、未被 git 跟踪，但 `data.go:20` 有 import → 不可构建 | `.gitignore:16-19`、`apps/server/app/admin/internal/data/data.go:20` |
| HFC Wire | 只有 `app/admin/cmd/wire_gen.go`，media 无 wire 生成物 | 全仓 `wire*.go` |
| HFC gRPC | 全仓无 `RegisterGRPCServer` | 全仓检索 |

---

## 2. F01–F51 逐项判定

判定口径：
- ✅ 覆盖准确、落点可行、验收可测；
- ⚠ 部分准确/落点可行但验收含糊或缺关键分支；
- ❌ 事实错误、行为臆造、或验收与蓝本相反；
- ⚪ 覆盖存在但验收不可测。

| ID | 判定 | 蓝本证据（实核） | 主要问题 / 备注 |
|---|---|---|---|
| F01 开播 | ❌ | `engine.py:318-346`；`engine.py:305-306` | 蓝本重复开播是 **`stop("reconnect")` 后重开**，不是 `ALREADY_LIVE`；方案 §3.8 的拒绝语义与之相反。另：验收“10s 内 live”与创建超时 120s 冲突。 |
| F02 收播 | ✅ | `bridge.py` live/stop；`engine.stop` 发 hangup 后 0.4s finalize | 5s 目标可测；需补“starting 阶段 Stop”的行为。 |
| F03 状态/增量 | ❌ | `engine.py:3006-3075` | 蓝本 `since` 是**字幕下标**，不是事件 seq；方案 `since_seq` 是另一套游标。蓝本只回最近一段字幕，“序号不丢”不成立，除非入库。 |
| F04 WS 生命周期 | ❌ | `signaling.py:84-86` | 心跳是 **10s/20s**，F04 写“5s ping/15s pong”，§4.1 又写“10/20、约 15s”，三处不一致。 |
| F05 断线/看门狗 | ⚠ | `engine.py:433-490,607-646` | 75s 判据是“无回包 + 20s 窗口 + 0.55x 速率”，F05 简化为“无发送/接收”；“6 次/20s”应为“6 次、每次间隔 20s”；§4.5 的 60s 冷却无出处。 |
| F06 异常清理 | ✅ | `engine.finalize` | Go 侧可用 goleak 验证；需明确“重复事件”用唯一键去重。 |
| F07 创建会话 | ⚠ | `client.py:66-69`、`engine.py:318-335` | body 清单缺 `managed_rtc`/RTC 配置（只在 F34 露出）；**403 动作库降级**（engine.py:321-335）完全没写。 |
| F08 查询/录播/额度/列表 | ⚠ | `client.py:72-96` | 分页参数名错（`page/size` vs `pager.page/pagesz`）；四类端点超时值（8/15/8/8）未列。 |
| F09 资产/头像/动作库 | ⚠ | `client.py:99-198` | 三步上传第 2 步 PUT **不能加鉴权**；图片资产 90 天平台自动删（client.py:140）未提；“失败可回滚本地记录”是新逻辑，需定义。 |
| F10 WS 信令 | ✅ | `signaling.py:39-52,90-124,150-194` | 类型 1/2/5/6/7/9/10/11/12/14 齐；需补 type=-1 补事件。 |
| F11 24k s16le | ✅ | `audio.py:16-17` | 48B/ms 明确。 |
| F12 节奏/漂移 | ⚠ | `engine.py:1703-1761` | §4.3 的“>0.25s 跳过 sleep”在蓝本不存在（蓝本只在 `delay>0` 时 sleep）；“8h 漂移 <1 分片”缺测量条件（Windows/负载/分片大小）。 |
| F13 喂流权抢占 | ⚠ | `engine.py:831-938` | 漏 `respect_song`、4 轮等待、卡死持有者强制取消；“手动聊天抢占歌曲”与 `respect_song` 语义不符。 |
| F14 发送超时 | ✅ | `engine.py:1703-1761` | 3s、不重发一致。 |
| F15 LLM 流式 | ⚠ | `llm.py:47-98` | “流中断可回退”在蓝本未验证；首句延迟只有“可测”，无阈值。 |
| F16 短句合并 | ✅ | `llm.py:99-117` | `soft_min/hard_max/min_chars/first_min` 与蓝本一致；需补“去 Markdown/表情”的实际范围。 |
| F17 TTS 队列 | ✅ | `engine.py:1568,1669` | `maxsize=8`、逐句/整段两种模式均有。 |
| F18 整段/音频文件 | ⚠ | `bridge.py:548-566`；`engine.py:1857-1931` | 漏 `script` 字幕、同名 `.txt` 兜底、点播即置歌曲优先（engine.py:1902-1907）。 |
| F19 type10 | ✅ | `signaling.py:39-44` | 仅上报文本，字幕本地叠加，准确。 |
| F20 音色/预览/文件 | ✅ | `bridge.py:786-823`、`/api/tts/files` | 路由与行为一致；需补导入目录/同名去重规则。 |
| F21 弹幕接入 | ⚠ | `bridge.py:722-766` | 蓝本**永远 200 + reason**（idle/filtered/latest/vip），方案改成“非法 body 被拒”；礼物与弹幕在蓝本是同一入口，方案拆成 `/gifts:ingest`，采集器兼容性未处理。 |
| F22 T0 匹配 | ✅ | `dm_router.py:536-597` | 黑名单/`auto_only_words` 存在；需把参数写进验收。 |
| F23 T2 模式 | ✅ | `dm_router.py:8,770,886-892` | off/confirm/auto 映射正确；`set_config` 只接受 confirm/auto，off 靠 `enabled=false`。 |
| F24 去重/风控/批读/TOP10 | ❌ | `dm_router.py:105-109,562,900-918`；`engine.py:2385-2392` | **没有按文本去重**；只有最近 8 条缓冲、4s 批读、20s 播报闸门、`last_fired`/6s 建议限速。“重复只触发一次”无实现依据。 |
| F25 VIP 优先 | ⚪ | `dm_router.py` VIP 队列 | 队列存在，但“延迟显著低于普通”不是可测指标。 |
| F26 动作优先级 | ❌ | `engine.py:1168-1171` | 蓝本是 `vip 3 > manual 2 > choreo 1 > 其他(含 t0/t2/confirm/auto) 0`；方案“t0 > t2 / confirm 属 manual”是臆造。 |
| F27 礼物合并 | ⚠ | `engine.py:145-167` | 6s 窗口、≥4 人或 ≥50 钻、最多 4 名、25s/10s 间隔均未写进验收。 |
| F28 礼物欢迎/贵宾 | ⚠ | `engine.py:1168-1171` 注释“99 钻贵宾” | 阈值、触发一次、可关闭均未给参数。 |
| F29 贵宾名单 | ✅ | `bridge.py` patrons 路由 | CRUD/join/welcome 一致。 |
| F30 编排 | ✅ | `/api/choreo`、`engine.py:1417` | VIP 时跳过 choreo 确实存在（1417）。 |
| F31 口播 | ⚠ | `koubo.py` 14 个 `@router` | “14 个口播动作”是错的，14 是路由；蓝本 `project.json` 持久化→DB 的迁移未定义。 |
| F32 歌曲点播 | ⚠ | `engine.py:1857-1931`、`tools/lyrics_align.py` | 歌词缓存的生产链（离线打轴工具）未纳入；“3s 内起播”没算 ffmpeg 解码长音频。 |
| F33 参考图 | ✅ | `bridge.py:1312-1345` | 1–3 张、switch/remove、失败 type14 一致。 |
| F34 录播 | ⚠ | `engine.py:341-346,707-758` | 漏“最长 30 分钟轮询、15s→30s→60s”；漏“customer RTC 不录”。 |
| F35 人工流水/事件 | ❌ | `bridge.py` 无 `/tr_list`；`engine.py:3006-3075` | `tr_list` 不存在；`since` 只是字幕下标；蓝本无按场次持久化回放。 |
| F36 统计/成本 | ⚠ | `engine.py:3006-3075`、`bridge.py:79-124` | 蓝本只有 Vidu credits 轮询（30–60s、开播基线重置）与 stats；TTS/LLM 成本拆分无数据源。 |
| F37 设置聚合 | ⚠ | `bridge.py:842`、`stage/index.html:683-837` | 实际 **12 组**；漏“弹幕主播模式”“资产库”；“保存即生效/需重启”的逐字段矩阵未给。 |
| F38 人设 | ⚠ | `/api/personas` | 漏“切换后清空对话记忆”（对应 `/api/llm/reset`）。 |
| F39 头像 | ✅ | `/api/avatars` | 上传/启用/删除一致。 |
| F40 动作库 | ✅ | `client.py:157-198` | 库/动作 CRUD 一致。 |
| F41 音频素材 | ✅ | `/api/tts/files` | 列表/上传/导入/删除一致。 |
| F42 Vidu Key | ⚠ | `bridge.py:1458-1503` | 多 key 存储/激活正确；漏“页面只见尾 8 位”的掩码与轮换验收。 |
| F43 舞台主区 | ✅ | `stage/index.html`（隐私/mask/日志） | 视频/字幕/隐私遮罩/日志/转写条均有；需补“重连中”视觉状态。 |
| F44 右栏面板 | ⚠ | `stage/index.html` 面板区 | 漏贵宾/编排/口播三块（对应 `/api/patrons`、`/api/choreo`、`/api/koubo`）。 |
| F45 底部输入条 | ⚠ | `stage/index.html`（keydown） | 有快捷键；未列按键表与“静音/打断”的实际范围。 |
| F46 设置抽屉 | ❌ | `stage/index.html:683-837` | **12 组**写成 11 组；漏“弹幕主播模式”；“资产库”被并入“头像”。 |
| F47 面板 | ⚠ | `stage/index.html` | `VoicePanel` 在蓝本不是独立面板（音色在设置组）；`PatronPanel` 未列。 |
| F48 本地预设 | ⚠ | `stage/index.html` localStorage 7 键 | 只覆盖 `cmdEdits`；漏 `rndEvery/rndBias/stats/cmdFx/rightW/s2s_songPath`。 |
| F49 RTC 播放器/token | ❌ | `rtc_tokens.py:1-20`、`engine.py:340-430` | 蓝本无 token 刷新端点；“临近过期刷新”无实现路径；customer RTC 的 ARTC/TRTC 算法未写进 Go 设计。 |
| F50 复盘页 | ⚠ | 蓝本无 | 新增功能，不是克隆；依赖事件入库与保留策略，工期未计入。 |
| F51 健康/指标 | ⚠ | `bridge.py:341-349` | 蓝本 health 含 `build/song_playing/danmaku_pending`；方案只写“健康状态可视”。 |

统计：✅ 18、⚠ 24、❌ 8、⚪ 1。

---

## 3. 遗漏行为穷举（对照 F 表）

| # | 异常/边缘路径 | 蓝本实际行为（证据） | F 表覆盖 | 判定 | 必须补的内容 |
|---|---|---|---|---|---|
| 1 | 建场失败/积分不足 | 创建 120s 超时；`/ent/v2/credits` 8s；开播前不预检 | F01/F08 | ⚠ | 预检额度、402/额度不足提示、失败回滚、错误码 |
| 2 | 动作库 403 | 去掉 `action` 重建一次并提示 | F07/F09 | ❌ | 降级分支写进 F07；验收“换 key 后仍能开播” |
| 3 | RTC token 过期/刷新 | 只下发 `token_expire_at`；无刷新端点 | F49 | ❌ | 明确“managed 不支持续期”，customer 自签；删掉刷新验收 |
| 4 | 双控制面互相打断 | `_own_audio` cancel + type7 + 250ms | F13 | ⚠ | 定义 stage 手点/弹幕/编排/口播的仲裁矩阵 |
| 5 | TTS 合成失败/超时/取消 | `synth_pcm` 失败抛错；全局锁串行；无 HTTP 取消契约 | F17 | ❌ | 边车 cancel/timeout/并发/重试/降级预录音 |
| 6 | 单句过长/分句边界 | `hard_max=45`；合并 30/14 | F16 | ✅ | 补“40+ 字无标点”回归用例 |
| 7 | 弹幕洪峰 | 最近 8 条、最新优先、`dropped` 计数 | F24 | ❌ | 有界队列 + 丢弃策略 + `dropped` 指标；删除“去重” |
| 8 | 动作抢占/排队 | `_act_q` 按优先级插入、同级 FIFO、冷却/间隙 | F26 | ⚠ | 队列上限、满时丢弃策略、冷却参数 |
| 9 | 礼物/贵宾插队 | 6s 窗口、紧急阈值、VIP 队列 | F27/F28 | ⚠ | 参数与“VIP 间隔” |
| 10 | 打断/静音 | `_barge_in`、type7；stage 无“静音” | F13/F45 | ⚠ | 区分“打断当前句/清空队列/停动作”三种语义 |
| 11 | 音画不同步 | 蓝本只保证上行节奏；无 A/V 测量 | F12 | ⚠ | 定义测量点（首字延迟、录音回环）、阈值、失败处理 |
| 12 | 进程崩溃恢复 | 无断点续播；重启后只能重开 | F06/F50 | ❌ | 崩溃后状态落库、孤儿 live 清理、重启策略 |
| 13 | 重复开播/收播幂等 | 开播先 stop 再重开；收播可重复 | F01/F02 | ❌ | 改 F01 验收；明确 Stop 在 starting/stopping 的行为 |
| 14 | 配置热更新 | `/api/settings/reload`；部分字段需重启 | F37 | ⚠ | 逐字段“热应用/需重启”矩阵 |
| 15 | 密钥轮换 | 多 key 保存/激活/删除；尾 8 位显示 | F42 | ⚠ | 轮换流程、失效检测、审计 |
| 16 | WS 心跳/pong | `ping_interval=10`、`ping_timeout=20` | F04 | ❌ | 修正常量；明确 coder/websocket 的 ping 行为 |
| 17 | 断线中的 UI/状态 | 蓝本页面轮询 status | F43/F49 | ⚠ | `reconnecting` 阶段、遮罩、禁用按钮 |
| 18 | LLM 记忆重置 | `POST /api/llm/reset` | 无 | ❌ | 加 `ResetLlm` RPC；人设切换联动 |
| 19 | 开场白 | 设置项“开场白”；就绪后先说 | 无 | ❌ | 补 F 行或并入 F01 验收 |
| 20 | 按句 type10 开关 | 设置项“输出按句上报” | F19 | ⚠ | 开关字段与行为 |
| 21 | 资产 90 天回收 | 图片资产平台 90 天自动删 | F09 | ❌ | 生命周期提示/本地缓存策略 |
| 22 | 积分轮询/基线 | 30–60s 轮询、开播重置基线 | F36 | ⚠ | 轮询周期、基线、异常显示 |
| 23 | 歌词打轴生产链 | `tools/lyrics_align.py` 离线生成缓存 | F32 | ⚠ | 明确工具归属/是否保留 Python/缓存失效 |
| 24 | 随机表演 | `rndEvery/rndBias` + `/api/cmd` | 无 | ❌ | 新增 F 行；参数与开关 |
| 25 | 指令特效/统计面板布局 | `cmdFx`、`stats`、`rightW` | F44/F48 | ⚠ | 纳入 UI 状态持久化 |
| 26 | 弹幕主播模式设置组 | 12 组中的一组（VIP 间隔等） | F46 | ❌ | 修正组数并补字段 |
| 27 | 健康字段 | `build/song_playing/danmaku_pending` | F51 | ⚠ | 字段与 `/healthz` 契约 |
| 28 | `dmcmd/toggle` | 模式切换路由 | F23 | ✅ | 已覆盖 |
| 29 | 录制轮询 | 30 分钟、15/30/60s | F34 | ❌ | 写进验收与 Asynq 任务 |
| 30 | 歌曲优先置位 | 点播受理即 `_song_playing=True` | F32 | ⚠ | 抢占窗口与旧歌 epoch |

---

## 4. Go 实时音频可行性硬审

### 4.1 48B/ms 在 Windows 下如何保证

- 蓝本用 `loop.time()` 绝对时基 + `await asyncio.sleep(delay)`，每 100ms 一片（`engine.py:1703-1761`）。
- Go 用 `time.NewTimer` + 绝对 deadline 是正确方向；`time.Ticker` 会累积漂移，方案 §4.8 已写明，判断正确。
- Windows 下真正的问题不是 `time.Timer` 的标称精度，而是：
  - **唤醒延迟**：高负载/杀软/电源节流下 p99 可能 5–15ms；100ms 分片有 10 倍余量，但要按 p99 验收，不能只看平均。
  - **GC 停顿**：喂流 goroutine 被 STW/辅助 GC 拖住；需要预分配缓冲、避免每帧分配、必要时在压测中固定 `GOGC` 观察。
  - **调度**：单 writer goroutine + `GOMAXPROCS>=4`；不要每帧起 goroutine。
  - **写阻塞**：`coder/websocket` 的 `Write` 是阻塞的；一帧卡住会连带后续 30 片。方案“3s 超时后终止本轮”是对的，但必须同时取消上游 TTS 生产，否则队列继续堆积。
- 建议的验收：`feed_lag_ms` 的 p50/p99/max、`send` 耗时 p99、8h 漂移（首片与末片的 deadline 差）、GC pause 分布，全部在 Windows 目标机采集。方案只写“8h 漂移 <1 分片”，不足以证明。

### 4.2 喂流走 HTTP chunked 还是 WebSocket 二进制

- 蓝本是 **WS 二进制裸 PCM**（`signaling.py:150-194`），不是 HTTP chunked。方案选 `coder/websocket` 正确；不要提供 HTTP chunked 备选，Vidu 不认。
- Go 侧必须显式关闭压缩（避免 CPU 抖动与帧变大），并串行化所有写（JSON 与 PCM 共用一把锁/单 writer）。方案 §4.2 提到了串行化，但没提压缩与缓冲复用。

### 4.3 漂移校正公式与阈值

蓝本公式（`engine.py:1712-1757`）：

```
t0 = now
played_ms = 0
for chunk:
    deadline = t0 + played_ms/1000
    delay = deadline - now
    if delay > 0: sleep(delay)
    send(chunk)
    played_ms += len(chunk)/48      # 按真实字节
```

- 方案 §4.3 的公式等价，且强调“按真实字节”正确。
- **错误点**：方案写“若落后 >0.25s 则跳过 sleep”。蓝本没有 0.25s 判断；`delay<=0` 时本来就不 sleep。0.25s 是臆造阈值，应删除或改为“delay<0 即不等待”。
- 蓝本的告警阈值是 **delay < -0.3s**（落后 300ms），方案保留 300ms 正确。
- 方案新增“落后 >1s 打断+重同步”在蓝本不存在。若保留，必须定义重同步语义：是丢弃未播音频、重新锚定 `t0/played_ms`，还是暂停等追上？否则会与“不跳帧”自相矛盾。
- 8h 漂移验收要给出：分片 100ms、发送耗时上限、允许的累计误差、测量方法（首末 deadline 差 + 音频字节总数）。建议阈值：累计漂移 ≤100ms（1 片）、p99 单片唤醒 ≤20ms。

### 4.4 喂流权状态机

方案 §4.4 只给了结构体与伪代码，缺状态图与并发细节：

- 建议状态：`Idle → Acquiring → Owned → Releasing → Idle`；抢占时 `Owned(old) → Releasing(old) → Acquiring(new)`。
- 关键竞态：`Acquire` 不能在持锁时 `sleep(250ms)`，否则所有并发 Acquire 被串行拖慢 250ms；应“取旧 owner → 标记 preempting → 解锁 → cancel/wait → type7 → sleep → 加锁安装新 owner”。
- `ownerID` 用单调 generation；旧 feeder 每片检查 `generation == mine`，不等即退出；`Release` 只在 generation 匹配时关闭 `released`，且必须幂等。
- 等待旧 owner 必须有超时（例如 500ms/1s），超时后强制取消并记录 `feed_owner_force_cancel`；蓝本有“强制取消卡死持有者”的逻辑（`engine.py:831-938`），方案没写超时值。
- `respect_song` 必须进入接口：普通 TTS 在歌曲窗口内 `Acquire` 应等待或合并，而不是抢占。
- 抢占后必须 cancel 旧 TTS 生产并 drain 队列，方案 §4.4 最后一条提到了，但要在状态机里体现“队列清空”与“字幕不回退”。

### 4.5 背压、看门狗、熔断

| 项 | 蓝本 | 方案 | 问题 |
|---|---|---|---|
| 单帧 | 3s timeout，失败终止本轮 | 同 | ✅ |
| 断流 | 75s 无回包 + 20s 窗口 + 0.55x 速率 | 75s + 1s 检查 | 缺 0.55x 判据，且“无发送/接收”表述不准 |
| 重连 | 6 次、固定 20s | 6 次/20s + 60s 冷却 | 60s 冷却无出处；应删除或标注为新增 |
| 音频守护 | 30s 无发送 / 并发 >0.55 | 同 | 需核实“30s”是否真实存在；若无，标注新增 |
| TTS 队列 | 8，满则生产者阻塞 | `chan []byte` 容量 8 | 容量 8 是 8 片（~800ms）还是 8 句？必须区分 `sentenceCh` 与 `pcmCh` |
| 熔断 | 无 | 令牌桶/计数窗口 | 需给出阈值、半开状态、恢复条件 |

### 4.6 文档没写清、但 Go 必须写清的 9 个点

1. Windows 定时器/GC/调度的测量与 p99 验收。
2. `coder/websocket` 的 ping/pong 行为与 `lastRecv` 的更新点（读循环）。
3. 压缩关闭、缓冲复用、单 writer 的锁粒度。
4. 3s 写超时后如何取消上游 TTS 与队列。
5. 1s 落后重同步的具体语义（若保留）。
6. 抢占等待超时与强制取消。
7. `respect_song` 的接口与调用点。
8. `sentenceCh`/`pcmCh` 两级背压与丢弃策略。
9. 看门狗在 `starting/stopping` 阶段是否生效（蓝本只在 live？）。

---

## 5. 架构合规性

| 项 | 判定 | 说明 |
|---|---|---|
| proto→service→biz→data | ✅ | 与 HFC 一致；`service` 薄、`biz` 定义端口、`data` 只依赖 Ent，规则正确。 |
| `engine` 作为第四层 | ⚠ | HFC 没有 `engine`；它介于 biz 与 adapters，容易变成 god object。建议明确 `engine` 只依赖 `ports.go`，且不反向被 biz 依赖（biz 调 engine 端口）。 |
| Wire | ✅ | `wire.go`+`wire_gen.go` 符合 HFC admin；注意 HFC media 无 wire 生成物，不要照抄目录。 |
| Ent schema 位置 | ⚠ | 骨架只写到 adapters；需明确 `internal/data/ent/schema`，并在 `data.go` 里只创建 client。 |
| `pkg/storage` 坑 | ✅ | HFC 实际：`.gitignore:16-19` 忽略 `storage/`，`data.go:20` import `framework/pkg/storage` 但目录不存在 → 不可构建。方案改用 `pkg/blob` 正确；必须加 `.gitignore` 例外 + CI `git check-ignore`。 |
| HTTP+WS 不要 gRPC | ✅ | HFC 全仓无 `RegisterGRPCServer`；方案判断正确。 |
| `EventService` proto 预留 gRPC stream | ❌ | §3.7 写“proto 预留 gRPC stream；v1 走 WS”，自相矛盾；要么删掉，要么明确“proto 只定义消息，不生成 stream”。 |
| Asynq/NATS | ⚠ | NATS 不用，正确；Asynq 用在录播/批量 TTS/素材/统计/清理，单机单场属于过度设计。更严重的是 §3.1 说“Redis 可选、v1 可不引入”，但 Asynq 依赖 Redis → 矛盾。 |
| 单进程单场是否锁死 | ⚠ | §8 用“LiveUsecase 单例 + 互斥”锁死；但 `engine/live_engine.go` 没有 `sessionID` 参数，后续多场要改构造函数、所有队列、事件 topic。建议从第一天就 `SessionManager{max:1}` + `NewLiveEngine(sessionID)`。 |
| `config_snapshot` | ⚠ | JSON 快照若含 Vidu key/LLM key 会入库泄密；必须脱敏或只存非敏感字段。 |

---

## 6. 边车与外部依赖契约

### 6.1 OmniVoice / gradio_client 能否用 HTTP 契约稳定包住

- 蓝本事实：`gradio_client` 必须带 `session_hash`（`omnivoice.py:3-11`）；模块级 `_lock` + 事件循环锁 + 线程池单 worker（`omnivoice.py:82-95,160-230`）；地址优先 `127.0.0.1:9877`，其次配置/内网 IP（`omnivoice.py:96-103`）；有 `recent_ms/restart_count/fail_streak` 看门狗。
- 方案 §5.3 只给了 `POST /v1/synthesize` + `/v1/health`，**缺**：
  - `cancel`：长文本合成中用户点“打断”，如何取消 gradio job；
  - **超时**：单句 30s？整段 120s？超时后边车是否仍在跑；
  - **并发**：单 worker 是硬约束，Go 侧必须知道“排队深度/预计等待”，否则 8 容量队列会堆到分钟级；
  - **显存/OOM**：失败后是否重启、重启冷却、最大重启次数；
  - **session_hash 语义**：是每 voice 一个、每请求一个、还是全局复用？蓝本是复用；若每请求新生成，Gradio 队列会失控；
  - **缓存**：同一文本+音色是否复用 wav；缓存失效键（voice revision/text hash）。
- 方案 §5.3 说“推荐 PCM 直出”，但响应示例是 JSON `audio_b64`；二者矛盾。建议：`POST /v1/synthesize` 返回 `audio/wav` 二进制 + header 里带 `duration_ms`、`sample_rate`、`job_id`；另给 `POST /v1/jobs/{id}:cancel`。
- 健康：`GET /v1/health` 返回 `ok/queue_depth/gpu_mem/last_error/model`；Go 侧 3 次失败熔断、指数退避重启、最多 3 次/10 分钟；仍失败则切“预录音兜底”（方案 P0-5 提到，但没写触发条件）。

### 6.2 Vidu 端点覆盖与失败语义

方案 §5.4 的端点覆盖基本齐，但缺关键参数与失败语义：

| 端点 | 实际超时 | 方案缺的 |
|---|---|---|
| `POST /live/s_avatar/component` | 120s | 120s vs F01 “10s 内 live”；4xx 不重试、5xx 重试 1–2 次、超时是否重试（创建有副作用） |
| `GET /live/v1/lives/{id}` | 8s | 404/410 语义（会话已结束） |
| `GET .../recording` | 15s | 404 表示“还没生成”而非失败；轮询 30 分钟 |
| `GET /live/v1/lives` | 8s | 参数名 `pager.page/pagesz` |
| `GET /ent/v2/credits` | 8s | `show_detail=false`；30–60s 轮询；基线 |
| `POST /tools/v2/files/uploads` | 60s | 三步式；第 2 步 PUT **不带鉴权**；etag 缺失 |
| `PUT presigned` | 300s | 不可加 Token；超时/断点续传 |
| `PUT .../finish` | 60s | 失败重试 |
| avatars | 8/300/30 | 90 天回收 |
| action libraries | 8/30/30/8/120/30 | 403 “action library forbidden” 降级 |

错误映射：方案 `ViduError{Code,HTTPStatus,Retryable}` 方向正确；但“401/403 触发 key 失效提示”不完整——403 也可能是动作库无权限，必须区分 `action library forbidden` 与 key 无效。`429` 未提。

### 6.3 RTC token

- managed：Vidu 创建响应下发 `rtc` 与 `token_expire_at`（`engine.py:340-430`）；**无刷新端点**。
- customer：`rtc_tokens.py` 支持 ARTC（sha256/authinfo_b64/concat_json/raw）与 TRTC UserSig（TLS 2.0/hmac/zlib/base64 变体），TTL 24h；Agora 未实现。
- 方案说“Go 只做三件事”，但 `RtcTokenUsecase` 没有写 ARTC 的三种 token shape 与 TRTC 的 UserSig 算法；这是 Go 侧必须实现的实际工作量，不能一句“自签”带过。
- F49 的“token 临近过期刷新”在 managed 模式下无法实现；应改为“到期前提示/自动重开（会中断）”，或 customer 模式“到期前重新签发并让前端 `refreshToken` 重进”（会短暂黑屏）。

---

## 7. 前端

### 7.1 功能对等

- 蓝本 3534 行、128 个具名函数、fetch 轮询、无 WebSocket（`stage/index.html`）。
- 方案 §6.3 的区域映射基本合理，但漏了：
  - **随机表演**（`rndEvery/rndBias`，`setInterval`）；
  - **指令特效**（`cmdFx`）；
  - **统计面板布局**（`stats`/`rightW`）；
  - **贵宾/编排/口播面板**（对应 `/api/patrons`、`/api/choreo`、`/api/koubo`）；
  - **设置 12 组**（含“弹幕主播模式”“资产库”）。
- `F47` 的 `VoicePanel` 在蓝本不是独立面板；音色在设置抽屉的“声音”组。
- `F46` 的 11 组是错的，直接导致设置页少一组、字段漏校验。

### 7.2 Pinia / WS / RTC

- 12 个 store 划分本身合理；但“store 不互相 import”与 `useLiveStore` 需要 `useEventStore` 的 epoch/seq 有冲突。建议：
  - `useEventStore` 只暴露纯函数 `apply(event)` / `reset(epoch)`；
  - `useLiveStore` 通过 composable 订阅事件，不 import store；
  - 派生状态（phase/audio owner）从事件日志重算，避免双写。
- WS 与本地合并策略必须定义：
  - 事件带 `event_id` + `seq` + `epoch`；`epoch` 变化全量重建；
  - 断线后 `GetLiveStatus` + `ListEvents(after_seq)` 补齐；补齐失败则全量；
  - 同一 `event_id` 幂等；字幕只追加、不回退；
  - 慢客户端：服务端按 topic 丢弃/合并（15–30Hz），并给 `dropped` 计数。
- RTC 封装：`RtcPlayer` 的 props/events 基本够；需补：
  - SDK 懒加载失败/超时；
  - `autoplay` 被拒后的“点击播放”兜底；
  - `onUserLeft/onError` 与 store 的 `reconnecting` 状态；
  - `refreshToken` 只有 customer 模式可用。

### 7.3 NovaTrader 规范逐条落实（这是本方案最严重的失实）

实际规则（`NovaTrader/doc/开发文档/前端UI/README.md:503-556`）与方案 §6.7 的对照：

| 规则 | 实际要求 | 方案写法 | 判定 |
|---|---|---|---|
| R-UI-02 | 颜色只写 `var(--nt-*)`；裸色必须进 allowlist，`countLock` 只降不升 | “禁止裸 hex；语义色只用于业务语义” | ⚠ 近似但漏 allowlist/countLock |
| R-UI-04 | 状态先入册 `ui/status-map.ts`，面板禁自封文案 | 一致 | ✅ |
| R-UI-05 | 材质沿规范§2–4：玻璃窗体细边/静态外晕；完整模式≤1 层 16s 低强度流光；节能/减少动态停；数字/图表正文不得发光 | “材质三件套：半透明底、毛玻璃、内高光” | ❌ 完全不是同一条 |
| R-UI-08 | 材质模式与 `data-motion` 由 settings 唯一驱动 | “禁止 !important；3 处重复才提；禁止 v-html” | ❌ 臆造 |
| R-UI-10 | `ui/`/`viz/`/`display/` 三层**皆禁** import store/router | “ui/ 可被任意层引用；viz/display 不得 import” | ❌ 方向相反 |
| R-UI-11 | 面板薄壳：`panels/<组>/*.panel.vue` 只装配接线 | “空态/加载/错误必须用 display/AppEmpty/AppLoader/AppError” | ❌ 臆造，且组件名不存在 |
| R-UI-12 | 不引入 UI 组件库，缺口优先扩展 Nt* | 一致（Quasar 豁免） | ✅（需记录偏离） |
| R-UI-13 | 单 SFC >800 行或 >3 状态机必须拆 | 一致 | ✅ |
| R-UI-20 | 缺位显示“—”+ 原因徽注，禁 `?? 0` | “主工作区 ≤1480px，卡片间距 16px” | ❌ 完全错位 |
| R-UI-30 | 防呆内联：条件不满足禁用提交+行内说明 | “动效 120–200ms，禁止无限动画” | ❌ 臆造 |
| R-UI-34 | 轮询统一 `utils/polling.ts` 的 `createVisibilityPolling` | “表格统一 NtTable；空态/加载/错误齐全” | ❌ 错位 |
| R-UI-40/41 | 间距刻度 / 数字等宽 | “桌面优先、最小 1280×720；窄屏折叠右栏” | ❌ 这是 R-UI-43 |
| R-UI-24 | 遮蔽统一 `ui/NtSecretText.vue` | 未提 | ❌ 缺 |
| R-UI-36 | 错误统一 `api/http.ts` 的 `classifyApiError` | 未提 | ❌ 缺 |
| R-UI-37 | 全局快捷键登记表 | 未提 | ❌ 缺 |
| R-UI-43 | <1280 收侧栏、<960 单列、<720 仅卡片 | 写成 40/41 | ❌ 错位 |
| R-UI-60/61 | 首帧防白闪、CSP 白名单不扩 | 未提 | ❌ 缺 |

Token 值同样失实：方案写 `#071016/#0d1b22/#4fd1c5/#e6f1f5/#8fa6b2`，实际是 `#050d17/#0b1b2b/#45e0ff/#eef6fa/#85a5bc`（`tokens.css:8-23`）。组件归属也错：`NtStatCard`、`NtTimeline` 在 `display/`，`NtPanel` 不存在（`web/src/ui` 与 `web/src/display` 实际清单）。

结论：§6.7 这一节必须整节重写，否则前端无法按规范验收，且会把“照抄 NovaTrader”与“自创规则”混在一起。

### 7.4 多端/iframe/mixed content/CSP

方案没写：
- 若页面走 HTTPS，后端必须是 HTTPS + WSS；否则 mixed content 直接失败。
- 若嵌入 iframe：`allow="autoplay; fullscreen; microphone"`、`frame-ancestors` CSP、SameSite cookie。
- Quasar 的 `QDialog/QMenu` 会往 body 注入 class/层，需与 Nt token 体系对齐；CSP 下 Quasar 的 inline style 可能需要 `style-src 'unsafe-inline'`，与 NovaTrader R-UI-61 冲突，需明确取舍。
- 多端：1280×720 / 1920×1080 之外，窄屏右栏折叠（R-UI-43）没有落点。

---

## 8. 数据模型与安全

### 8.1 Ent 覆盖

方案 §7.1 覆盖了场次/转写/弹幕/礼物/TTS/编排/人设/贵宾/动作库/素材/设置/成本/录制/口播，基本合理。缺：

- `ViduKey` / `RtcCredential`：多 key、激活、轮换、失效时间、尾号掩码；
- `DmRule` / `BlockWord` / `VipConfig`：如果要做“可配置”而不是只读 config；
- `AuditLog`：谁改了 key/设置/人设；
- `SessionCrash` / `Recovery`：崩溃恢复；
- `Song` / `LyricCache`：如果复盘要按歌词时间轴；
- `KouboTake` 的音频文件与版本。

`LiveSession.config_snapshot` 必须脱敏；否则 Vidu key/LLM key 会进 DB。

### 8.2 鉴权（方案完全缺失）

蓝本事实：`/api/settings`、`/api/vidu/keys`、`/api/danmaku` 等**全无鉴权**（`bridge.py` 全文无 `Depends`/`CORS`/Origin 校验）。方案只在 Danmaku 一处写“仅本机/可信网段”，其余写接口没有任何保护。

必须补：
- 默认 `bind 127.0.0.1`；远程访问需显式开关 + Bearer token 或 mTLS；
- 所有写接口（settings/keys/avatars/actions/audio/personas/koubo/choreo/patrons/record/danmaku/gifts）统一中间件鉴权；
- WS `/events` 也要鉴权（header 或短期 ticket，不能只靠 URL query 明文 token）；
- `Origin` 白名单 + CSRF（若用 cookie）；CORS 默认关闭；
- 外部采集器：HMAC 共享密钥 + IP allowlist，保留“永远 200 + reason”兼容；
- 密钥：OS Keychain/DPAPI 或 0600 文件，日志/DB/`config_snapshot` 全程脱敏；页面只显示尾 4/8 位（`NtSecretText`）；
- 审计：key 激活/删除、设置变更、开播/收播。

### 8.3 本地回环 vs 远程

- 本机单场：`127.0.0.1` + 无鉴权可接受，但必须显式“仅本机”并在启动日志/健康页标注。
- 局域网/公网：必须 HTTPS/WSS + token；否则任何同网段用户都能改 key、开播、读转写。
- 若未来多用户：所有表现在带 `session_id` 不够，还需要 `owner_id`；方案 §8 说“为 v2 留缝”，但没留 `owner_id`。

---

## 9. 许可与 clean-room 有效性

- 蓝本 AGPL-3.0（`LICENSE:1`），NOTICE 明确排除人物形象、克隆音色、人设/文案/歌单/动作库、品牌（`NOTICE:6-12`）。
- 方案 §9.3 选“C 行为对齐 + clean-room 重写”，方向正确；但 §9.4 的“参与重写的人不看源码”与文档自身**逐行引用 `engine.py:831-938`、`signaling.py:39-52`** 矛盾。设计文档已经是“看过源码的人写的规格”，clean-room 的证据链已被污染。
- 可执行隔离建议：
  1. 规格与实现分两拨人：规格组只输出“行为表 + 官方协议文档 + 黑盒观测”，不输出行号/函数名/注释/常量名；实现组不接触 `cyber-xiaoya-live-main` 仓库。
  2. 新仓库独立，禁止把蓝本源码/素材/配置放进同一 workspace；CI 加“禁止路径/关键词”检查（如 `cyber-xiaoya`、`_own_audio`、`s2s.`）。
  3. UI 不照抄 DOM/类名/文案/设置组顺序；12 组可以功能对等，但要重命名、重排、重写文案。
  4. 协议字段（type 数字、`conn_id`、`seq_id`）以 Vidu 官方文档/抓包为准，不以蓝本注释为准。
  5. OmniVoice 包装层重写；不复制 `omnivoice.py` 的 `session_hash` 逻辑与看门狗实现；如需参考，记录来源与许可。
  6. 歌词打轴工具 `tools/lyrics_align.py` 是 AGPL 代码，不能直接移植；要么保留 Python 外部工具（独立进程、AGPL 合规），要么重写。
  7. HFC/NovaTrader 无 LICENSE：只能参考“架构思想/视觉语言”，不能复制文件/样式/组件实现；最好取得书面确认。
- 结论：当前文档只能算“行为规格”，不能算 clean-room 证据；在实施前必须把行号引用从实现规格中剥离。

---

## 10. 工期与验收

- §10.1 团队：2 Go + 1 前端 + 0.5 QA；§10.3 又写“2 后端 + 2 前端：8–10 周”，自相矛盾。
- 10–14 周的前提是：M1 实时音频一次成型、M2 前端 3–4 周完成 128 函数对等、M3 2–3 周完成弹幕/礼物/贵宾/编排/口播/歌曲/参考图/录播/复盘/成本。以 1 名前端计算，M2/M3 明显偏乐观；建议 16–20 周，或把 F50 复盘、F36 成本拆分、F48 多端同步移到 v2。
- 阶段验收物的证明力：
  - M0“CRUD 可演 + 单测通过”不能证明 WS 事件总线/Ent/Wire 正确；需加“事件 seq/epoch 单测 + WS 断线重连 demo”。
  - M1“30 分钟不炸”不能证明 8h 漂移；8h 压测要单独作为 exit gate，并给出 p99/漂移/内存/goroutine 报告。
  - M2“1280/1920 可演”只证明视觉，不证明功能对等；需逐 F 的 demo 脚本（录屏 + 日志 + 指标）。
  - M3“F01–F51 全绿”在当前 F 表有 8 个 ❌/1 个 ⚪ 的情况下不可能全绿；先修表再谈全绿。
- V1–V8 有验证方式，但**无时间点/负责人/门禁**。建议：
  - V4（Kratos WS）在 M0 前 PoC，否则 WS 事件总线可能返工；
  - V1/V2/V3/V5 在 M1 exit 前关闭；
  - V6 在 M2 前；
  - V7 在 M3 前；
  - V8 在上线前；
  - 每个 V 给“失败即停止/降级”的 kill criteria。

---

## 11. 问题清单（P0 / P1 / P2）

### P0（阻断/错误：不关闭不得进入实现）

#### P0-1 §6.7 前端规范整节失实
- **位置**：§6.7、§6.8、§6.3 的 `README.md:553-559` 引用。
- **问题**：R-UI-05/08/10/11/20/30/34/40/41 的内容与出处大面积错位；Token 值与实际 `tokens.css` 不符；`NtPanel` 不存在，`NtStatCard/NtTimeline` 不在 `ui/`。
- **证据**：`NovaTrader/doc/开发文档/前端UI/README.md:503-556`；`web/src/theme/tokens.css:8-23`；`web/src/ui`、`web/src/display` 文件清单。
- **建议改法**：整节重写为“实际规则 → 方案落点 → 判定”三列；删除错误 Token；补 R-UI-24/34/36/37/43/60/61。
- **验收**：用脚本比对 README 规则 ID 与方案引用，0 错位；Token 值与 `tokens.css` 逐项相等。

#### P0-2 全站无鉴权/密钥设计缺失
- **位置**：§3.7、§7.2、§8，以及所有写接口。
- **问题**：方案只为 Danmaku 写了“本机/可信网段”，settings/keys/avatars/actions/personas/koubo/record/WS 均无鉴权；无 CSRF/Origin；密钥存哪、如何脱敏未定义；`config_snapshot` 可能入库泄密。
- **证据**：`src/bridge.py` 全文无 `Depends/CORS/Header` 校验；`stage/index.html` 只显示 key 尾 8 位（设置组）。
- **建议改法**：新增“安全与鉴权”章：默认 127.0.0.1、远程 Bearer/mTLS、写接口中间件、WS ticket、Origin/CSRF、密钥 DPAPI/Keychain、审计、脱敏。
- **验收**：未带 token 的任意写接口返回 401；远程开关关闭时仅回环可访问；`config_snapshot`/日志无 key 明文。

#### P0-3 F01/§3.8 重复开播语义与蓝本相反
- **位置**：F01、§3.8。
- **问题**：蓝本重复开播是 `stop("reconnect")` 后重开，方案写 `ALREADY_LIVE` 拒绝；这会让“断线重连/重开”路径与验收冲突。
- **证据**：`src/engine.py:305-306`。
- **建议改法**：二选一：对齐蓝本（覆盖 `StartLive` 语义为“先停再开”并记录 `reconnect`），或明确这是**偏离**并把 F01 验收改成“重复开播先停后开/或 409（需产品确认）”。
- **验收**：重复点击开播的行为与所选语义一致；断线重连用例通过。

#### P0-4 F04/§4.1 心跳事实错误
- **位置**：F04、§4.1。
- **问题**：F04 写“5s ping/15s pong”，§4.1 写“10/20、约 15s 未回 pong”；实际是 10/20，且“约 15s”无依据。三处不一致会直接导致 Go 客户端心跳实现错误。
- **证据**：`src/vidu/signaling.py:84-86`。
- **建议改法**：统一为 10s/20s；删除“5s/15s/约 15s”；写明 `coder/websocket` 的读循环更新 `lastRecv`。
- **验收**：PoC 在 20s 无 pong 时触发断连；日志时间戳与配置一致。

#### P0-5 F49 token 刷新无实现路径
- **位置**：F49、§4.7、§6.6。
- **问题**：managed RTC 无刷新端点；方案却把“临近过期刷新”写成验收；customer 模式需自签 ARTC/TRTC，方案没写算法。
- **证据**：`src/engine.py:340-430`；`src/rtc_tokens.py:1-20`；`src/bridge.py` 无 rtc 刷新路由。
- **建议改法**：managed 明确“不支持续期，到期只能重开（会中断）”；customer 写清 ARTC 三种 shape 与 TRTC UserSig；F49 验收改为“到期前提示/重开”或“customer 重签+重进”。
- **验收**：长会话跨过 `expire_at` 的行为符合所选策略；无“承诺刷新但无接口”的验收项。

#### P0-6 F24/F26 行为臆造
- **位置**：F24、F26、§12.1。
- **问题**：蓝本没有按文本去重；动作优先级 t0/t2/confirm 都是 0，不存在“t0 > t2”“confirm 属 manual”。这会误导实现与验收。
- **证据**：`src/dm_router.py:105-109,562,900-918`；`src/engine.py:1168-1171`；`src/engine.py:2385-2392`。
- **建议改法**：F24 改为“最新优先 + 有界缓冲 + dropped 计数 + 20s 播报闸门”，去重作为可选新增；F26 改为 `vip 3 > manual 2 > choreo 1 > 其他 0`。
- **验收**：用固定弹幕序列断言队列/丢弃/优先级；不再断言“重复只触发一次”。

#### P0-7 F03/F35 游标语义错配
- **位置**：F03、F35、§3.7、§6.5。
- **问题**：蓝本 `since` 是字幕下标；方案 `since_seq` 是事件序号；“不丢”在蓝本环形缓冲下不成立；`tr_list` 路由不存在。
- **证据**：`src/engine.py:3006-3075`；`src/bridge.py` 无 `/tr_list`。
- **建议改法**：拆成两个游标：`transcript_since`（字幕）与 `event_seq`（事件）；落库后由 DB 提供“不丢”保证；删除 `tr_list` 或明确映射到 `ListEvents`。
- **验收**：断线 5s 后补齐，字幕无重复/无空洞；跨场次回放可查。

#### P0-8 OmniVoice 边车契约缺关键语义
- **位置**：§5.3、P0-5。
- **问题**：无 cancel/timeout/并发/显存/重启；`audio_b64` 与“PCM 直出”自相矛盾；`session_hash` 语义未定义。
- **证据**：`src/tts/omnivoice.py:3-13,82-95,160-230`。
- **建议改法**：给出完整契约：`/v1/synthesize`（二进制 wav）、`/v1/jobs/{id}:cancel`、`/v1/health`（queue/gpu/last_error）、超时/并发=1/503+Retry-After、重启策略、预录音兜底。
- **验收**：注入 30s 卡死 → 超时并取消；连续 3 次失败 → 重启/熔断；健康页可见队列深度。

#### P0-9 实时音频常量混杂臆造、状态机未闭环
- **位置**：§4.3、§4.4、§4.5。
- **问题**：0.25s“跳过 sleep”、60s 冷却、30s 守护、音频优先级 100/60/50/40 均无蓝本依据；ownership 伪代码在锁内 sleep，且没有 generation/释放竞态处理。
- **证据**：`src/engine.py:1703-1761`（只有 `delay>0` 才 sleep、`delay<-0.3` 告警）；`src/engine.py:831-938`（有 `respect_song`/4 轮等待）。
- **建议改法**：删除无依据常量；画 `Idle/Acquiring/Owned/Releasing` 状态图；锁外等待；等待超时；`respect_song` 入接口；两级队列容量。
- **验收**：并发 Acquire/Release 的 race 测试；抢占后无叠音；8h 漂移报告。

#### P0-10 自检结论不成立
- **位置**：§12.1、§12.3。
- **问题**：“70 个路由”“11 组”“未发现漏项”均与源码不符：63+14=77、12 组、F44/F46/F48 漏项。
- **证据**：`src/bridge.py` 63；`src/koubo.py` 14；`stage/index.html:683-837`；localStorage 7 键。
- **建议改法**：重写 §12.1 为“逐路由/逐组/逐键”的核对表；把“未发现漏项”改成“已知漏项 + 处理决定”。
- **验收**：脚本化核对：路由数、设置组数、localStorage 键、面板数与文档一致。

### P1（缺口：需在 M0/M1 前补设计）

| ID | 位置 | 问题 | 蓝本/框架证据 | 建议改法 | 验收方式 |
|---|---|---|---|---|---|
| P1-1 | F01 | 验收“10s 内 live”与创建超时 120s 冲突 | `client.py:66` | 改为“创建 ≤120s，UI 10s 内进入 starting 并显示进度” | 注入 30s 慢响应，状态可见 |
| P1-2 | F05/§4.5 | 75s 判据缺 0.55x；“6 次/20s”误读；60s 冷却臆造 | `engine.py:433-490` | 写全判据与重连间隔；删 60s 或标注新增 | 断网/限速双场景 |
| P1-3 | F07 | 创建 body 缺 `managed_rtc`/RTC；403 降级未写 | `engine.py:318-335` | 补字段与降级分支 | 换 key 后仍可开播 |
| P1-4 | F08/§5.4 | 分页参数名错；端点超时未列 | `client.py:83-91` | 改为 `pager.page/pagesz`；列超时 | 真实列表查询 |
| P1-5 | F09 | 图片资产 90 天回收未提 | `client.py:140` | 生命周期提示/本地缓存 | 90 天后资产失效提示 |
| P1-6 | F12/§4.3 | 0.25s 阈值无依据；8h 验收缺条件 | `engine.py:1742` | 删 0.25s；给 p99/漂移测量法 | 8h 报告 |
| P1-7 | F13 | `respect_song`/4 轮等待/超时未写 | `engine.py:831-938` | 补接口与超时 | 歌曲中点聊天不叠音 |
| P1-8 | F15 | “流中断可回退”无证据 | `llm.py` | 删或标注新增，给回退策略 | 注入 SSE 断流 |
| P1-9 | F18/F32 | `script`/`.txt`/歌词打轴生产链未纳入；3s 起播不现实 | `engine.py:1857-1931`、`tools/lyrics_align.py` | 补字幕/歌词/缓存失效；起播目标按文件时长 | 10s 歌曲点播 |
| P1-10 | F21 | 蓝本永远 200+reason；方案改拒收并拆 gifts | `bridge.py:722-766` | 保留兼容入口或给采集器适配 | 旧采集器直推 |
| P1-11 | F31 | “14 个动作”实为 14 路由；project.json→DB 未定义 | `koubo.py` | 改述；给迁移 | 14 路由回归 |
| P1-12 | F34 | 录播 30min 轮询/15-30-60s 未写；customer 不录 | `engine.py:341-346,707-758` | 补轮询与模式限制 | 停播后 30min 内拿到 |
| P1-13 | F36 | TTS/LLM 成本拆分无数据源；credits 轮询/基线未写 | `bridge.py:79-124` | 只做可采集的 credits/tokens；其余标“估算” | 对账 |
| P1-14 | F37/F46 | 11 组→12 组，漏“弹幕主播模式”“资产库” | `stage/index.html:683-837` | 重列 12 组 | 组数/字段核对 |
| P1-15 | F38 | 切换人设清空记忆未写 | `bridge.py:536` | 补 `/llm/reset` | 切换后旧上下文不出现 |
| P1-16 | F42 | key 尾 8 位掩码/轮换未写 | `bridge.py:1458-1503` | 补掩码/激活/删除审计 | 页面不显示全 key |
| P1-17 | F44/F48 | 漏贵宾/编排/口播面板；漏 4 个 localStorage 键 | `stage/index.html` | 补面板与 UI 状态持久化 | 128 函数功能清单核对 |
| P1-18 | §3.1/§3.6 | “Redis 可选”与 Asynq 必需矛盾；Asynq 过度设计 | HFC `asynq.go` | 要么 Redis 必选+compose，要么改 DB 任务表 | 一键启动含 Redis |
| P1-19 | §8/§3.2 | `LiveEngine` 无 sessionID，单场锁死成本高 | `engine.py:831` 单实例 | `SessionManager{max:1}` + `NewLiveEngine(sessionID)` | 多场开关只改 max |
| P1-20 | §7 | 缺 ViduKey/RtcCredential/DmRule/AuditLog；`config_snapshot` 泄密 | `config.example.json` | 补实体与脱敏 | schema/日志检查 |
| P1-21 | §9 | clean-room 证据链已被行号引用污染 | 本文档 §4/§5 | 规格/实现分离；删实现级引用 | 隔离检查 |
| P1-22 | §10 | 1 FE vs 2 FE 矛盾；M2/M3 过短 | §10.1/§10.3 | 16–20 周或裁 F50/F36/F48 | 甘特图 |
| P1-23 | §12.2 | V1–V8 无时间点/门禁 | §12.2 | 加 owner/date/kill criteria | 评审表 |
| P1-24 | §5.4 | 401/403/429/5xx/超时语义不全；PUT 鉴权 | `client.py:8,102-105` | 逐端点表 | 故障注入 |
| P1-25 | §6.5 | WS/本地合并缺幂等/补齐/慢客户端策略 | NovaTrader `ws/*` | 定义 event_id/seq/epoch/dropped | 断线 30s 恢复 |

### P2（优化/表述）

| ID | 位置 | 问题 | 证据 | 建议 |
|---|---|---|---|---|
| P2-1 | §3.1 | “78 个 proto”实为 75 | `apps/server/api` | 改数字 |
| P2-2 | §3.1 | HFC media 无 wire_gen，只有 admin | 全仓 | 改述 |
| P2-3 | §3.7 | EventService 预留 gRPC stream 自相矛盾 | §3.4 | 删除 |
| P2-4 | §2.2 结论 | “0 功能级裁剪”与 §2.3/F50 新增不一致 | §2.2/§2.3 | 改述“裁剪 4 类 + 新增 1 项” |
| P2-5 | §6.3 | `README.md:553-559` 行号漂移 | 实际 518-519 | 改引用 |
| P2-6 | §6.7 | `tokens.css:1-107` 行号与实际不符 | 实际更长 | 改引用 |
| P2-7 | §6.7 | “NtPanel”不存在 | `web/src/ui` | 删/改 |
| P2-8 | F45 | “快捷键可用”无按键表 | `keydown` 3 处 | 列键位 |
| P2-9 | F51 | health 缺 `build/song_playing/danmaku_pending` | `bridge.py:341-349` | 补字段 |
| P2-10 | §4.5 | 告警分级未给采样窗口 | — | 加窗口/阈值 |
| P2-11 | §3.5 | 事件 topic 与前端 store 未一一映射 | §3.5/§6.4 | 加映射表 |
| P2-12 | §9.4 | “如必须参考，保留记录”不可执行 | — | 给模板/责任人/存放位置 |
| P2-13 | §10.2 | “8h 压测”未说在哪台机器/是否含 GPU | — | 固定基准机 |
| P2-14 | §6.9 | “首屏 <2s”未说网络/构建模式 | — | 标 local/prod |

---

## 12. 给方案作者的修订清单（按优先级，可直接逐条执行）

1. **重写 §6.7**：从 `NovaTrader/doc/开发文档/前端UI/README.md:503-556` 逐条抄录实际规则，建立“规则 → 方案落点 → 判定”表；删除错误 Token，改为 `tokens.css:8-23` 的真实值；修正 `NtStatCard/NtTimeline` 归属，删除 `NtPanel`。
2. **新增“安全与鉴权”章**：默认 127.0.0.1、远程 Bearer/mTLS、所有写接口中间件、WS ticket、Origin/CSRF、密钥 DPAPI/Keychain、审计、`config_snapshot` 脱敏。
3. **改 F01/§3.8**：明确“重复开播=先停后开（reconnect）”或改为 409 并标注偏离；补 starting/stopping 的 Stop 语义。
4. **改 F04/§4.1/§4.5**：心跳统一 10/20；删除 5/15/约 15s；补 0.55x 判据；删除 60s 冷却或标注新增。
5. **改 F03/F35**：`since` 与 `since_seq` 拆成两个游标；`tr_list` 改为 `ListEvents`；给“不丢”加上 DB 条件。
6. **改 F49**：managed 明确不可续期；customer 写清 ARTC/TRTC 算法；验收改为“到期提示/重开/重进”。
7. **删除/改写臆造项**：F24 去重、F26 t0>t2、F46 11 组、F48 只覆盖 1 键、F44 6 面板。
8. **补边车契约 v2**：cancel/timeout/并发=1/健康/重启/显存/缓存/预录音兜底；`audio_b64` 与 PCM 统一。
9. **补 Vidu 契约表**：每个端点的方法/鉴权/超时/重试/失败语义；`pager.page/pagesz`；PUT 不带鉴权；403 动作库降级；90 天资产回收。
10. **补实时音频状态机**：`Idle/Acquiring/Owned/Releasing` 图；锁外等待；generation；`respect_song`；等待超时；`sentenceCh/pcmCh` 两级容量；Windows p99/8h 测量法。
11. **补漏项 F 行**：开场白、`/llm/reset`、按句 type10 开关、随机表演、统计/指令特效持久化、资产 90 天、credits 轮询基线、录播轮询、歌词打轴工具、Koubo take。
12. **补 Ent 实体**：`ViduKey`/`RtcCredential`/`DmRule`/`BlockWord`/`VipConfig`/`AuditLog`/`SessionCrash`；`config_snapshot` 脱敏。
13. **重写 §9 clean-room**：规格/实现分离、独立仓库、禁止路径检查、UI 重命名/重排、协议以官方文档为准、OmniVoice 包装层重写、歌词工具独立进程或重写。
14. **改 §10 工期**：统一团队假设；M2/M3 加人/加周；F50/F36/F48 移 v2；给每个 V 项 owner/date/kill criteria（V4 在 M0 前，V1/V2/V3/V5 在 M1 exit 前，V6 在 M2 前，V7 在 M3 前，V8 上线前）。
15. **重写 §12.1 自检**：路由 63+14=77、设置 12 组、localStorage 7 键、面板清单；“未发现漏项”改为“已知漏项表”。
16. **修架构一致性**：Redis 是否必选与 Asynq 对齐；`LiveEngine(sessionID)`；`EventService` 删除 gRPC stream 预留；`pkg/blob` + `.gitignore` 例外 + `git check-ignore` CI。
17. **补可观测性**：`/healthz` 字段、`feed_lag_ms`、`send_timeout`、`reconnect`、`tts_queue`、`danmaku_dropped`、`ws_last_recv`。
18. **补前端多端**：HTTPS/WSS、iframe 权限、CSP、Quasar 与 R-UI-61 的取舍、窄屏折叠（R-UI-43）。

---

## 13. 一句话给决策层

这不是“能不能用 Go 重写”的问题，而是“当前文档能不能作为施工图”的问题：架构选型正确，但校验性事实、鉴权、边车契约、实时状态机和 clean-room 证据链都还没到可开工的程度。**先修 P0，再谈里程碑。**

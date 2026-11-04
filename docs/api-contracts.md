# 接口与事件契约

本文是开发侧接口契约的索引与语义说明。两份**单一事实来源**文件优先于本文任何转述：

- 事件契约：`web/src/services/api/events.contract.json`
- 面板路由：`web/src/services/api/panel-routes.json`

proto 源文件：`api/live/v1/live.proto`、`api/live/v1/danmaku.proto`、`api/config/v1/settings.proto`；
HTTP 绑定经 `google.api.http` 注解，生成代码由 `make proto` 产出（禁止手写）。

## 1. proto 服务路由

### 1.1 LiveService（`dh.live.v1`）

控制面；实时音频与 Vidu 信令在引擎进程内，不经此服务。核心写路由四条为
`StartLive / StopLive / Say / SendCommand`，其余为控制与读接口：

| RPC | HTTP | 说明 |
|---|---|---|
| StartLive | POST `/api/v1/live/start` | 重复开播=先 `stop(reconnect)` 再开，绝不 409；支持 `idempotency_key` |
| StopLive | POST `/api/v1/live/stop` | 幂等；返回 `phase/recording_state/ok/note`；支持 `idempotency_key` |
| Say | POST `/api/v1/live/say` | `mode` = sentence \| whole；返回 `utterance_id` |
| SendCommand | POST `/api/v1/live/command` | 返回 `accepted/reason` |
| Interrupt | POST `/api/v1/live/interrupt` | 中断口播：停播放 + 清 TTS 队列 + 发 type7 语义事件 |
| ClearTranscript | POST `/api/v1/live/transcript/clear` | 清字幕，返回 `cleared` |
| StopActions | POST `/api/v1/live/actions/stop` | 中断当前动作语音 |
| ClearActions | POST `/api/v1/live/actions/clear` | 只清动作队列，不影响播放中动作 |
| ResetLlm | POST `/api/v1/live/llm/reset` | 清空当前场次 LLM 对话记忆：返回真实 `cleared_turns`（无会话/无记忆为 0），并发布 `llm.reset` 事件 |
| GetLiveStatus | GET `/api/v1/live/status` | 快照 + feed/ws/danmaku/rtc 子状态；`transcript_since` 增量字幕 |
| ListEvents | GET `/api/v1/live/events` | `session_id/after_seq/limit`（默认 200，上限 500）；与 WS 同源 |
| GetRtcToken | GET `/api/v1/live/rtc-token` | 见 §4 |

引擎未配置时 `StartLive` 返回 **503** 且 `error_code=engine_not_configured`（fail-closed，不退回 fake）。

### 1.2 DanmakuService（`dh.live.v1`）

| RPC | HTTP |
|---|---|
| Ingest | POST `/api/v1/danmaku/ingest`（collector 模式 HMAC 入口见 §5） |
| SetMode | POST `/api/v1/danmaku/mode` |
| Confirm | POST `/api/v1/danmaku/confirm` |
| Dismiss | POST `/api/v1/danmaku/dismiss` |
| ListPending | GET `/api/v1/danmaku/pending` |

### 1.3 SettingsService（`dh.config.v1`）

| RPC | HTTP |
|---|---|
| Get | GET `/api/v1/settings` |
| Save | POST `/api/v1/settings/save` |
| Reload | POST `/api/v1/settings/reload` |

## 2. 面板路由（85 条）与独立 preflight 路由（1 条）

85 条面板全集见 `panel-routes.json`，Go 与前端双侧测试校验一致性。按前缀分组：

| 分组 | 条数 | 代表路由 |
|---|---|---|
| `/api/v1/tts/*` | 8 | voices/voice/preview/files/upload/import-folder/delete/queue |
| `/api/v1/actions*` | 8 | libraries 增删查、actions 增删查、queue、send |
| `/api/v1/choreo*` | 8 | 编排增删查、groups、start/stop/run-once/status |
| `/api/v1/prompt*` | 3 | GET/POST `/prompt`、POST `/prompt/type14` |
| `/api/v1/credits` | 1 | Vidu 额度查询 |
| `/api/v1/record*` | 3 | GET/POST `/record`、POST `/record/fetch` |
| `/api/v1/refs*` | 2 | 列表、upload |
| `/api/v1/vidu/keys` | 2 | GET/POST |
| `/api/v1/songs/*` | 6 | 列表/upload/play/stop/删除/status |
| `/api/v1/sessions*` | 3 | 列表/详情/`{id}/export`（导出上限 10000 事件，超出 `truncated=true`） |
| `/api/v1/cost/*` | 3 | `ledger/{id}`、POST ledger、summary |
| `/api/v1/danmaku/state` | 1 | 面板状态 |
| `/api/v1/koubo/*` | 14 | state/split/seg/merge/seg/split/tts/tts_all/select/action/actions_all/play/play_all/stop/audio/{name} |
| `/api/v1/personas*` | 4 | 列表/select/save/delete |
| `/api/v1/chat` | 1 | 管理对话（需开播会话；未开播 409、LLM 未配置 503） |
| `/api/v1/patron*` | 5 | patrons 列表/ids/save、patron/join、patron/welcome |
| `/api/v1/avatars*` | 13 | 档案 CRUD/duplicate/activate/sync-vidu/refs/upload、档案与资产图片 |

另有 **1 条独立 preflight 路由**：`POST /api/v1/live/preflight`，由 `registerPreflightRoute` 单独注册，不属于上述 85 条面板清单。

面板接口沿用 `{ok, error}` 信封（A 批 P1-14 裁决：前端已全量依赖，不改成 proto 信封；
**新接口必须沿用该信封**并在评审说明）。口播「合并选中/拆分/全部动作」走
`/koubo/merge`、`/koubo/seg/split`、`/koubo/actions_all`。

### 2.1 草稿路由

| HTTP | `owner_id` 位置 | 说明 |
|---|---|---|
| GET `/api/v1/live-drafts` | query | 按查询范围列出草稿，返回 `draft_id/revision/summary/committed` 等选择字段 |
| GET `/api/v1/live-drafts/{id}` | query | 读取查询范围内的草稿详情及 apply 所需租约字段 |
| POST `/api/v1/live-drafts/{id}/apply` | JSON body | 复用编辑租约、fencing token 与 expected revision CAS 提交草稿 |

**`owner_id` 非安全边界，仅供多会话/多实例区分。** 当前 local 回环校验和 lan/collector
共享管理令牌均不提供用户级认证主体，服务端无法从主体派生 owner；同一共享管理令牌持有者可传入
任意 `owner_id` 切换查询范围。列表过滤及详情的不存在/范围不匹配统一 404 都只是应用级查询语义，
不得当作用户权限校验。若未来需要用户级隔离，必须先引入可区分的认证主体，再由主体派生或校验 owner。

上述三条路由每次请求都会追加 `action=draft_owner_unverified`、`actor=unknown_principal`、
`target=owner_id` 的审计，显式标记 owner 未经认证主体验证；审计不记录 token、请求体或 owner 参数值。
apply 仍保留原有写操作审计。

## 3. WS 事件流（`/ws/v1/events`）

- **ticket 申领**：`POST /api/v1/ws-ticket?session_id=...` → `{ticket, expires_at, ttl_s}`；
  ticket 为 32 字节随机 hex，TTL 由 `security.ticket_ttl`（默认 60s）。
- **传递方式**：经子协议头 `Sec-WebSocket-Protocol: dh.v1, ticket.<t>`，**不放 URL query**
  （浏览器 WS 不能设 Authorization 头，故该路由不做 Bearer，由 handler 消费 ticket）。
- **一次性消费**：握手前先 `Peek` 只读校验（无效/过期/场次不符 → 401 `ticket_invalid_or_used` /
  `ticket_session_mismatch`，**不烧票**）；`Upgrade` 成功后才 `Consume`；
  并发窗口内票被别的连接用掉则升级后立即以 close frame（PolicyViolation）关闭。
- **场次绑定**：ticket 绑定了 session 时，query `session_id` 必须一致；推送时按场次过滤
  （其他 session 的事件不推给本连接，空 session 的全局事件保留）。
- **断线补齐**：query `after_seq=N` → 先回放 `seq > N` 的本场次历史，再挂实时订阅；
  去重用 `event_id`。
- **保活**：服务端每 30s 发 ping；写截止 10s；客户端断开经 `req.Context().Done()` 结束
  （该路由不受全局短超时影响，`timeoutFilter` 对它豁免）。
- 连接计数：握手成功经 `Manager.WSConnected/WSDisconnected` 记账，状态接口读真实连接数。

## 4. RTC 凭据（`GetRtcToken`）

`GET /api/v1/live/rtc-token?session_id=...` → `{mode, provider, token, expires_at, room_id, user_id, ok, reason}`
（`expires_at` 为 epoch ms）。三档行为：

| `rtc.mode` | 行为 |
|---|---|
| `disabled`（默认） | HTTP 200 + `mode=unconfigured, ok=false`；前端显示「RTC 未配置」，**不是错误** |
| `dev` | 本地 HMAC 签发（`DH_RTC_API_KEY`/`DH_RTC_API_SECRET` 走环境变量）；每次签发轮换 nonce；仅供联调，不能直接用于真实厂商 SDK |
| `managed` | 厂商签发器；未装配签发器时仍为 `unconfigured`（fail-closed） |

前端刷新失败保留旧凭据并显示降级提示，不静默清空播放器。使用者视角见 `docs/使用手册*.md`。

## 5. 鉴权模型（`internal/server/auth.go`）

filter 执行顺序：timeout → requestLogger（分配 `request_id`，回写 `X-Request-Id`）→ audit（**在鉴权之前**，
被拒的写请求也留痕）→ origin → auth。未登记的私有路径默认要求鉴权，避免新增接口漏口。

公开白名单：`/healthz`、`/api/health`。`/metrics` 仅回环（`metrics_loopback`，默认开）。

### 5.1 三种安全模式（`security.mode`）

| 模式 | 语义 |
|---|---|
| `local`（默认） | 仅允许 loopback 来源；配置要求监听地址必须是 loopback，否则启动拒绝 |
| `lan` | 必须配置 64 位 `admin_token_sha256`；请求头 `Authorization: Bearer <token>`，服务端对 token 做 SHA256 后常量时间比较 |
| `collector` | 弹幕采集入口（`/api/danmaku`、`/api/v1/danmaku`）走 HMAC 签名，其余路由同 lan 走 Bearer；必须配 `collector_secret`（或 `DH_COLLECTOR_SECRET`） |

### 5.2 collector HMAC

头：`X-Dh-Ts`（秒）、`X-Dh-Nonce`、`X-Dh-Sign`。
签名串：`HMAC_SHA256(secret, method + "\n" + path + "\n" + sha256(body) + "\n" + ts + "\n" + nonce)`。
时间窗 ±300s；nonce 经 `NonceStore` 去重（窗口 10min；内存版重启即清空、多实例不共享，Redis 版多实例共享）。
body 读取上限 4MB，读完回填供后续 handler 使用。

### 5.3 Origin

`Origin` 头非空时：local 模式放行 `http://127.0.0.1[:port]` / `localhost` / `[::1]`；
其余按 `security.allowed_origins` 精确匹配（忽略尾斜杠，大小写不敏感）；不在白名单 → 403 `origin_not_allowed`。

## 6. 事件契约（`events.contract.json`）

信封字段：`seq, event_id, session_id, type, ts_ms, epoch, request_id, payload`。规则：

- `seq` 按场次单调递增，换场次从 1 重新计数，不跨场次串号；
- `event_id` 全局唯一，前端去重键；
- `session_id`：`live./song./refs./action.` 事件必须等于当前场次；
- `epoch` 同一场次内恒定，换场次/切歌/重连时改变；
- `payload` 为字符串键的业务字段。

事件类型与 payload 字段集合（以 JSON 文件为准，此处仅索引）：

`transcript.partial(text,index,mode)`、`transcript.final(text,index,utterance_id)`、
`live.start_requested(persona_id,avatar_id)`、`live.start_failed(error,error_code)`、
`live.started(live_id)`、`live.stop_failed(reason,error)`、`live.stopped(reason,session_id)`、
`live.interrupted`、`live.crash_recovered(session_id,error_code,from_phase)`（Recover 发布）、
`audio_interrupted(signal_type)`（type7 语义）、`llm.reset`、
`song.playing(file,script,epoch,song_playing,caption_lines,lyrics_source)`、`song.stopped(song_playing,file)`、
`refs.mounted(type,op,ok,images)`、`refs.error(type,op,ok,error)`、
`action.sent(text,source,queued)`、`action.send_failed(text,error)`、
`action.deferred(text,source,priority,gap_s,queued)`、`action.stopped(cleared)`、
`action.cleared`。

> 注：`live.crash_recovered` / `audio_interrupted` / `llm.reset` / `action.cleared` 由后端发布，
> 若未列入 JSON 的 `events` 表，以 JSON 文件与 `pkg/eventcontract` 测试的当前校验口径为准；
> 新增/修改事件类型必须改 JSON 并同步两侧，禁止单边改字段。

字幕归并键：partial/final 双方都带 `utterance_id` 时按 id 归并（交错发言不串行），任一方缺 id 退回 `index`
（前端 `web/src/stores/transcript.ts` `sameUtterance`，A 批语义裁决 3）。

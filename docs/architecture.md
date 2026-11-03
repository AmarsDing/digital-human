# 架构说明

数字人直播控制台（S2）：Go 1.26 后端（Kratos v2 + Ent + Wire + Proto）+ Vue 3.5 / Quasar / Pinia / TS 前端。
行为对齐以 `cyber-xiaoya-live-main` 为参照，代码为 clean-room 重写。本文只描述开发侧架构事实；
面向使用者的操作说明见 `docs/使用手册*.md`。

## 1. 分层与依赖方向

```
web/                 Quasar 控制台（NovaTrader tokens + Nt* 原子）
api/                 proto 定义（api/live/v1、api/config/v1）与生成代码（api/gen/go）
cmd/dh/              唯一进程入口；Wire 装配（wire.go 手写声明、wire_gen.go 由 make wire 生成）
internal/server/     Kratos HTTP + WS handler + 鉴权/审计/Origin/超时 filter
internal/service/    proto 服务适配层（LiveService / DanmakuService / SettingsService）
internal/biz/        领域逻辑：live（场次核心）、danmaku、settings、koubo、song、action、
                     choreo、refs、media、tts、billing（F36 额度轮询）、recording（F34 云录制）、
                     archive、audit、rtc、chat（LLM 对话管理）、persona（人格）、avatar（形象档案）、
                     gift、patron（舰长）、vidukeys
internal/data/       Ent client / Redis 的唯一持有者 + repo 实现（含 Switchable* 降级包装）
internal/data/ent/   Ent schema 与生成代码（make ent）
internal/engine/     引擎：ViduController（HTTP 建场 + 信令 WS + 喂流权 + Feeder）、
                     vidu/（纯客户端层）、tts/（OmniVoice 边车客户端）
pkg/                 可复用基础包：eventbus、eventcontract、audio、logx、secrets
configs/             配置样例（敏感值不入 yaml，见 docs/ops.md）
```

依赖方向自上而下，**不允许反向**：

- `server` → `service` → `biz` → `data` / `engine`；`pkg/*` 是叶子，不依赖 internal。
- `internal/data` 唯一持有 `ent.Client` 与 `redis.Client`；上层不得 import ent（`data.go` 注释为硬约束）。
- `internal/engine` 实现 `biz/live` 定义的端口（`Controller` / `StatusProvider`），biz 不认识 Vidu 具体类型。
- `biz/live` 的 `Recorder`（F34）与 `CreditsPoller`（F36）也是端口，由 `biz/recording`、`biz/billing` 实现、装配层注入。

## 2. Wire 装配

- 进程只有一个入口 `cmd/dh`；`wire.go` 声明 ProviderSet，`wire_gen.go` 由 `make wire` 生成（禁止手写，见仓库根 AGENTS.md 第 3 节）。
- 构造期无法表达的运行期注入走 Setter，均在全部组件就绪后调用一次：
  - `Manager.SetRecorder` / `SetCreditsPoller` / `SetInterruptHooks`（`internal/biz/live/live.go`）。
  - `Data.SetOnDBReady`：PG 晚就绪时的切换回调（EventSink、审计、场次各注册一个；注册时 PG 已可用则立即回调）。
- 引擎 fail-closed：`engine.mode=real`（默认）缺 `DH_VIDU_KEY` / `DH_OMNIVOICE_ADDR` 或真实适配器未装配时，
  `StartLive` 返回 503 `engine_not_configured`，**不退回 fake 成功**；`dev` 模式显式装配 FakeController，
  启动日志打 `WARN using fake live controller`。

## 3. 事件总线与 REST/WS 同源

事件流是唯一事实源，前后端共用 `web/src/services/api/events.contract.json`（详见 docs/api-contracts.md）。

- `pkg/eventbus.Bus` 负责分配信封：`seq, event_id, session_id, type, ts_ms, epoch, request_id, payload`。
- `Manager` 构造时调用 `bus.SetScope(m.Scope)` 与 `bus.SetAppender(sink)`：
  - 所有事件（含 song/refs/action 等经 `PublishScoped` 的业务事件）统一由 bus 写**同一个 sink**；
  - WS 广播与 `GET /api/v1/live/events` REST 列表因此**同源**，不存在「WS 有、REST 没有」的分叉；
  - `Manager.publishFor` 不自己落库，避免双写。
- `seq` **按场次单调递增**（唯一约束 `(session_id, seq)`），换场次从 1 重新计数；`event_id` 全局唯一，作前端去重键。
- `epoch` 是喂流/歌曲代际，切歌或重连时 +1；前端见 epoch 变化即清空旧喂流相关 UI 状态。
- 断线补齐走 `after_seq`（`seq > after_seq`）：WS 握手带 `after_seq` 时 `bus.SubscribeFor` 先回放历史再挂实时订阅。

## 4. 单场次语义（重复开播）

- `Manager` 单进程只允许一场直播。`Start` 时发现处于 `starting/live/stopping`，
  在**同一个 `opMu` 临界区**内先 `stop(reason=reconnect)` 再创建新场次，**绝不返回 409**。
- 幂等：`StartIdempotent` / `StopIdempotent` 按 `idempotency_key`（`start:`/`stop:` 前缀）复放结果；
  key 的 get→执行→put 在 `idemMu` 内串行，杜绝并发同 key 的 check-then-act 竞态。默认窗口 24h
  （`DefaultIdempotencyTTL`）。存储后端按 Redis 可达性选 Redis 版或进程内版（差异见 docs/data-model.md）。
- `Stop` 幂等：`idle/ended` 返回 `ok=true` 与说明；收尾对齐蓝本 0.4s 窗口。

## 5. 崩溃恢复（Manager.Recover）

- 判据是**归属**而非时间窗：`Recover` 执行时唯一可能真正活着的未完成场次是本 Manager 当前持有的
  `m.session`；其余所有 `starting/live/stopping` 行一律是已死进程的遗留，标记为
  `error(crash_recovered)` 并发布 `live.crash_recovered` 事件——哪怕它几秒前还在更新
  （那是崩溃前最后一次写入，不标记会变永久幽灵 live）。
- 曾用的 60s 陈旧窗口已废弃（P2-13，会把刚崩溃的场次漏掉）；cutoff 取 `now+1` 表示全量扫描。
- 执行时机：`App.Run` 在 `Server.Start` 前一次；DB 晚就绪时由装配层再入一次。
- Recover **绝不自动恢复喂流或会话**，只标记与发事件。
- 锁定测试：`TestRecoverMarksAllUnfinishedExceptCurrentSession` / `TestRecoverSkipsCurrentSession` /
  `TestRecoverAfterCrashNewManagerTakesOver`。

## 6. 引擎（internal/engine）

`ViduController` 是真实引擎适配器，组合 Vidu HTTP 建场 + 信令 WS + 喂流权（Ownership）+ Feeder，
实现 `live.Controller` 与 `live.StatusProvider`。硬约束：引擎内部重连**绝不**触发 Manager 层
stop/start（蓝本 NOT_RESTART）；外部依赖失败如实上抛，不静默降级。

### 6.1 看门狗与重连（`controller.go`）

| 参数 | 默认值 | 语义 |
|---|---|---|
| WatchdogTick | 1s | 看门狗节拍 |
| StallIdleRecv | 75s | 信令收帧闲置阈值 |
| StallMinWindow / MinSamples / MaxRate | 20s / 3 / 0.55 | 滑动窗口喂流速率判定：窗口≥20s、采样≥3、速率≤0.55×48B/ms 判 stall |
| MaxReconnects | 6 | 信令重连预算，耗尽后 `terminateLocal(reconnect_exhausted)` |
| ReconnectMinGap | 20s | 两次重连最小间隔；被丢弃的请求由看门狗节拍补试 |
| StallSuppress | 60s | 重连后 60s 内不判 stall（链路回暖） |
| IdleTimeout | 300s | 无 Say/喂流活动即本地收场 |
| HardLimit | 1800s | 场次硬上限，无条件终结 |
| GuardInterval / GuardStale / GuardHold | 5s / 30s / 30s | 音频守卫独立节拍，`Ownership.RevokeStale` |

stall 判定窗口只保留最近 4×minWindow，避免陈旧高速样本稀释近期低速。`terminateLocal` 只关闭引擎侧
资源并落 error 态，不触碰 Manager。

### 6.2 喂流权（`ownership.go`）

复刻蓝本 engine.py:831-938：非强制路径最多 4 轮、每轮等旧持有者 ≤1.5s；排队上限 25s；
25s 后若持有者最近 5s 仍在喂流则继续等；强制收回前发 type7（audio_interrupted）并静默 250ms；
歌曲/聊天持有者优先（守卫豁免、抢占受限，等待上限 30s）。`Say` 与 `NewFeedSink` 写 WS 前经
`leaseSender` 强制校验 lease，旧 gen 的帧直接拒绝。

### 6.3 Feeder（`feeder.go`）

按 **48B/ms 绝对时基**喂 PCM（100ms×4800B 分片）：每轮 t0 独立（静默期不喂流，不能用全局连续流
假设）；单帧写超时 3s；单帧失败终止本轮不重发；迟到只记录（`Late/Warn/MaxLag/Drift`），
**绝不跳帧或补发**。长跑结论见 docs/verification.md。

### 6.4 Vidu 客户端（`engine/vidu`）

纯客户端层，只依赖 net/http/gorilla，便于 httptest 与本地 fake WS 契约测试。心跳参数以代码为准：
`ping_interval=10s / ping_timeout=20s / close_timeout=5s`（蓝本注释的 5s/15s 不作数）。
错误码（`vidu_*`，写进 `LiveSession.error_code` 与 UI 文案）与端点超时见 `client.go` 常量；
GET 仅在网络错/5xx 时重试一次，POST 创建永不重试。

## 7. 前端

- Quasar + Pinia + TS；视觉遵循 NovaTrader Aether V2 基准与 `--nt-*` tokens（工作区规则第 6 节）。
- 85 条面板路由由 `web/src/services/api/panel-routes.json` 单一事实来源登记，Go 与前端双侧测试校验；另有 1 条独立注册的 `POST /api/v1/live/preflight`，不计入面板清单。
- 事件契约 Go 侧 `pkg/eventcontract` 与前端 `contract.ts` 各有测试读 `events.contract.json` 校验，禁止单边改字段。
- 快捷键：F9 开播；F10 只打开下播确认，显式激活「确认下播」后才停止，Esc/取消保持 live；`/` 聚焦 + 命令前缀。

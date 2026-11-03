# digital-human 后端与架构独立审查报告

- 审查日期：2026-11-03
- 时点快照标注（2026-11-04）：文中「62 条面板路由」「71 条」等计数均为评审当日口径；主线已陆续交付 DZ/G 批等接线，现口径为 **85 条面板路由 + 1 条独立 preflight 路由**（面板单一事实来源 `web/src/services/api/panel-routes.json`，分组见 `docs/api-contracts.md` §2）。评审统计本体保留不改。
- 审查对象：`f:\myproject\teamwork\digital-human`（Go 1.26 + Kratos v2.9.2 + Ent v0.14.6 + Wire + Proto）
- 方案基准：`f:\myproject\teamwork\digital-human-go-plan.md`（v2.0，下称“方案”）
- 评审基线：`f:\myproject\teamwork\digital-human-go-plan-review.md`
- 行为蓝本：`f:\myproject\teamwork\cyber-xiaoya-live-main`（下称“蓝本”）
- 审查方式：只读代码 + 实际执行 `gofmt/build/vet/test/race` + 真实起进程做 HTTP/WS 冒烟 + 与蓝本逐条行为对照
- 约束遵守：未修改任何业务代码；未执行 git 提交；未安装任何系统软件。唯一写入为本报告。
- 三态口径：**已实测** = 本次真实执行并观察到结果；**仅契约验证** = 单测/静态代码路径验证，未起真实外部依赖；**未验证** = 需要真实 Vidu/OmniVoice/RTCPG/Redis/8h 压测但本次环境不具备。

---

## 0. 总体判定

**判定：不可交付。**

按 v2.0 方案的 M1/M2 门禁，本项目当前实际状态是：

- **M0 骨架成立**：目录分层、Kratos/Ent/Wire/Proto 脚手架、鉴权 filter、审计、健康检查、事件总线、Vidu/TTS/Feeder/Ownership 的**库级实现**均存在且可编译、可单测。
- **M1 实时闭环未交付**：生产装配 `cmd/dh/wire.go` 全部使用 Fake（FakeController/FakePlayer/FakeSynth/FakeSender/FakeSignaler/FakeDecoder），真实 Vidu HTTP/WS、OmniVoice 边车、喂流、喂流权、状态机、看门狗均**未接入生产**。
- **M2/M3 只交付路由形状**：62 条面板路由存在，但 koubo/song/tts/action/choreo 多为内存/文件存储 + Fake 实现，存在必现的功能中断（请求 ctx 被取消、sticky stop）。
- **实时事件链路不可用**：`/ws/v1/events` 在 15s 被 Kratos 全局 Timeout 断开（已实测）。

统计（F01–F51 与 G01–G30 中后端应负责的 71 条）：

| 判定 | 数量 | 说明 |
|---|---:|---|
| 已实现且可测 | 6 | 主要是无外部依赖的库级能力（F04/F08/F11/F12/F14/G28） |
| 实现但与蓝本不符 | 10 | 有主体实现，但存在语义偏差或必现缺陷 |
| 只实现形状 | 19 | 路由/DTO/接口存在，行为为 Fake、空实现或未接线 |
| 未实现 | 36 | 无对应 biz/路由/接线 |
| 不在后端范围（纯前端） | 10 | F43–F48、G19/G20/G23/G30 等 |

结论：**当前版本不能作为 v2.0 的 M1/M2 交付物；应先修复 P0（8 条）后重新评审，其余 P1 在 M2/M3 前关闭。** 若仅作为 M0 骨架演示，必须把 README、健康接口与开播接口改为“未配置真实引擎则拒绝开播”，不能返回 `fake-live-*` 的假成功。

---

## 1. 实测基线

### 1.1 命令与结果

| 命令 | 结果 | 备注 |
|---|---|---|
| `gofmt -l .` | 无输出 | 格式干净 |
| `go build ./...` | exit 0 | 全量可编译 |
| `go vet ./...` | exit 0 | 无 vet 告警 |
| `go test -count=1 ./...` | 全部 PASS | 见 1.2 覆盖率与 8 章测试质量 |
| `go test -race -count=1 ./internal/engine/... ./internal/biz/live/... ./internal/server/... ./pkg/eventbus/... ./pkg/audio/...` | 全部 PASS | 关键包 race 通过；但未覆盖真实并发 Start/Stop 场景 |
| `make vet` | 成功 | Makefile 使用 `/bin/bash`，本机可用 |
| `go mod verify` | All modules verified | 依赖完整性正常 |
| `go list -m`（直接依赖） | 无异常版本 | ent 0.14.6 / kratos 2.9.2 / pgx 5.7.2 / redis 9.7.0 / websocket 1.5.3 / prometheus 1.20.5 |
| `govulncheck` | 未执行 | 环境约束不安装新工具；属未验证项 |

### 1.2 覆盖率（`go test -cover` 实测）

| 包 | 覆盖率 | 判读 |
|---|---:|---|
| `internal/server` | 65.8% | 鉴权测试用 dummy handler，未覆盖真实路由表 |
| `internal/biz/live` | 66.8% | 未覆盖并发 Start/Stop、WS 生命周期 |
| `internal/engine` | 81.7% | Feeder/Ownership 单测较好，但未接线 |
| `internal/engine/vidu` | 72.4% | HTTP/WS 契约测试，未对真实 Vidu |
| `internal/engine/tts` | 74.8% | 边车契约测试，未对真实 OmniVoice |
| `internal/biz/tts` | 81.9% | FakeSynth，非真实合成 |
| `internal/biz/song` | 69.4% | FakeDecoder/FakePlayer |
| `internal/biz/koubo` | 55.2% | 未覆盖 sticky stop / play_all ctx |
| `internal/biz/danmaku` | 46.7% | 未覆盖风控/批读/VIP |
| `internal/biz/action` | 64.2% | 未覆盖并发 lastAt 竞态 |
| `internal/biz/settings` | 62.5% | 未覆盖 Ent 并发 revision |
| `pkg/eventbus` | 87.2% | 未覆盖跨重启 seq/epoch |
| `pkg/audio` | 42.9% | Pacer 平台分支覆盖有限 |
| **`internal/service`** | **0%** | 所有生成路由的 service 入口无测试 |
| **`internal/data`** | **0%** | 数据层无测试 |
| **`internal/data/repo`** | **0%** | Ent/Memory sink 无契约测试 |
| **`cmd/dh`** | **0%** | Wire 装配/启动/停机无测试 |

“全绿”的真实含义：库级单测通过；**核心 HTTP→service→biz→data 链路、Ent 契约、WS 长连、并发开播、停机恢复均无测试覆盖**。

### 1.3 HTTP/WS 冒烟（真实进程，已实测）

环境限制：本机未运行 PostgreSQL/Redis（未安装/未启动），因此 `healthz` 返回 503；Ent 运行期路径无法真实跑通，只做静态与单测验证。

| 场景 | 观察到 | 结论 |
|---|---|---|
| `GET /healthz`（local） | 503，dependencies PG/Redis=false | 启动自检按设计工作 |
| `POST /api/v1/live/start`（local，FakeController） | 200，`live_id=fake-live-<8>` | 假成功，P0-1 |
| `POST /api/v1/live/say`（idle） | HTTP 500 + 原始错误 | 错误码未分类，P1-14 |
| `GET /api/v1/live/status`（idle） | `feed.owner=true`、`ws.state=disconnected` 等硬编码 | 状态接口失真，P0-5 |
| `POST /api/v1/live/stop`（传伪 session_id） | 仍停止当前场次 | 忽略 `session_id`，P0-3/P1-3 |
| `POST /api/v1/settings`（只传部分组） | 200 `{saved:false, rejected:[...]}` | 校验失败仍 200，P1-2 |
| `POST /api/v1/koubo/play_all`（无片段/未开播） | 200 `started=true`，无实际播放 | ctx 取消 + 未校验状态，P0-6/P1-7 |
| `POST /api/v1/songs/play`（非法参数） | 400 | 正常 |
| LAN：未带 Bearer 的 GET/POST | 401 | 鉴权生效 |
| LAN：Bearer 后 | 200 | 鉴权生效 |
| Collector：正确 HMAC | 200 | 签名生效 |
| Collector：**同 ts/path/签名，替换 body** | **200** | body 未绑定，P0-4 |
| Collector：无签名 | 401 | 正常 |
| WS：ticket 第二次使用 | 401 | 一次性消费生效 |
| WS：空闲保持 | **约 15s 被服务端 Aborted** | Kratos Timeout 杀长连，P0-2 |

### 1.4 三态分类

- **已实测**：构建/vet/test/race；LAN/collector 鉴权；HMAC 篡改；WS 一次性与 15s 断开；local 冒烟各接口。
- **仅契约验证**：Vidu HTTP/WS、TTS 边车、Feeder 节奏、Ownership 状态机、EventBus、Ent schema。
- **未验证**：真实 Vidu 建场/信令/8h 漂移/叠音；真实 OmniVoice；RTC token；PostgreSQL/Redis 运行期切换与 Ent 事务；断网/限速/看门狗；前端联调。

---

## 2. P0（必须修复后才能交付）

### P0-1 生产装配全部为 Fake，M1 实时链路未接入

- **位置**：`cmd/dh/wire.go:78`、`cmd/dh/wire.go:150-160`、`cmd/dh/wire.go:178`；`cmd/dh/wire_gen.go`（生成物一致）
- **问题**：`provideController` 固定返回 `live.NewFakeController`；面板运行期固定使用 `media.NewFakePlayer`、`tts.NewFakeSynth`、`action.NewFakeSender`、`refs.NewFakeSignaler`、`action.NewMemoryRemote`、`media.FakeDecoder`。生产 Wire 中不存在 `vidu.Client`、`vidu.Signaling`、`engine.Ownership`、`engine.Feeder`、`engine.StateMachine`、`engine/tts.Client`、RTC token、ViduKey、Persona/Avatar/Record 的装配。
- **证据**：`wire.go` 上述行；`internal/biz/live/fake.go`；实测 `live/start` 返回 `fake-live-<8>`；`internal/engine/**` 全部只在测试中被引用。
- **复现方式**：启动 `cmd/dh`（local），`POST /api/v1/live/start` 后 `GET /api/v1/live/status`，观察 `live_id` 前缀 `fake-live-` 且无任何真实出流。
- **修复建议**：增加 `provideRealRuntime`（由配置开关选择），装配 Vidu/TTS/Engine；未配置 Key/边车时 **fail-closed**：`live/start` 返回 `engine_not_configured`，不得返回假 live_id。Fake 仅允许在 `mode=dev` 且显式配置时启用，并在 health 中标注。

### P0-2 `/ws/v1/events` 在 15s 被服务端强制断开

- **位置**：`internal/server/http.go:40`（`khttp.Timeout(cfg.Server.ReadTimeout)`）；`internal/server/ws_events.go:92`（`case <-req.Context().Done(): return nil`）
- **问题**：Kratos 的 `Timeout` 会为每个请求派生带超时的 ctx 并在结束时 `cancel()`；WebSocket 升级后仍复用该 ctx。默认 `ReadTimeout` 15s，导致所有事件 WS 在 15s 被服务端主动关闭。实时字幕/事件/断线补齐在 15s 后全部失效。
- **证据**：实测无事件空闲连接在约 15s 收到 Aborted；代码路径与配置一致。方案 §3 要求 WS 长连，F03/F04 要求断线补齐。
- **复现方式**：`POST /api/v1/ws-ticket` 取票 → 以 `Sec-WebSocket-Protocol: ticket.<t>, dh.v1` 连接 `/ws/v1/events?after_seq=0` → 不发事件，计时观察约 15s 关闭。
- **修复建议**：WS 走独立 `http.ServeMux`/独立 `http.Server`（不设置全局 `ReadTimeout`），或对 `/ws/v1/events` 绕过该 Timeout；保留应用层 ping/pong 与空闲上限；增加“空闲 60s 仍存活”的集成测试。

### P0-3 `Manager.Start/Stop` 生命周期非原子，并发开播可双建场；Idempotency-Key 头未读取

- **位置**：`internal/biz/live/live.go:136-183`（Start 释放锁后调用 Stop 再重锁，`ctrl.Start` 在锁外）；`live.go:186-212`（Stop 不取消在途 Start）；`internal/service/live_service.go:27`（只传 `req.GetIdempotencyKey()`，不读 `Idempotency-Key` 头）；`internal/biz/live/idempotency.go`（内存存储）
- **问题**：两个并发 Start 可同时通过 `phase==Live` 分支，各自调用 Stop→Start；Stop 不取消另一个请求的 `ctrl.Start`；后完成的 Start 可能把 `m.session` 覆盖为另一场，但两场上游都已创建。`Idempotency-Key` HTTP 头被忽略；内存幂等键重启即丢；幂等锁是全局单锁，串行化所有 key。
- **证据**：代码路径；`live.go:138-145` unlock 后再 Stop；`live.go:163` 锁外 `ctrl.Start`；`live_service.go:27`；`idempotency.go` 无持久化。
- **复现方式**：FakeController 下并发 10 次 `POST /api/v1/live/start`（同 key 与不同 key），观察 `live.start_requested` 多条、最终 `session_id` 被覆盖；逻辑竞态不会触发 Go race 报告，需要并发行为断言。
- **修复建议**：用单一的 `opMu`/single-flight 覆盖“check→stop→create→set live”全流程；每次 Start 生成 `op_id`，Stop 按 `op_id` 取消对应创建 ctx；读取 `Idempotency-Key` 头并落 Redis（方案 §3 明确要求 Redis）；幂等结果持久化；补并发测试。

### P0-4 Collector HMAC 不覆盖 body，且无 nonce，可在 300s 内伪造/重放任意弹幕

- **位置**：`internal/server/auth.go:174-194`（签名仅 `ts + "\n" + path`）
- **问题**：签名不包含 HTTP method、body、query，也没有 nonce。攻击者只要捕获一次合法请求的 `ts/sign`，在 300s 窗口内可对 `/api/danmaku` 发送**任意 body**；同一请求也可无限重放。弹幕在最终实现中会驱动 TTS/动作/付费，属于可被利用的写入面。
- **证据**：实测同一 `X-Dh-Ts`/`X-Dh-Sign`、同路径、**不同 body**，服务端仍 200 接受；未签名 401。
- **复现方式**：collector 模式配置固定 secret；用脚本对 `{"text":"hello"}` 计算签名，再发送 `{"text":"TAMPERED-DIFFERENT-BODY"}`，观察 200。
- **修复建议**：签名改为 `HMAC(secret, method + "\n" + path + "\n" + sha256(body) + "\n" + ts + "\n" + nonce)`；服务端维护 nonce 短期去重（Redis，TTL≈窗口）；强制 HTTPS；对 `ts` 窗口收紧；增加篡改 body/重放 nonce 的负向测试。

### P0-5 `GetLiveStatus` 伪造关键状态，且忽略游标与会话参数

- **位置**：`internal/service/live_service.go:42-60`；`internal/biz/live/live.go:313-329`
- **问题**：`Feed` 恒为 `{owner:true, owner_kind:"none"}`、`Ws` 恒为 `disconnected`、`Danmaku` 恒空、`Rtc` 恒 `managed/artc`；忽略 `req.SessionId`、`TranscriptSince`；`transcript` 从未被业务追加，却每次全量返回；`DurationS` 在 `Ended` 后仍按 `time.Since(startedAt)` 持续增长。状态接口是前端/运维的唯一事实源，当前返回的是“假健康”。
- **证据**：实测 idle 状态即返回 `feed.owner=true`；代码上述行；全仓无 `transcript` append。
- **复现方式**：`GET /api/v1/live/status`（idle）观察 `feed.owner=true`；开播→下播后隔 10s 再查，`duration_s` 仍增长。
- **修复建议**：状态全部取自 Engine/Manager 的真实快照（feed owner/kind、WS state、danmaku 计数、RTC mode/provider、cursor）；错误 session 返回 404/409；`Ended` 后冻结时长；transcript 由 type10/歌词事件写入并支持 `transcript_since` 增量。

### P0-6 面板后台任务绑定请求 ctx，handler 返回即被取消

- **位置**：`internal/server/panels.go:605-615`（`play_all` 中 `go func(){ svc.PlayAll(ctx, ...) }()`）；`internal/server/panels.go:258-261`（`choreo/start` 直接传 `c.Request().Context()`）
- **问题**：Kratos handler 返回后请求 ctx 被取消。`play_all` 的 goroutine 立即收到 canceled，播放链路中途退出；`choreo/start` 的循环同理。接口却先返回成功，形成“必现假成功”。
- **证据**：实测 `play_all` 在未开播/无片段时返回 `started=true` 且无任何播放；代码路径可证；`choreo/start` 同构。
- **复现方式**：`POST /api/v1/koubo/play_all`，响应 200/started=true；服务端日志可见 ctx canceled；前端无音频。
- **修复建议**：后台任务使用服务生命周期 ctx（`context.WithoutCancel(reqCtx)` 并叠加任务超时/取消函数），由 job 状态机管理 Start/Stop；handler 只在“已成功调度”后返回；补“请求返回后任务继续运行”的测试。

### P0-7 停机不收敛：SIGINT/SIGTERM 只停 HTTP，不停止 Manager/调度器/后台任务

- **位置**：`cmd/dh/app.go` 的 `Run()`（收到信号只 `a.Server.Stop(ctx)`）
- **问题**：未调用 `Manager.Stop`/`Scheduler.Stop`/`Koubo`/`Choreo`/Feeder 的停止；真实接入后进程退出会把 Vidu 场次留在“live”，持续计费；本地 DB 也会留下 `live` 行，下次启动虽有 Recover，但 60s 窗口与 DB 就绪竞态会漏（见 P0-8）。
- **证据**：`app.go` 信号处理分支；`wire.go` 未注入任何 lifecycle 管理器。
- **复现方式**：开播（Fake）后 Ctrl+C；重启后查询会话，仍可能为 `live`/`starting`。
- **修复建议**：实现有序关闭：先停喂流/接受新请求 → `Manager.Stop("shutdown")`（带超时）→ 停 Scheduler/Choreo/Koubo → 关闭 Data；等待 goroutine 退出；补 goleak/停机集成测试。

### P0-8 内存→Ent 切换不迁移；启动 Recover 与 DB 就绪存在竞态，崩溃旧场次可能永不恢复

- **位置**：`cmd/dh/wire.go:72-93`（SwitchableSink/SessionStore/Settings + `SetOnDBReady`）；`internal/data/data.go`（Probe/回调）；`internal/data/repo/switchable.go`；`internal/biz/live/live.go:262-283`（Recover）
- **问题**：启动时先使用内存 sink/store；DB 就绪后直接切 Ent，**不回填**内存中已产生的场次/事件/设置。`App.Run` 在启动时调用一次 `Recover`，若此刻 Switchable 仍指向内存，则 DB 中上次崩溃遗留的 `live/starting/stopping` 行不会被标记；DB 晚就绪后也不会再次 Recover。结果：UI 看到“仍在直播”的幽灵场次，或内存期数据永久丢失。
- **证据**：`wire.go` 的切换写法；`Recover` 只调用一次；`data.NewData` 的 5s 探测与异步回调；实测无 DB 时 health 503 且所有写走内存。
- **复现方式**：不启动 PG 启动 dh → 开播/写设置 → 再启动 PG；查询 Ent 表与 `GET /api/v1/sessions`，内存期数据不在 Ent；预置一行 upsert 时间 30s 前的 `live` 记录，重启后仍为 `live`。
- **修复建议**：DB 就绪回调中再次执行 `Recover`；启动阶段在 DB 未就绪时 fail-closed 或进入只读“降级”并明确标识；为 Switchable 增加一次性内存→Ent 回填（或强制重启后以 Ent 为准并清理内存）；补切换/延迟就绪集成测试。

---

## 3. P1（交付前应修复）

### P1-1 WS ticket 不绑定会话，且在 Upgrade 前消费

- **位置**：`internal/server/ws_events.go:21`（`ok, _ := tickets.Consume(ticket)` 丢弃 sessionID）；`ws_events.go:47`（先消费再 Upgrade）
- **问题**：ticket 只证明“曾经持有 admin token”，不证明“可订阅该 session”；多场次时任意票可看任意场事件。先消费后 Upgrade 意味着客户端握手失败（缺 `dh.v1`、非 WS 请求）也会烧票。
- **证据**：代码；`Consume` 返回 `(bool,string)` 被忽略；`Issue(sessionID)` 存了 sessionID 却未校验。
- **复现方式**：`ws-ticket?session_id=A` 取票 → 订阅 B（当前单场不明显，代码路径可证）；用同一票先发普通 GET 再 WS，第二次 401。
- **修复建议**：`Consume` 后校验 sessionID（或签发时绑定 `live_id + epoch`）；在 Upgrade 成功后再删除票（原子“验证+占用”），失败不烧票；补跨会话拒绝测试。

### P1-2 设置保存非串行 + 吞错 + Reload 空实现

- **位置**：`internal/data/repo/settings.go`（revision 检查在事务内但无行锁）；`internal/biz/settings/settings.go`（仅校验 `danmaku.mode`、Patch 非原子）；`internal/service/settings_service.go:30-46`（所有错误→200 `saved:false`）；`settings_service.go:48-54`（Reload 只 Get 并返回 ok）
- **问题**：两个并发保存同一 `base_revision` 都可读到旧值并写入，丢失更新；解析错误、revision 冲突、DB 故障全部变成 HTTP 200，前端无法区分“拒绝”与“服务异常”；`Reload` 不重新应用配置、不重启任何运行时（G29 只满足路由形状）；`effective` 字段普遍为空；保存弹幕模式不会实际切换 `danmaku.Service`。
- **证据**：代码；实测部分保存返回 200 `saved:false`。
- **修复建议**：`SELECT ... FOR UPDATE` 或乐观锁 `UPDATE ... WHERE revision=?` 并检查受影响行数；校验/冲突返回 409，DB 错误返回 5xx；Reload 执行真实 apply 并留审计；保存后按“即时/连线生效”矩阵调用对应服务。

### P1-3 场次持久化 check-then-create 非原子、全字段覆盖、并发乱序

- **位置**：`internal/data/repo/session.go`（`Exist` 后 `Create/Update`）；`internal/biz/live/live.go:106-133`（persist 在锁外写、错误忽略）；`live.go:197-210`（Stop 用请求 ctx、忽略 `ctrl.Stop` 错误）
- **问题**：并发 Start/Stop 的 persist 可交错，`Starting` 可能晚于 `Live` 落库；`Update` 覆盖全部字段，旧请求可把新状态写回；`Stop` 失败仍强制 `Ended`；`session_id` 被 service 忽略，停止任意当前场次。
- **证据**：`session.go`；`live.go` 上述行；实测伪 `session_id` 仍能停止。
- **修复建议**：使用 `ON CONFLICT DO UPDATE` + 版本/updated_at 条件；状态迁移只允许合法前置状态；persist 串行化或带 generation；Stop 校验 session_id 并传播错误；建场 ctx 可取消。

### P1-4 事件 seq/epoch 语义不成立，断线补齐与重放不可靠

- **位置**：`pkg/eventbus/eventbus.go`（全局 `seq` 原子递增、2048 环形缓冲、慢订阅关闭 channel）；`internal/biz/live/live.go:153`（epoch=启动毫秒，之后不递增）
- **问题**：seq 全进程全局，重启归零且无法跨会话区分；`after_seq` 来自旧进程会永远“超前”而收不到；epoch 不是场次内递增的“歌/中断代际”，G13 无法实现；慢订阅被关闭 channel → 事件丢失且无补偿；`Subscribe` 不按 session 过滤。
- **证据**：代码；`m.seq` 字段未使用；`Snapshot.EventSeq` 取 `bus.Seq()`。
- **修复建议**：seq 按 session（或持久化全局）分配并跨重启单调（Redis/DB 序列）；epoch 在场次创建、切歌、强制回收时递增；订阅按 session 过滤；慢订阅改为丢弃计数 + 从 sink 回放，不关 channel。

### P1-5 数据层：10 实体仅 4 个在用，ViduKey 无“唯一激活”，无 TTL，缺方案实体

- **位置**：`internal/data/ent/schema/content.go`、`live.go`、`ops.go`；`cmd/dh/wire.go:167`（billing 用文件而非 Ent）
- **问题**：`Persona`、`Asset`、`DanmakuEvent`、`Recording`、`ViduKey`、`CostLedger` 均无业务读写（6/10 死表）。`ViduKey.is_active` 只有普通索引，可同时存在多个 active key（方案 F42 要求“激活新 key 只影响下一次建场”，必须唯一）。无 TTL/清理任务（资产 90 天、ticket、事件归档均无自动清理）。方案 §7 的 ActionLibrary/Action、GiftBatch、TtsJob、KouboProject/Take、Song、ChoreoConfig、Patron、VipConfig、BlockWord/DmRule、RtcCredential、SessionCrash 等未落表（koubo/choreo/cost 用文件）。
- **证据**：schema 与 grep 的引用面；`billing.NewFileStore`；`koubo.NewFileStore`；`choreo.NewFileStore`。
- **复现方式**：插入两条 `is_active=true` 的 ViduKey，二者同时可被查询；重启后文件型工程保存于本地文件而非 DB。
- **修复建议**：加 partial unique index（`WHERE is_active`，Ent 可用 `entsql.Annotation`/原生迁移）；引入清理 job（TTL/90 天）；按方案补齐核心实体与索引；将文件型 store 收敛到 Ent 或明确标注“本地单机模式”。

### P1-6 中断/停止动作语义与蓝本不符

- **位置**：`internal/biz/live/live.go:295-311`（`Interrupt`/`StopActions`/`ClearActions` 只发事件）；`internal/biz/action/action.go`（`Interrupt` 与 `Clear` 都清队列）
- **问题**：蓝本 `stop_actions` 会中断当前语音（bridge.py:442-467），`clear_actions` 只清队列；`interrupt` 同时终止 TTS 生产与喂流（engine.py:943-980）。Go 侧两者只发事件、都清队列，且不发 type7、不取消 TTS/Feeder，G21/G22 不通过；`ResetLlm` 返回 0 且不动 LLM（G07 不通过）。
- **证据**：代码；方案 F35、G21、G22、G07 判定标准。
- **修复建议**：定义统一的 `EngineControl`：Interrupt → cancel utterance + type7 + 清 TTS 队列；StopActions → 取消当前动作语音；ClearActions → 只清队列；ResetLlm 调用真实 chat 记忆并返回条数。

### P1-7 Koubo sticky stop / play_all 状态 / Split ID 冲突

- **位置**：`internal/biz/koubo/koubo.go`（`Stop` 置 `st.Stop=true` 后无复位；`PlayAll` 先检查 `Stopped()`；`PlayAll` 不设置 `Playing/PlayIdx`；`Split` 生成 ID 可能碰撞）；`internal/server/panels.go:605-615`
- **问题**：调用一次 `stop` 后，后续 `play/play_all` 永远返回 0；`play_all` 无片段/未开播也返回成功；播放不更新 `Playing/PlayIdx`，`state` 永远看不到进度；分段 ID 碰撞会覆盖 take。与蓝本 `play_all` 重置 `stop=False`、按段设置 `playing/play_idx`、无 live/无片段返回 409 的行为不符（koubo.py:306-640）。
- **证据**：代码；实测 `play_all` 返回 `started=true`；方案 F31。
- **修复建议**：`Stop` 只停当前播放，`PlayAll` 开始时复位 stop 标志；播放前校验 `isLive` 与片段非空（409）；写入 `Playing/PlayIdx` 并发布事件；Split ID 用 UUID/单调序号；补 sticky-stop 回归测试。

### P1-8 Song 未接 PCM/epoch/字幕优先级，解码播放均为 Fake

- **位置**：`internal/biz/song/song.go`；`cmd/dh/wire.go:160`（`media.FakeDecoder{}`、`NewFakePlayer`）
- **问题**：点播只做校验与歌词读取，不产出 24k PCM、不进入 Feeder、不设置 `song_playing`、无 `song_epoch` 失效；歌词优先级实现为 lrc > script > txt（与蓝本一致），但文件头注释写成 lrc > txt > script（文档矛盾）；F18/G10/G11/G12/G13 均不通过。
- **证据**：代码；方案 F18、G10–G13；蓝本 engine.py:1857-1931。
- **修复建议**：接入 ffmpeg 解码（或边车）→ 24k s16le；点播受理即置 playing/epoch；旧 epoch Feeder 一帧内退出；歌词事件优先于口播字幕；修正注释；补真实音频偏移测试。

### P1-9 Danmaku 无风控/无消费者/无持久化，pending 无界

- **位置**：`internal/biz/danmaku/danmaku.go`；`internal/service/danmaku_service.go`
- **问题**：`auto` 模式没有消费者（不触发 LLM/回复）；pending 列表无上限；无屏蔽词、`auto_only_words`、长度限制、结构攻击过滤（蓝本 engine.py:30-40 的 `<tag>`、连续大写、控制符、零宽、长重复字符过滤全部缺失）；无文本去重（符合蓝本）；`t2_skipped` 从不递增；`SetMode` 不持久化（G15）；gift/join 只记录计数，无合并/欢迎（F27/F28/G24/G25/G26）；`NoteCommand` 在动作真正调度前计数。
- **证据**：代码；方案 F21/F22/F24/F25；蓝本 bridge.py:737-766、dm_router.py:105-109、659-705、900-918。
- **修复建议**：实现 dm worker（8 条缓冲/最多 14 条/1.2s 冷场/VIP 5s/普通 3s）；接入结构过滤与词表；pending 有界；auto 入 LLM；模式落库并热应用；补压测 `accepted+dropped=total`。

### P1-10 安全面：原始错误回显、任意目录导入、目录泄漏、上传无单文件限制、metrics 反代暴露、密钥明文

- **位置**：`internal/server/panels.go:693-695`（`internal()` 直接把 `err.Error()` 返回给客户端）；`panels.go:103`（`/tts/import-folder` 接受任意本地路径）；`panels.go:89`（`/tts/files` 暴露绝对目录）；`panels.go:638-664`（200MB `ParseMultipartForm` 无单文件限制、失败时已保存文件不清理）；`internal/server/auth.go:87-93`（`/metrics` 仅按 `RemoteAddr` 判定回环）；配置 YAML 明文 collector secret
- **问题**：错误信息泄漏本地路径、DB/上游细节；拿到 Bearer 的用户（LAN 内任意管理员）可让服务读取/导入任意本地音频目录；反代部署时 `RemoteAddr` 是代理地址，`/metrics` 可能被外部访问；上传可打爆内存/磁盘；README 声称 Vidu Key 使用 DPAPI/AES-GCM 与只存尾 8 位，实际代码**没有**该实现。
- **证据**：代码；实测 raw 错误出现在 500 响应；README 与实现的差异。
- **修复建议**：对外错误统一映射为稳定错误码，详细仅入日志；限制 import-folder 为配置白名单根目录；`/tts/files` 返回相对名；逐文件大小/总大小限制 + 失败清理；metrics 用独立监听地址/Unix socket 或显式 token；密钥加密落盘并只回显尾 8 位（或删除 README 谎言）。

### P1-11 Ownership 强制接管路径双发 type7 / 无条件打断，与蓝本语义不符

- **位置**：`internal/engine/ownership.go:264-272`（排队超时先 `Interrupt` + 250ms），`ownership.go:~298-304`（随后 `forceTakeover` 再次 `Interrupt` + 250ms）
- **问题**：超时路径会连续两次 type7 并等待约 500ms，而蓝本 engine.py:824-948 的超时路径只做一次“收回并清理”；强制分支在“未在说话”时也无条件打断，增加不必要的静默；`respect_song/wait_only` 主流程存在，但 `type7` 的发送条件与蓝本不完全一致。
- **证据**：代码；蓝本 engine.py:824-948；方案 F13 判定标准。
- **复现方式**：单测注入“排队 25s 超时且 owner 未说话”，统计 type7 次数（当前应为 2）。
- **修复建议**：将“发送中断”收敛到单一 `interruptOnce(owner)`；仅在 `speaking==true` 时发 type7；超时路径只等待一次 250ms；补表驱动测试覆盖 4 轮/歌曲保护/续等。

### P1-12 Signaling 把 type10 当 Unknown 丢弃；close 事件可能丢失；无读上限

- **位置**：`internal/engine/vidu/signaling.go` 的 `readLoop` switch（只识别 5/6/11/12/14，`TypeOutputText=10` 落入 default 并 `Unknown=true`）；`events` 缓冲 32，close 事件非阻塞投递；未 `SetReadLimit`
- **问题**：type10 口播字幕是 F19/G12/G16 的唯一上行来源，当前被当未知事件丢弃；缓冲满时连接关闭事件可能丢，引擎无法及时判定会话断开；无读上限，异常对端可发超大帧。
- **证据**：`signaling.go`；蓝本 signaling.py:39-52；方案 F10/F19。
- **修复建议**：显式解析 type10（句子/序号/时间戳）并映射为字幕事件；close 事件用独立可靠通道/优先队列；`SetReadLimit`；补 10 种类型表驱动测试（当前测试未断言 type10 语义）。

### P1-13 Vidu 上传部分失败无清理；finish 缺 URI 时返回伪 URI；无 403 动作库降级

- **位置**：`internal/engine/vidu/client.go` 的 `Upload`（start 成功→PUT 失败/finish 失败无 orphan 清理；`fin.URI==""` 返回 `ssupload:?id=<id>`）；`CreateSession` 为通用 body，无 action 403 去 action 重建
- **问题**：三步上传中途中止会留下上游对象；伪 URI 会被当作合法资产写入，后续建场失败且难以定位；方案 F01/F09/G08 要求的 403 降级未实现。
- **证据**：代码；蓝本 client.py:99-198、engine.py:321-335。
- **修复建议**：失败路径调用删除/补偿接口（或记录 orphan 待清理）；finish 无 URI 返回明确错误 `vidu_upload_no_uri`；Create 包装 `createWithActionFallback` 并在业务层发布 `action_library_degraded`。

### P1-14 错误码/HTTP 映射不统一，分页缺失，响应信封混用

- **位置**：`internal/server/panels.go:689-735`（`badRequest/internal/mapSongErr/mapKouboErr` 各自为政）；生成路由错误直接 500；`/api/v1/sessions`、`/songs`、`/actions`、`/koubo/state` 等列表无分页
- **问题**：同语义错误在不同模块映射不同（song `ErrNotLive`→400，koubo `ErrNotLive`→409）；`panels` 用 `{ok,error}`，proto 路由用 `{code,message,details}`（未统一）；列表接口无 `page/page_size` 或 `after_seq`，方案要求分页统一；`LiveService` 的 `Say` idle 返回原始 error→500，而方案期望稳定业务码。
- **证据**：代码；实测 idle `say` 500；方案 §3 要求 proto 命名/错误码/分页统一。
- **修复建议**：定义统一 `errcode` 包与 HTTP 映射表；所有业务错误走 Kratos `errors.Error`；列表统一 `page/page_size` 或 `after_seq/limit`；响应信封统一为 proto 约定；补错误映射表测试。

### P1-15 测试“假通过”：service/data/repo 0%，鉴权测试未走真实路由

- **位置**：`internal/server/auth_test.go`（用 dummy handler 验证 filter，不构造 `NewHTTPServer` 真实路由表）；`internal/service`/`internal/data`/`internal/data/repo`/`cmd/dh` 覆盖率 0%
- **问题**：现有“全绿”不能证明任何一条真实 HTTP 路由经过鉴权/参数映射/错误映射；WS 15s、并发 Start/Stop、koubo sticky stop、play_all ctx、HMAC body 篡改、Ent 并发 revision、Switchable 回填均无测试。`-race` 只覆盖单测，不覆盖上述并发场景。
- **证据**：覆盖率表；`auth_test.go` 结构；无对应测试文件名/grep。
- **修复建议**：新增“真实路由表”集成测试（`NewHTTPServer` + `httptest`，遍历所有非 GET 路由断言 401/200）；WS 空闲 >30s 测试；并发 Start/Stop×100 + `-race`；koubo stop→play_all 回归；HMAC 篡改/重放；Ent 合约测试（testcontainers 或本地 PG，需环境支持）；CI 覆盖率门禁。

### P1-16 工程化：CI 缺 race/fmt/coverage，README 与实现不符

- **位置**：`.github/workflows/ci.yml`（仅 vet/build/test，无 `-race`、无 `gofmt -l`、无覆盖率、无 govulncheck）；`Makefile`（`ci` 无 race/fmt/coverage）；`README.md`（称当前 M0、真实 Vidu/OmniVoice/ffmpeg 未接入、Fake 不可用；同时声称 Vidu Key DPAPI/AES-GCM + 只存尾 8 位，代码不存在）
- **问题**：CI 无法阻止“格式/竞态/覆盖率回退”；README 状态与代码中的 62 条 M3 面板路由、settings/mode 等相矛盾；安全声明与实现不符，会误导验收。
- **证据**：`ci.yml`、`Makefile`、`README.md` 与 `wire.go`/schema 对照。
- **修复建议**：CI 增加 `gofmt -l`、`go test -race`、覆盖率阈值、`govulncheck`；README 按“M0/M1/M2/M3 实际状态”重写，删除未实现的安全声明；Makefile `ci` 与 CI 命令一致；`.gitignore` 增加嵌套 storage（`**/storage/`）。

### P1-17 Clean-room 风险：逐字复制蓝本默认文案与音色 ID

- **位置**：`internal/biz/tts/tts.go:113-115`（`tw-xiaoya`/`zh-xiaoxiao`/`en-jenny`）、`tts.go:157`（`"你好，这是音色试听。"`）
- **问题**：`tw-xiaoya` 是蓝本 koubo 默认音色（koubo.py:195）；默认试听文案与蓝本 bridge.py:819 **逐字相同**。方案 §0 明确“不做代码级克隆、素材/克隆音色不在授权内”，逐字文案与蓝本音色标识会带来合规风险。
- **证据**：上述两处对照；方案 NOTICE 约束。
- **修复建议**：替换为自研/已授权音色与自写试听文案；在提交前做字符串相似度扫描；保留 clean-room 记录。

### P1-18 方案核心业务模块缺失（M2/M3）

- **位置**：`internal/biz/`（无 chat/llm、gift、patron、persona、avatar、vidukey、rtc、record、replay、dm_worker）
- **问题**：F15/F16/F27/F28/F29/F34/F38/F39/F42/F49、G01–G09、G12–G18、G22、G24–G27 无后端实现；`/api/v1/sessions` 只有复盘列表/导出，无录像回放；`billing` 只有手动文件 ledger，无 credits 轮询基线（F36/G03）。
- **证据**：目录/路由/F 表。
- **修复建议**：按方案里程碑补齐；优先 M1 的 greeting/chat/LLM/credits/RTC；M3 的 gift/patron/persona/avatar/vidukey/record。

### P1-19 健康/指标与蓝本字段不对齐

- **位置**：`internal/server/health.go`；`internal/server/http.go:45-52`
- **问题**：只有 `/healthz`，无蓝本 `/api/health` 别名；`capabilities` 恒 false；无 `feed_lag_ms`/`ws_reconnect_total` 等 P0 指标；`wsConns` 字段未使用；`/metrics` 只靠 RemoteAddr 回环判定（见 P1-10）。F51 的 ①/② 不通过。
- **证据**：代码；方案 F51、§4.9。
- **修复建议**：加 `/api/health` 别名；字段与蓝本对齐；capabilities 由真实装配计算；暴露 feed/WS/queue 指标；metrics 独立监听。

### P1-20 Billing/Archive 数据完整性风险

- **位置**：`internal/biz/billing/billing.go`（文件 JSON 损坏时静默当空）；`internal/biz/archive/archive.go`（`fetchEvents` 20×500 上限静默截断；CSV 无公式注入转义；成本读取错误忽略）
- **问题**：账本损坏会显示“0 成本”；导出超过 1 万事件静默缺失；CSV 以 `= + - @` 开头的文本在 Excel 中可执行公式（公式注入）；导出文件名来自 ID，缺少内容类型/大小限制。
- **修复建议**：损坏文件报错并备份，不静默清空；导出显式提示截断或流式分页；CSV 单元格转义；补数据完整性测试。

---

## 4. P2（可排期）

| 编号 | 位置 | 问题 | 证据 | 修复建议 |
|---|---|---|---|---|
| P2-1 | `internal/data/data.go` | `Close` 同时关闭 Ent driver 与 `sql.DB`，可能重复关闭/报错 | 静态 | 只关一层；`sync.Once` |
| P2-2 | `internal/biz/live/live.go:321-322` | `Ended` 后 `DurationS` 仍增长（属 P0-5 子项） | 实测 | 冻结 endedAt-start |
| P2-3 | `internal/server/auth.go:44-63` | 过期 ticket 只在下次 Issue 时清理；无后台 GC | 静态 | 定时 GC |
| P2-4 | `internal/biz/live/fake.go` | `sessionID[:8]` 会 panic（短 ID） | 静态 | 长度保护 |
| P2-5 | `internal/server/health.go` | `wsConns` 死字段；capabilities 恒 false | 静态 | 删除或接线 |
| P2-6 | `internal/server/audit.go` | 审计无响应状态码；actor 恒 `local`；写失败忽略 | 静态 | 记录 status/actor；失败计数 |
| P2-7 | `internal/biz/choreo/choreo.go` | 步骤最小时长按 2s 归一，方案/蓝本下限 0.3s | 静态 | 对齐 0.3s 或说明 |
| P2-8 | `.gitignore` | 只忽略根 `/storage`，未覆盖嵌套 storage | 静态 | 加 `**/storage/` |
| P2-9 | `internal/server/panels.go:46-50 / 283-286` | `/tts/voices`、`/prompt` 硬编码 `source=fake` | 静态 | 由真实装配决定 |
| P2-10 | `internal/biz/live/live.go:64` | `m.seq` 未使用，易误用 | 静态 | 删除 |
| P2-11 | `internal/server/panels.go:386-414` | `/sessions` 列表无分页，导出文件名来自 ID | 静态 | 分页/内容协商 |
| P2-12 | `internal/engine/vidu/client.go` | 429 `Retry-After` 解析后未用于退避；重试固定 1s 无抖动 | 静态 | 按 Retry-After + jitter |
| P2-13 | `internal/biz/live/live.go:266` | Recover 只处理 >60s 的 stale 行，重启前 <60s 崩溃的场次不会被恢复（需确认方案意图） | 静态 | 启动时以“进程已重启”为准，cutoff 归零或配合 epoch |
| P2-14 | `internal/server/panels.go:305-320` | `refs` 图片数量/类型校验依赖 fake；type14 真实回显未接 | 静态 | 真实信令 + 负向测试 |
| P2-15 | `README.md` / `Makefile` / `ci.yml` | 文档命令可跑，但与实际状态/门禁不一致（详见 P1-16） | 实测 `make vet` 成功 | 统一文档与 CI |

---

## 5. 全量路由与鉴权清单（86 条）

鉴权规则（`internal/server/auth.go`）：

- `local`：除 `/healthz` 外全部要求 `RemoteAddr` 回环；`/metrics` 回环；`/ws/v1/events` 回环 + handler 内 ticket。
- `lan`：除 `/healthz`、`/metrics`（回环）、`/ws/v1/events`（ticket）外，全部要求 `Authorization: Bearer <admin token>`。
- `collector`：`/api/danmaku` 与 `/api/v1/danmaku` 要求 HMAC；其余同 lan（要求 Bearer）。
- 未识别 mode 在配置校验阶段拒绝。

**结论：未发现“非 GET 且完全无鉴权”的路由。** 用户点名的 `/api/danmaku`、`/tts/*`、`/actions*`、`/songs*`、`/koubo*`、`/prompt*`、`/sessions*`、`/cost*`、`ws-ticket` 全部被 Bearer/HMAC 覆盖；`/ws/v1/events` 由一次性 ticket 覆盖。风险不在“漏保护”，而在 P0-4（HMAC 不签 body）与 P1-1（ticket 不绑会话）。

### 5.1 基础设施

| 方法 | 路径 | 保护 |
|---|---|---|
| GET | `/healthz` | 公开 |
| GET | `/metrics` | 仅回环 |
| POST | `/api/v1/ws-ticket` | Bearer（lan/collector）/ 回环（local） |
| GET | `/ws/v1/events` | ticket（handler 内消费；lan/collector）；回环（local） |

### 5.2 Live（11）

| 方法 | 路径 | 保护 |
|---|---|---|
| POST | `/api/v1/live/start` | Bearer |
| POST | `/api/v1/live/stop` | Bearer |
| GET | `/api/v1/live/status` | Bearer |
| POST | `/api/v1/live/say` | Bearer |
| POST | `/api/v1/live/command` | Bearer |
| POST | `/api/v1/live/interrupt` | Bearer |
| POST | `/api/v1/live/transcript/clear` | Bearer |
| POST | `/api/v1/live/actions/stop` | Bearer |
| POST | `/api/v1/live/actions/clear` | Bearer |
| POST | `/api/v1/live/llm/reset` | Bearer |
| GET | `/api/v1/live/events` | Bearer |

### 5.3 Danmaku（6）

| 方法 | 路径 | 保护 |
|---|---|---|
| POST | `/api/v1/danmaku` | Bearer（lan）/ HMAC（collector） |
| POST | `/api/danmaku` | Bearer（lan）/ HMAC（collector） |
| POST | `/api/v1/danmaku/mode` | Bearer |
| POST | `/api/v1/danmaku/confirm` | Bearer |
| POST | `/api/v1/danmaku/dismiss` | Bearer |
| GET | `/api/v1/danmaku/pending` | Bearer |

### 5.4 Settings（3）

| 方法 | 路径 | 保护 |
|---|---|---|
| GET | `/api/v1/settings` | Bearer |
| POST | `/api/v1/settings` | Bearer |
| POST | `/api/v1/settings/reload` | Bearer |

### 5.5 TTS（8）

| 方法 | 路径 | 保护 |
|---|---|---|
| GET | `/api/v1/tts/voices` | Bearer |
| POST | `/api/v1/tts/voice` | Bearer |
| POST | `/api/v1/tts/preview` | Bearer |
| GET | `/api/v1/tts/files` | Bearer |
| POST | `/api/v1/tts/upload` | Bearer |
| POST | `/api/v1/tts/import-folder` | Bearer |
| POST | `/api/v1/tts/delete` | Bearer |
| GET | `/api/v1/tts/queue` | Bearer |

### 5.6 Actions（8）

| 方法 | 路径 | 保护 |
|---|---|---|
| GET | `/api/v1/actions/libraries` | Bearer |
| POST | `/api/v1/actions/libraries` | Bearer |
| DELETE | `/api/v1/actions/libraries/{id}` | Bearer |
| GET | `/api/v1/actions` | Bearer |
| POST | `/api/v1/actions` | Bearer |
| DELETE | `/api/v1/actions/{id}` | Bearer |
| GET | `/api/v1/actions/queue` | Bearer |
| POST | `/api/v1/actions/send` | Bearer |

### 5.7 Choreo（8）

| 方法 | 路径 | 保护 |
|---|---|---|
| GET | `/api/v1/choreo` | Bearer |
| POST | `/api/v1/choreo` | Bearer |
| POST | `/api/v1/choreo/groups` | Bearer |
| DELETE | `/api/v1/choreo/groups/{id}` | Bearer |
| POST | `/api/v1/choreo/start` | Bearer |
| POST | `/api/v1/choreo/stop` | Bearer |
| POST | `/api/v1/choreo/run-once` | Bearer |
| GET | `/api/v1/choreo/status` | Bearer |

### 5.8 Prompt / Refs（3）

| 方法 | 路径 | 保护 |
|---|---|---|
| GET | `/api/v1/prompt` | Bearer |
| POST | `/api/v1/prompt` | Bearer |
| POST | `/api/v1/prompt/type14` | Bearer |

### 5.9 Songs（6）

| 方法 | 路径 | 保护 |
|---|---|---|
| GET | `/api/v1/songs` | Bearer |
| POST | `/api/v1/songs/upload` | Bearer |
| POST | `/api/v1/songs/play` | Bearer |
| POST | `/api/v1/songs/stop` | Bearer |
| DELETE | `/api/v1/songs/{name}` | Bearer |
| GET | `/api/v1/songs/status` | Bearer |

### 5.10 Sessions / Cost / Danmaku state（7）

| 方法 | 路径 | 保护 |
|---|---|---|
| GET | `/api/v1/sessions` | Bearer |
| GET | `/api/v1/sessions/{id}` | Bearer |
| GET | `/api/v1/sessions/{id}/export` | Bearer |
| GET | `/api/v1/cost/ledger/{id}` | Bearer |
| POST | `/api/v1/cost/ledger` | Bearer |
| GET | `/api/v1/cost/summary` | Bearer |
| GET | `/api/v1/danmaku/state` | Bearer |

### 5.11 Koubo（14）

| 方法 | 路径 | 保护 |
|---|---|---|
| GET | `/api/v1/koubo/state` | Bearer |
| POST | `/api/v1/koubo/split` | Bearer |
| POST | `/api/v1/koubo/seg` | Bearer |
| POST | `/api/v1/koubo/merge` | Bearer |
| POST | `/api/v1/koubo/seg/split` | Bearer |
| POST | `/api/v1/koubo/tts` | Bearer |
| POST | `/api/v1/koubo/tts_all` | Bearer |
| POST | `/api/v1/koubo/select` | Bearer |
| POST | `/api/v1/koubo/action` | Bearer |
| POST | `/api/v1/koubo/actions_all` | Bearer |
| POST | `/api/v1/koubo/play` | Bearer |
| POST | `/api/v1/koubo/play_all` | Bearer |
| POST | `/api/v1/koubo/stop` | Bearer |
| GET | `/api/v1/koubo/audio/{name}` | Bearer |

---

## 6. 方案符合度（F01–F51 / G01–G30）

判定口径严格按用户要求：**已实现且可测 / 只实现形状 / 未实现 / 实现但与蓝本不符**。前端专有条目标注“不在后端范围”。

### 6.1 F01–F51

| ID | 判定 | 后端证据 / 说明 |
|---|---|---|
| F01 开播 | 只实现形状 | `Manager.Start` 有 reconnect 语义但非原子；`FakeController`；无 120s/403 降级/旧场 ended |
| F02 收播 | 只实现形状 | 0.4s 收尾存在；忽略 session_id/reason/错误；starting 阶段不取消建场 |
| F03 状态/增量 | 实现但与蓝本不符 | `live_service.go:42-60` 硬编码指标；忽略两个游标；另提供 HTTP 事件列表（F35 相反） |
| F04 WS 生命周期 | 已实现且可测 | `signaling.go` 15×2s、ping/pong、NOT_READY 重发；单测；未接线 |
| F05 断线/看门狗 | 未实现 | 无 watchdog 包；`internal/engine` 仅 feeder/ownership/state/vidu/tts |
| F06 异常清理 | 只实现形状 | 仅 Recover 标记；无 goleak/去重/finalize 清理 |
| F07 创建会话 | 只实现形状 | Vidu `CreateSession` 通用 body；无 avatar_missing/managed_rtc 组包；未接线 |
| F08 查询/录播/额度/列表 | 已实现且可测 | `client.go` 超时 8/15/8/8、`pager.page/pagesz`、404 分类；单测；未接线 |
| F09 资产/上传 | 只实现形状 | 三步上传/etag 校验有；资产 90 天/回滚/删除无 |
| F10 WS 信令 | 实现但与蓝本不符 | 常量齐；type10 下行被当 Unknown 丢弃；close 事件可丢 |
| F11 24k s16le | 已实现且可测 | `pkg/audio` 48B/ms + 单测 |
| F12 节奏/漂移 | 已实现且可测 | 绝对时基/不跳帧/往返告警；单测；8h 真机未验证 |
| F13 喂流权抢占 | 实现但与蓝本不符 | 主状态机有；超时路径双 type7/500ms、force 无条件打断（蓝本 engine.py:824-948） |
| F14 发送超时 | 已实现且可测 | Feeder 单帧 3s；未接线；指标未暴露 |
| F15 LLM 流式 | 未实现 | 无 biz/chat/llm |
| F16 短句合并 | 未实现 | 同上 |
| F17 TTS 队列 | 只实现形状 | 边车契约/熔断有；无 maxsize=8 生产队列/逐句整段调度 |
| F18 整段/音频文件 | 只实现形状 | 歌词读取有；无 PCM/song_playing/epoch |
| F19 type10 | 未实现 | 下行 type10 被丢；无 captions 开关/句子事件 |
| F20 音色/预览/文件 | 只实现形状 | 8 路由全；FakeSynth；无 ≤200 字校验/同名确认 |
| F21 弹幕接入 | 只实现形状 | 200+ok/reason、off 生效；无结构过滤/长度 |
| F22 T0 精确命中 | 未实现 | 无词表/auto_only/冷却 |
| F23 T2 模式 | 只实现形状 | 三态存储；auto 无消费者；SetMode 不持久化 |
| F24 批读/风控 | 未实现 | 无 dm worker/14 条/1.2s/t2skip |
| F25 VIP 优先 | 未实现 | 无 VIP 队列 |
| F26 动作优先级 | 实现但与蓝本不符 | 3/2/1/0+冷却+队列 3 有；`SendDirect` 与 lastAt 更新有竞态；fake sender |
| F27 礼物合并 | 未实现 | 无 gift |
| F28 礼物欢迎/贵宾礼 | 未实现 | 无 gift |
| F29 贵宾名单 | 未实现 | 无 patron/路由 |
| F30 编排 | 只实现形状 | CRUD/start/pause；start 用请求 ctx；无 VIP 跳过/随机 |
| F31 口播编排 | 实现但与蓝本不符 | 14 路由全；sticky stop、play_all ctx、ID 碰撞、文件存储 |
| F32 歌曲点播 | 只实现形状 | FakeDecoder/FakePlayer；无 PCM/偏移/epoch |
| F33 参考图 | 只实现形状 | `FakeSignaler`；无真实 type14 回显 |
| F34 录播 | 未实现 | 无 biz/record；client `GetRecording` 存在 |
| F35 人工流水/事件 | 实现但与蓝本不符 | 提供 `GET /live/events`（蓝本无事件列表 HTTP）；4 个中断语义不符 |
| F36 统计/成本 | 未实现 | 手动文件 ledger；无 credits 轮询/基线 |
| F37 设置聚合 | 只实现形状 | 12 组/GET/POST/reload 路由；吞错、effective 空、Reload 空 |
| F38 人设 | 未实现 | 无 persona biz/路由 |
| F39 头像 | 未实现 | 无 avatar biz/路由 |
| F40 动作库 | 只实现形状 | 库/动作 CRUD；fake；无 403 降级/引用置空 |
| F41 音频素材 | 只实现形状 | 上传/删除/列表；无解码校验/时长/引用确认 |
| F42 Vidu Key | 未实现 | 仅 `ViduKey` schema，无路由/biz |
| F43 舞台主区 | 不在后端范围 | 前端 |
| F44 面板与右栏 | 不在后端范围 | 前端；后端面板路由已提供形状 |
| F45 底部输入条 | 不在后端范围 | 前端 |
| F46 设置抽屉 | 不在后端范围 | 前端；后端 settings DTO 存在 |
| F47 面板拆分 | 不在后端范围 | 前端 |
| F48 本地预设 | 不在后端范围 | 前端 |
| F49 RTC 播放/token | 未实现 | 无 RTC token/biz |
| F50 复盘页 | 只实现形状 | sessions list/detail/export；无 replay/recording |
| F51 健康/指标 | 实现但与蓝本不符 | 仅 `/healthz`；capabilities 恒 false；无 feed/WS 指标；metrics 回环判定脆弱 |

### 6.2 G01–G30

| ID | 判定 | 后端证据 / 说明 |
|---|---|---|
| G01 开场白 | 未实现 | `Start` 接收 `greeting` 但忽略 |
| G02 token_expire_at | 未实现 | 无 RTC |
| G03 额度轮询 | 未实现 | 无 credits 轮询 |
| G04 avatar_note/restart_required | 未实现 | 无 persona |
| G05 性格 prompt 合并 | 未实现 | 无 chat/persona |
| G06 LLM 超时降级 | 未实现 | 无 LLM |
| G07 清空 LLM 记忆 | 实现但与蓝本不符 | `ResetLlm` 返回 0，仅发事件 |
| G08 动作库 403 降级 | 未实现 | Vidu client 无 fallback |
| G09 开场白音频优先 | 未实现 | 无 greeting |
| G10 歌单 PCM 不经 TTS | 未实现 | FakeDecoder，无 PCM |
| G11 歌词 lrc/txt | 只实现形状 | 读取 lrc/script/txt；无时间轴字幕 |
| G12 歌词字幕优先 | 未实现 | 无字幕仲裁 |
| G13 song_epoch | 未实现 | 无 epoch 递增/旧喂流退出 |
| G14 随机表演 | 只实现形状 | choreo run-once/pause；无随机间隔/偏置 |
| G15 弹幕模式持久化 | 未实现 | SetMode 不落库 |
| G16 字幕层开关 | 未实现 | 无 captions 开关 |
| G17 资产 90 天 | 未实现 | 无资产列表/回收 |
| G18 Vidu Key 尾 8 位 | 未实现 | 仅 schema |
| G19 面板拖拽记忆 | 不在后端范围 | 前端 |
| G20 右栏事件上限 | 不在后端范围 | 前端 |
| G21 Stop/Clear 区分 | 实现但与蓝本不符 | 两者都清队列，Stop 不打断语音（bridge.py:442-467） |
| G22 interrupt 终止 TTS/喂流 | 未实现 | 只发事件 |
| G23 read_interval 秒/ms | 不在后端范围 | 前端/契约；后端无明显转换 |
| G24 观众加入欢迎 | 未实现 | 无 patron |
| G25 once_per_session | 未实现 | 无 patron |
| G26 礼物名过滤 | 未实现 | 无 gift |
| G27 take 音频缓存复用 | 未实现 | 无文本→缓存命中 |
| G28 口播串行 TTS 锁 | 已实现且可测 | `synthMu` 串行；`TTSAll` 用 `context.WithoutCancel` |
| G29 save/reload 分离 | 实现但与蓝本不符 | 路由分离且审计；Reload 空操作 |
| G30 停止保留画面 | 不在后端范围 | 前端 |

---

## 7. 实时链路与蓝本逐项对照（语义偏差）

| 能力 | 蓝本证据 | Go 位置 | 偏差 | 严重度 |
|---|---|---|---|---|
| 喂流权抢占 | `src/engine.py:824-948` | `internal/engine/ownership.go` | 主流程对齐（preempt/respect_song/wait_only/4 轮/1.5s/25s 队列/type7+250ms）；超时路径二次 type7+500ms；force 无条件下发中断；未接线 | P1 |
| 喂流节奏 | `src/engine.py:1703-1761` | `pkg/audio/pacing.go`、`internal/engine/feeder.go` | 绝对时基、48B/ms、不跳帧、3s 单帧超时对齐；Windows busy-wait 精度补偿；未接线；8h 未验证 | 已实现/未验证 |
| 断流看门狗 | `src/engine.py:433-490` | 无 | 无 75s/3 采样/20s 窗口/0.55x/6 次/20s 间隔实现 | P1（F05 未实现） |
| 信令类型 | `src/vidu/signaling.py:39-52` | `internal/engine/vidu/signaling.go` | type10 被 `Unknown` 丢弃；close 事件可丢；无读上限 | P1 |
| 结构攻击过滤 | `src/engine.py:30-40` | `internal/biz/danmaku/danmaku.go` | 无 `<tag>`/连续大写/控制符/零宽/长重复过滤 | P1 |
| 歌曲/歌词 | `src/engine.py:1857-1931` | `internal/biz/song/song.go` | 歌词优先级对齐；无 PCM/song_playing/epoch；注释与实现矛盾 | P1 |
| 动作 Stop/Clear | `src/bridge.py:442-467` | `internal/biz/live/live.go:306-307` | Stop 应打断语音，Go 两者只清队列/发事件 | P1 |
| interrupt | `src/engine.py:943-980` | `internal/biz/live/live.go:295` | 只发事件，不取消 TTS/Feeder/type7 | P1 |
| 开场白 | `src/engine.py:352-365` | `internal/biz/live/live.go:136` | `greeting` 参数被忽略 | P1 |
| play_all | `src/koubo.py:306-640` | `internal/biz/koubo/koubo.go`、`panels.go:605-615` | stop 不复位；不设置 playing/play_idx；ctx 必断 | P0/P1 |

---

## 8. 测试质量与“假通过”

**真实通过**：`go test ./...` 与关键包 `-race` 均通过；库级逻辑（Vidu client/signaling、TTS client、Feeder、Ownership、EventBus、Pacer）确实有表驱动/契约测试。

**假通过与缺口（按风险排序）**：

1. `internal/service` 0%：生成路由的 handler→service 映射、参数校验、错误映射无任何测试。
2. `internal/data`/`repo` 0%：Ent 唯一约束、事务、revision、Switchable 回填、Recover 全部无测试。
3. `auth_test.go` 用 dummy handler，不是 `NewHTTPServer` 真实路由表：无法发现“新增路由漏保护/路径拼错/生成器变更”。
4. 无 WS 长连测试：15s 断开这种 P0 只能靠真实冒烟发现。
5. 无并发 Start/Stop 行为测试：`-race` 不报逻辑竞态，需状态断言。
6. 无 koubo sticky-stop / play_all ctx 回归测试。
7. 无 HMAC body 篡改/重放测试（现有测试只覆盖“正确签名/错误签名”）。
8. `internal/engine/vidu` 测试未断言 type10 语义（因此 Unknown 丢弃未被发现）。
9. 无真实 Vidu/OmniVoice/RTC/PG/Redis 集成测试（环境限制，但方案要求 [真机]）。
10. 覆盖率无 CI 门禁，回退不可见。

**结论**：当前“全绿”只能证明编译与库级单测通过，不能作为 M1/M2 验收证据。方案 §2.4 中所有 `[真机]` 与故障注入项目均未验证。

---

## 9. 工程化结论

| 项 | 结论 | 证据 |
|---|---|---|
| `cmd/dh` 唯一入口 | 通过 | 全仓仅 `cmd/dh/main.go` |
| 分层 | 基本守住，但 service 层薄 | `api/gen → server/service → biz → data/pkg`；`internal/server` 直接 import 10 个 biz 包并承载 62 条面板路由（handler 内含较多业务编排，见 P0-6/P1-7） |
| Wire 一致性 | 通过（生成物与定义一致），但装配全是 Fake | `wire.go`/`wire_gen.go` |
| proto 命名/错误码/分页 | 不统一 | 见 P1-14；列表无分页；错误码未集中 |
| Makefile | 可跑 | `make vet` 实测成功；但 `ci` 缺 race/fmt/coverage |
| CI | 可跑但门禁弱 | `.github/workflows/ci.yml` 仅 vet/build/test + 前端 npm |
| README | 命令可跑，状态/安全声明失真 | `README.md` 与 `wire.go`/schema 对照 |
| `.gitignore` | 基本正确 | 忽略 `/bin`、`*.exe`、`web/node_modules`、`web/dist`、`/var`、`/storage`；未覆盖嵌套 storage |
| Lockfile | 无“双 lockfile”问题 | 仅 `web` 依赖树内有第三方 `yarn.lock`，非项目双锁 |
| 依赖 | 版本正常，完整性通过 | `go mod verify` 全部 verified；无异常直接依赖 |
| 可复现构建 | 可编译；未用 `-trimpath`/固定 toolchain 校验 | `go build ./...` 通过；建议 CI 固定 Go 版本 |
| 漏洞扫描 | 未执行 | 约束不安装 `govulncheck` |

---

## 10. 可直接执行的修复清单（按优先级）

### 10.1 立即（阻断交付，P0）

1. **真实装配 + fail-closed**：新增 `provideRealRuntime`（Vidu HTTP/WS、engine/tts 边车、Ownership/Feeder/StateMachine、RTC token）；按配置选择；未配置时 `live/start` 返回 `engine_not_configured`，删除 `fake-live` 成功路径，health 标注 `engine=unconfigured`。（P0-1）
2. **WS 长连**：为 `/ws/v1/events` 使用独立 `http.Server`（无全局 ReadTimeout）或绕过 `khttp.Timeout`；补 60s 空闲存活测试。（P0-2）
3. **生命周期串行化**：`Manager` 增加 `opMu`/op_id；Start 全程持锁或 single-flight；Stop 取消在途建场 ctx；读取 `Idempotency-Key` 头；幂等落 Redis；并发 Start/Stop×100 测试。（P0-3）
4. **HMAC 加固**：签名覆盖 method+path+body hash+ts+nonce；nonce 短期去重；强制 TLS；补篡改/重放负向测试。（P0-4）
5. **状态去伪**：`GetLiveStatus` 全部取自真实 Engine/Manager 快照；实现 `session_id`/`transcript_since`；`Ended` 冻结时长；transcript 写入。（P0-5）
6. **后台任务 ctx**：`play_all`/`choreo/start` 改用服务生命周期 ctx（`context.WithoutCancel` + 任务取消），由 job 状态机管理；handler 仅在调度成功后返回；补“返回后仍在跑”测试。（P0-6）
7. **优雅停机**：SIGINT/SIGTERM 顺序停止 Feeder→Manager.Stop→Scheduler/Choreo/Koubo→Data；带超时；goleak 测试。（P0-7）
8. **数据切换/恢复**：DB 就绪后再次 `Recover`；Switchable 增加内存→Ent 回填或 fail-closed；清理 DB 中幽灵 live 行；补延迟就绪集成测试。（P0-8）

### 10.2 交付前（P1，按依赖顺序）

9. ticket 绑定 session + Upgrade 成功后再消费；跨会话拒绝测试。（P1-1）
10. 设置保存改行锁/乐观锁；校验/冲突→409，DB 故障→5xx；Reload 真实 apply；保存热应用 danmaku mode。（P1-2）
11. 场次持久化改 `ON CONFLICT` + 状态迁移校验 + persist 串行化；Stop 校验 session_id、传播错误、可取消建场。（P1-3）
12. 事件 seq 按 session 持久化单调；epoch 在开播/切歌/强制回收递增；订阅按 session 过滤；慢订阅改为回放而非关 channel。（P1-4）
13. Ent：ViduKey partial unique active；补 TTL/90 天清理 job；按方案补齐核心实体；文件型 store 收敛或标注单机模式。（P1-5）
14. 实现 EngineControl：Interrupt/StopActions/ClearActions/ResetLlm 真实语义。（P1-6）
15. Koubo：Stop 只停当前播放、PlayAll 复位并写 playing/play_idx、校验 live/片段、ID 用 UUID；补回归测试。（P1-7）
16. Song：接 ffmpeg/边车解码→24k PCM→Feeder；song_playing/epoch；歌词字幕仲裁；修注释。（P1-8）
17. Danmaku：dm worker（缓冲/14 条/1.2s/VIP/3s/20s）+ 结构过滤 + 词表 + pending 有界 + 模式持久化 + 压测。（P1-9）
18. 安全面：统一错误码、禁止回显原始错误；import-folder 白名单根；逐文件大小限制 + 失败清理；metrics 独立监听；密钥加密/尾 8 位（或改 README）。（P1-10）
19. Ownership：中断单次化、仅 speaking 发 type7；补 4 轮/歌曲/超时/force 表驱动测试。（P1-11）
20. Signaling：type10 显式解析；close 可靠投递；SetReadLimit；10 类型全测。（P1-12）
21. Vidu 上传：失败补偿/orphan 清理；finish 无 URI 报错；Create 的 403 action 降级。（P1-13）
22. 错误码/分页统一：集中 `errcode` 包与 HTTP 映射；列表分页；响应信封统一。（P1-14）
23. 测试门禁：真实路由表鉴权遍历、WS 长连、并发 Start/Stop、HMAC 篡改、Ent 合约、Switchable 回填；CI 加 `-race`/coverage 阈值。（P1-15）
24. 工程化：CI 加 `gofmt -l`/`govulncheck`；README 按真实状态重写；Makefile 与 CI 对齐；`.gitignore` 加嵌套 storage。（P1-16）
25. Clean-room：替换 `tw-xiaoya` 等蓝本音色 ID 与逐字试听文案；做字符串扫描。（P1-17）
26. 业务补齐（M2/M3）：chat/LLM、greeting、credits、RTC、gift、patron、persona/avatar、vidukey、record/replay。（P1-18）
27. 健康/指标：`/api/health` 别名、capabilities 真实、feed/WS 指标、metrics 独立监听。（P1-19）
28. Billing/Archive：损坏文件报错备份；导出截断显式提示/流式；CSV 公式注入转义。（P1-20）

### 10.3 可排期（P2）

29. 修 `data.Close` 重复关闭、`DurationS` 冻结、ticket GC、fake panic、死字段、审计状态码、choreo 0.3s、`.gitignore`、`source=fake` 硬编码、删除 `m.seq`、sessions 分页、Retry-After/jitter、Recover 60s 口径、refs 真实回显。

### 10.4 验收命令（修复后应全绿）

```bash
gofmt -l .            # 期望无输出
go vet ./...          # 期望 0
go build ./...        # 期望 0
go test -count=1 ./...
go test -race -count=1 ./internal/... ./pkg/...
go test -coverprofile=cover.out ./...   # service/data/repo/cmd 不再是 0%
# 真实进程
go run ./cmd/dh -config <real.yaml>
# 1) 未配置引擎 → live/start 必须 fail-closed
# 2) WS 空闲 60s 不中断
# 3) collector 篡改 body / 重放 nonce → 401
# 4) 并发 live/start ×100 → 仅一场
# 5) koubo stop → play_all → 有进度
```

---

## 11. 审查结论

- **可交付性**：**不可交付**（按 v2.0 的 M1/M2 门禁）。当前是合格的 M0 骨架与较完整的库级实现，但不是可运行的直播后端。
- **最需要肯定的部分**：Vidu HTTP/TTS 边车/Feeder/Ownership/EventBus/Pacer 的库级实现质量较好，边界、超时、错误分类、单测覆盖明显高于一般脚手架；分层与 Wire/Proto 脚手架规范；鉴权链默认拒绝、审计与 health 具备。
- **最大风险**：不是“代码写得差”，而是**生产装配与验收证据错位**——库级实现已存在但未接线，路由形状已存在但行为为 Fake，测试“全绿”但没有覆盖真实链路。必须先关闭 8 条 P0，再用真实 Vidu/边车/PG/Redis/8h 压测重跑 F01–F19，才能进入交付评审。
</content>

# digital-human

数字人直播（S2）的新实现：Go 后端（Kratos/Ent/Wire）+ Quasar 控制台。
行为对齐以 `cyber-xiaoya-live-main` 为参照，但代码为 clean-room 重写，不复制其 AGPL 源码。

当前进度：**M0（骨架）**。真实 Vidu / OmniVoice / ffmpeg 尚未接入，相关能力为 `false` 并由
`/healthz` 显式暴露；不得把 M0 的 fake 行为当作可用功能。

## 环境要求

- Go 1.26+
- Node 24 + npm（本仓库不使用 pnpm，避免双 lockfile）
- protoc、protoc-gen-go、protoc-gen-go-http（仅生成代码时需要）
- 可选：PostgreSQL、Redis、ffmpeg、Docker

## 启动后端

```powershell
go build -o bin\dh.exe ./cmd/dh
.\bin\dh.exe -conf configs\config.yaml
```

默认监听 `127.0.0.1:8795`，安全模式 `local`（只允许本机回环访问）。

```powershell
curl.exe -i http://127.0.0.1:8795/healthz
```

缺 PG/Redis 时 `/healthz` 默认仍返回 **200**（进程可服务即存活），以 `status=degraded` + `missing` 名单显式标注缺失依赖；`health.require_dependencies=true` 恢复旧行为返回 **503**。降级状态对运维显式可见（不是失败，也绝不静默）。

## 引擎模式（fail-closed）

`engine.mode` 只有两档（`configs/config.yaml` 默认 `real`）：

| 模式 | 行为 |
|---|---|
| `real`（默认） | 必须同时具备 `DH_VIDU_KEY`、`DH_OMNIVOICE_ADDR`，且真实适配器已装配；任一缺失时 `StartLive` 返回 **503**（`error_code=engine_not_configured`），**不会退回 fake 成功**。 |
| `dev` | 显式装配 fake 引擎，仅供本机联调；启动时日志打 `WARN using fake live controller`。 |

`/healthz` 的 `capabilities.vidu/omnivoice/ffmpeg` 在未接入真实能力时恒为 `false`，用于区分“未配置”和“故障”，前端与监控不得把 fake 行为当作可用功能。

## RTC 凭据（`GetRtcToken`）

`GET /api/v1/live/rtc-token?session_id=...` 返回
`{ mode, provider, token, expires_at, room_id, user_id, ok }`（`expires_at` 为 epoch ms）。

| `rtc.mode` | 行为 |
|---|---|
| `disabled`（默认） | HTTP 200 + `mode=unconfigured, ok=false`；前端显示“RTC 未配置”，**不是错误**。 |
| `dev` | 本地 HMAC 签发（`DH_RTC_API_KEY` / `DH_RTC_API_SECRET` 走环境变量，不入 yaml）；每次签发轮换 nonce，前端按 `expires_at` 提前刷新。 |
| `managed` | 走厂商签发器；未装配签发器时仍为 `unconfigured`（fail-closed）。 |

前端刷新失败时**保留旧凭据并显示降级提示**，不静默清空播放器。

## 启动前端

```powershell
cd web
npm install
npm run dev      # http://localhost:9000，/api 与 /ws 代理到 127.0.0.1:8795
npm run build    # 产物在 web/dist/spa
```

## 生成代码

```powershell
make proto   # api/**/*.proto -> api/gen/go
make ent     # internal/data/ent/schema -> internal/data/ent
make wire    # cmd/dh/wire.go -> cmd/dh/wire_gen.go
```

## 校验

```powershell
make ci                     # vet + test + 前端 build
powershell -File scripts/ci.ps1   # Windows 等价脚本
```

## 重要约定

- 所有写接口（设置、开播/收播、弹幕、WS ticket）都由鉴权 filter 保护；`/healthz` 公开，
  `/metrics` 仅回环。
- WS 使用一次性 ticket，经 `Sec-WebSocket-Protocol: dh.v1, ticket.<t>` 传递，不放 URL query。
- 重复开播会先 `stop(reason=reconnect)` 再重开，不返回 409。
- **事件契约单一事实来源**：`web/src/services/api/events.contract.json`；Go `pkg/eventcontract` 与前端
  `contract.ts` 各有测试读取该文件校验，禁止单边改字段。
- **事件信封**：`seq, event_id, session_id, type, ts_ms, epoch, request_id, payload`。
  - `seq` **按场次单调递增**（唯一约束 `(session_id, seq)`）；换场次从 1 重新计数，不是全局自增序号。
  - `epoch` 是喂流/歌曲代际，切歌或重连时 `+1`；前端见 `epoch` 变化即清空旧喂流相关 UI 状态。
  - 断线补齐用 `after_seq`（`seq > after_seq`），去重用 `event_id`。
- **REST 与 WS 同源**：`GET /api/v1/live/events` 与 WS 广播共用同一个 sink（`eventbus.Bus.SetAppender`）。
  业务事件（song/refs/action 等）也经 `PublishScoped` 统一填 `session_id/epoch` 后进 sink，
  不存在“WS 有、REST 列表没有”的分叉。
- **降级期事件（最小 outbox）**：`SwitchableSink` 启动时写内存；PG 接上后切换并**一次性补写**内存快照
  （按 seq 顺序，`(session_id,seq)` / `event_id` 唯一约束冲突视为已写）。补写失败即停止并保留内存数据，
  重启后以 DB 为准。内存实现默认上限 4096 条，超出部分无法补写，这是降级期已知的丢失边界
  （详见 `internal/data/repo/switchable.go` 类型注释）。
- **前端面板覆盖**：弹幕面板已接入 `GET/POST /api/v1/danmaku/*` 5 条 RPC；口播「合并选中 / 拆分 /
  全部动作」已接 `POST /api/v1/koubo/merge`、`/seg/split`、`/actions_all`。面板路由全集清单
  `web/src/services/api/panel-routes.json`（85 条）由 Go 与前端双侧测试校验；另有 1 条独立注册的 preflight 路由，不计入面板清单。
- Vidu Key 初始值可由环境变量注入；多 Key 管理落盘使用 Windows DPAPI / 非 Windows AES-256-GCM + `DH_SECRET_KEY` 加密，API 只回显尾 8 位。
- **崩溃恢复（`Manager.Recover`）**：判据是「归属」而非时间窗。启动时（`Server.Start` 前）与 DB 晚就绪时
  各执行一次：凡是 `starting/live/stopping` 且**不属于当前进程在场 session** 的场次一律标记为
  `error(crash_recovered)` 并发布 `live.crash_recovered` 事件——哪怕它几秒前还在更新（那是死进程崩溃前
  的最后一次写入，不标记就会变成永久幽灵 live）。曾用的 60s 陈旧窗口已废弃（会把刚崩溃的场次漏掉）。
  Recover 绝不自动恢复喂流或会话。
- `.gitignore` 只忽略仓库根 `/storage/`、`/var/`，不会误伤源码目录。

## 开发文档（docs/）

- `docs/architecture.md` — 分层、Wire 装配、事件总线（REST/WS 同源）、单场次语义、崩溃恢复、引擎看门狗/喂流权/Feeder
- `docs/api-contracts.md` — proto 路由、85 条面板路由 + 1 条独立 preflight 路由、WS ticket 事件流、鉴权模型（local/lan/collector）、事件契约
- `docs/data-model.md` — Ent 全表清单、Switchable 降级切换与 outbox 补写、幂等表、迁移策略、Redis/内存差异
- `docs/ops.md` — 三模式（real/dev、rtc disabled/dev/managed）、环境变量、降级语义、/healthz 与 /metrics、日志与审计
- `docs/verification.md` — D 批验证结论（故障注入、feeder 长跑、E2E、已修缺陷）

## 目录

```
api/                 proto 定义与生成代码
cmd/dh/              唯一进程入口（Wire 装配）
internal/biz/        领域逻辑（live / danmaku / settings）
internal/data/ent/   Ent schema 与生成代码
internal/server/     Kratos HTTP + WS + 鉴权中间件
internal/service/    proto 服务适配层
pkg/                 可复用基础包（eventbus / logx / secrets）
web/                 Quasar 控制台（NovaTrader tokens + Nt* 原子）
configs/             配置样例
```

## 未验证项（M0）

- PostgreSQL / Redis：本机未部署。PG 侧（场次/事件/审计/设置/账本/弹幕/幂等键）已落 Ent 实现并随
  `onDBReady` 自动切换；Redis 侧（WS ticket、HMAC nonce、开播/收播幂等）在启动时按可达性选 Redis 版，
  否则用进程内实现。**内存版差异：ticket/nonce/幂等结果重启即丢、多实例不共享**（弹幕内存降级只留
  最近 512 条，不提供复盘查询）。PG/Redis 契约测试用 `DH_TEST_PG_DSN` / `DH_TEST_REDIS_ADDR`
  环境变量门控，本机默认 Skip，待真机执行。
- Vidu：无凭据，未建立真实 WS 会话；心跳参数 `ping_interval=10/ping_timeout=20` 仅按蓝本事实写入设计，
  代码将在 M1 落地并实测。
- OmniVoice：本机未安装，Gradio 契约与超时语义待 M2 实测。
- ffmpeg：本机未安装，音频解码/重采样未验证。

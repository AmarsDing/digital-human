# 部署与运维

配置唯一入口 `configs/config.yaml` + 环境变量（`internal/conf/config.go`）。敏感值
（Vidu Key / RTC secret / 采集器 secret / admin token hash）只走环境变量或专用字段，**不写进 yaml**。
使用者视角的凭据配置步骤见 `docs/使用手册*.md`，本文面向开发与运维。

## 1. 引擎模式（fail-closed）

`engine.mode` 只有两档（默认 `real`）：

| 模式 | 行为 |
|---|---|
| `real`（默认） | 必须同时具备 `DH_VIDU_KEY`、`DH_OMNIVOICE_ADDR` 且真实适配器已装配；任一缺失时 `StartLive` 返回 **503**（`error_code=engine_not_configured`），**不会退回 fake 成功** |
| `dev` | 显式装配 FakeController，仅供本机联调；启动日志打 `WARN using fake live controller` |

`/healthz` 的 `capabilities.vidu/omnivoice/ffmpeg` 在未接入真实能力时恒为 `false`，用于区分「未配置」
和「故障」；前端与监控不得把 fake 行为当作可用功能。

**ffmpeg**：不在 PATH 或未安装时明确不可用（`capabilities.ffmpeg=false`），音频解码/重采样能力不开放；
不要以静默降级方式绕过。

## 2. RTC 凭据三模式

`rtc.mode` = `disabled`（默认）| `dev` | `managed`，语义与 fail-closed 行为见
docs/api-contracts.md §4。`disabled` 下接口返回 HTTP 200 + `mode=unconfigured, ok=false`，属预期。

## 3. 环境变量清单

| 变量 | 作用 | 默认/说明 |
|---|---|---|
| `DH_ENGINE_MODE` | real \| dev | 默认 real |
| `DH_VIDU_KEY` | Vidu API Key | 环境变量明文注入，仅用于引擎配置；面板托管 Key 已用 DPAPI/AES-GCM 密文落库，API 只回显尾 8 位 |
| `DH_OMNIVOICE_ADDR` | OmniVoice 边车地址 | real 模式必需 |
| `DH_RECORDING_ENABLED` | F34 云录制开关 | 1/true/yes 为开 |
| `DH_VIDU_BASE_URL` / `DH_VIDU_WS_URL` | 覆盖 Vidu 端点 | 仅联调/验收用，默认 `https://api.vidu.cn` 与其 wss |
| `DH_RTC_MODE` / `DH_RTC_PROVIDER` | disabled\|dev\|managed / artc\|trtc | 默认 disabled / artc |
| `DH_RTC_API_KEY` / `DH_RTC_API_SECRET` | dev 模式本地 HMAC 签发凭据 | 不入 yaml |
| `DH_RTC_ROOM_PREFIX` | 房间号前缀 | 默认 `dh-` |
| `DH_ADMIN_TOKEN_SHA256` | lan/collector 模式 Bearer token 的 SHA256（64 位 hex） | 自动转小写 |
| `DH_COLLECTOR_SECRET` | collector HMAC 密钥 | collector 模式必需 |
| `DH_STREAM_KEY` / `DH_RTMP_URL` | DZ 批抖音推流密钥与地址（`publish.EnvTargetStore` 读取） | 密钥不落 yaml；面板保存走 DPAPI 加密文件 |
| `DH_TEST_PG_DSN` / `DH_TEST_REDIS_ADDR` | PG/Redis 契约测试门控 | 未设置则对应测试 Skip |
| `DH_SOAK_MINUTES` | 长跑分钟数（feeder 真实时钟、WS 事件总线） | 未设置或 ≤0 跳过 |
| `DH_SOAK_ACCEL` | =1 启用 feeder 加速时钟长跑 | 未设置跳过 |

## 4. 降级语义（PG / Redis 缺失）

- `data.required=false`（默认）：PG/Redis 任一缺失即降级运行——事件/审计/场次/账本用内存实现，
  ticket/nonce/幂等用进程内实现；**不假装已连上**。
- `/healthz`（与 `/api/health` 同语义）三态判定（2026-11-04 起）：postgres 与 redis 都可达 → 200 +
  `status=ok`；任一缺失默认仍返回 **200**（进程可服务即存活），以 `status=degraded` +
  `missing` 名单显式标注（配置留空视为未就绪）。`health.require_dependencies=true` 恢复旧行为：
  缺依赖返回 **503 + degraded=true**。降级是支持的运行模式，不是失败。
- `data.required=true`：PG/Redis 未就绪则启动直接失败退出。
- 探测结果缓存 5s（`probeInterval`），避免公共端点打穿依赖。
- PG/Redis 在进程启动后就绪：`Probe` 自动重连，PG 首次可用触发 `onDBReady` 回调切换 EventSink
  （含降级期事件一次性补写，见 docs/data-model.md §2），`/healthz` 转 `status=ok`，无需重启。
- 内存版边界：ticket/nonce/幂等结果重启即丢、多实例不共享；事件内存上限 4096 条；弹幕内存降级只留
  最近 512 条。

## 5. 探活与指标

- `GET /healthz`、`GET /api/health`：公开；返回依赖可达性、degraded 标记、capabilities、WS 连接计数。
- `GET /metrics`：Prometheus 导出，**仅回环**（`server.metrics_loopback` 默认 true；非回环来源 403）。

## 6. 日志与审计

- 结构化日志：`log.level`（默认 info）/ `log.format`（json \| text，默认 json），输出到进程标准输出；
  未配置日志文件落盘，落盘归集由部署侧负责。
- 每个请求分配 `request_id`（响应头 `X-Request-Id`），注入 logger 上下文（`pkg/logx`），
  全链路可按 request_id 关联。
- 审计 filter 位于鉴权**之前**：被拒的写请求也留痕；写操作落 `AuditLog` 表（actor/route/action/target/
  ip/request_id/ts_ms，保留 180 天口径）；PG 不可用时走内存实现。
- 引擎看门狗/重连/stall/终结均打 Warn/Error 级日志（`vidu live terminated locally`、
  `feed stall detected, reconnecting` 等），排障先按 live_id/session_id 过滤。

## 7. 构建与校验

```powershell
go build -o bin\dh.exe ./cmd/dh
.\bin\dh.exe -conf configs\config.yaml   # 默认 127.0.0.1:8795，security.mode=local
make proto / make ent / make wire        # 生成代码（禁止手写）
make ci                                  # vet + test + 前端 build
```

前端：`cd web; npm install; npm run dev`（:9000，/api 与 /ws 代理到 8795）；产物 `web/dist/spa`。

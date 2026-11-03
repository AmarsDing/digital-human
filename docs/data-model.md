# 数据模型与迁移

存储事实以 `internal/data/ent/schema/` 为准（`make ent` 生成代码，禁止手写）。
`internal/data` 唯一持有 `ent.Client` / `redis.Client`；上层不得 import ent。

## 1. Ent 表清单（13 张）

### live.go

**LiveSession**（一场直播一行）
`id`(PK,immutable) · `live_id`(唯一索引,可空) · `phase`(默认 idle，索引) · `mode`(默认 managed) ·
`persona_id`/`avatar_id` · `started_at_ms`/`ended_at_ms`/`duration_s` · `config_snapshot`(JSON，只存非敏感字段) ·
`stats`(JSON) · `recording_state`(默认 none) · `recording_url` · `error_code` · `updated_at_ms`

**SessionEvent**（断线补齐与复盘的事件源）
`id`(PK) · `session_id` · `seq` · `event_id`(全局唯一) · `type` · `ts_ms` · `epoch` · `request_id` · `payload`(JSON)。
索引：`(session_id, seq)` 唯一、`session_id`。seq 按场次单调，换场次从 1 重计。

**Recording**（录播拉取状态）
`id`(PK) · `session_id`(唯一) · `state`(none/recording/processing/ready/failed/unavailable) ·
`url` · `fetched_at_ms` · `expires_at_ms`

**CostLedger**（复盘成本口径；credits/tokens 为真实值，其余标估算/不提供）
`id`(PK) · `session_id`(唯一) · `account_id`(默认 local) · `credits_start/end/spent` ·
`tokens_in/out/total` · `llm_calls` · `diamonds` · `gift_count` · `estimated_cost`(float) ·
`cost_source`(默认 unset) · `source`(默认 manual) · `started_at_ms`/`ended_at_ms`/`updated_at_ms`

### content.go

**Persona**（对齐蓝本 personas 目录；切换后需重新开播）
`id`(PK) · `slug`(唯一) · `name` · `tagline` · `voice_id` · `avatar_note` · `system_prompt` ·
`is_active`(默认 false) · `updated_at_ms`

**PersonaRef**（人设-参考图绑定）
`id`(PK，persona_id + "/" + asset_id) · `persona_id` · `asset_id` · `ord`(默认 0)。
索引：`(persona_id, asset_id)` 唯一、`(persona_id, ord)`。

**Asset**（图片/音频/头像/参考图统一元数据）
`id`(PK) · `kind`(image|audio|avatar|ref) · `name` · `path` · `sha256`(唯一) · `bytes` ·
`duration_ms` · `uploaded_at_ms` · `expires_at_ms`（90 天）· `ref_count`

**DanmakuEvent**（弹幕风控与复盘，30 天保留）
`id`(PK) · `session_id` · `kind`(chat|gift|join) · `user_id`/`user_name` · `text` · `diamond` ·
`source` · `decision`(t0|t2|confirm|blocked|dropped) · `created_at_ms`；索引 `(session_id, created_at_ms)`

**Setting**（13 组设置中的共享项，不落本机偏好）
`id`(PK，group id 或 group.key) · `value_json` · `updated_at_ms` · `updated_by`

**Patron**（舰长/ patrons 名录）
`id`(PK，nick 归一 trim) · `nick`(唯一) · `uid` · `alias` · `note` · `since`(YYYY-MM-DD) ·
`created_at_ms`。索引：`nick` 唯一、`uid`。

### ops.go

**ViduKey**（只存密文与尾 8 位，完整 key 永不落库/出日志）
`id`(PK) · `cipher`(bytes) · `tail8` · `is_active`(索引) · `added_at_ms` · `last_ok_at_ms` · `last_error`

**AuditLog**（所有写操作审计，180 天保留）
`id`(PK) · `actor`(默认 local) · `route` · `action` · `target` · `ip` · `request_id` · `ts_ms`(索引)

**IdemKey**（开播/收播幂等键）
`id` 即幂等 key（含 `start:`/`stop:` 前缀），唯一约束保证并发同 key 首写胜出（等价 Redis SET NX）；
`result`(JSON) · `expires_at_ms`（到期由读取方删除并允许重建）· `created_at_ms`。默认窗口 24h。

## 2. 降级与切换（Switchable）

- **SwitchableSink**（`internal/data/repo/switchable.go`）：启动时写内存实现；PG 运行期接上后
  （`Data.SetOnDBReady` → `SetEnt`）原子切换为 Ent，并把降级期内存快照**一次性补写**（最小 outbox）：
  按 seq 顺序逐条 Append，`(session_id,seq)` / `event_id` 唯一约束冲突视为已写、继续下一条；
  其他错误即停止补写并保留内存数据，重启后以 DB 为准。
- 内存实现默认上限 **4096 条**，超出部分无法补写——这是降级期已知的丢失边界。
- 切换后新事件只写 Ent；内存数据只用于补写，补写后不主动删（双读只写不删：读路径始终走当前 sink，
  不做跨源合并）。
- **SwitchableCostStore**（`costledger.go`）同型：内存 → Ent 切换 + 补写。
- **SwitchableAvatarStore**（`avatar.go`）同型：形象档案内存 → Ent 切换；persona store 经
  `NewAvatarBackedStore` 委托该存储（见 §4）。另有 SwitchablePatronStore（`patrons.go`）、
  SwitchableSettingStore（`settings.go`）、SwitchableViduKeyStore（`vidukeys.go`）等同型包装。
- PG/Redis 缺失时的整体 degraded 语义见 docs/ops.md。

## 3. 迁移策略

- 迁移只在 Ent 模式执行：`ent.Client.Schema.Create` 在拿到 PG 连接时跑——启动时一次，
  PG 晚就绪自动重连成功时再跑一次（`data.go connectPostgres`）。内存降级模式不建表。
- 迁移失败：PG 视为不可用，进程按 degraded 继续（`data.required=true` 时直接退出）。
- 事件保留期由 `data.event_retention_days`（默认 90）控制；弹幕 30 天、审计 180 天为 schema 注释口径，
  清理作业的具体执行节奏待真机验证。

## 4. Redis 版与进程内版差异

| 组件 | Redis 版 | 内存版 |
|---|---|---|
| WS ticket（`TicketStore`） | 多实例共享、重启不丢 | 重启即丢、多实例不共享；后台每分钟 GC 过期票 |
| HMAC nonce（`NonceStore`） | 多实例共享去重 | 进程内窗口 10min，重启即清空 |
| 开播/收播幂等（`live.Store`） | 多实例共享、TTL 24h | 进程内 map，重启即丢 |
| 弹幕 | 落 Ent（PG 可用时） | 内存降级只留最近 512 条，不提供复盘查询 |

启动时按 Redis 可达性自动选择；契约测试用 `DH_TEST_PG_DSN` / `DH_TEST_REDIS_ADDR` 环境变量门控，
本机默认 Skip。

人设存储（`persona.Store`）不在上表：`persona.NewAvatarBackedStore` 是兼容壳，把 persona 的
CRUD 委托给形象档案存储（`avatar.AvatarStore`，v3 §1.2 拍板 avatar 档案服务为人设 prompt 单源），
经 `SwitchableAvatarStore` 内存 → Ent 切换（wire 装配见 `cmd/dh/wire_gen.go`）。

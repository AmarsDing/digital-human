# D 批验证结论

本文件记录 D 批（验证批）实测结论。数值为真实实测，禁止回填估计值；未完成项明确标注。

## 1. 故障注入（4 条，各 `-race` ×10 稳定）

| 场景 | 测试 | 结论 |
|---|---|---|
| WS 断连补写 | `TestWSReconnectAfterSeqBackfillsMissedEvents`（`internal/server/ws_reconnect_test.go`） | 断线后按 `after_seq` 补发遗漏事件，无丢失无重复 |
| 引擎崩溃 Recover 接管 | `TestRecoverAfterCrashNewManagerTakesOver`（`internal/biz/live/recover_takeover_test.go`） | 新 Manager 把旧进程遗留场次标记 `crash_recovered` 并发事件，不自动恢复 |
| 边车超时熔断 | `TestSidecarHangTimeoutTripsBreaker`（`internal/engine/tts/breaker_timeout_test.go`） | OmniVoice 边车挂起时超时生效并触发熔断，不阻塞主流程 |
| PG 掉线降级恢复 | `TestSwitchableSinkPGOutageDegradeAndRecovery` + `TestSwitchableCostStoreDegradeThenSwitch`（`internal/data/repo/switchable_outbox_test.go`） | PG 掉线切内存、恢复后切回 Ent 并一次性补写降级期事件（最小 outbox） |

## 2. Feeder 漂移长跑

### 2.1 真实时钟 1h（`TestSoakFeederDriftRealClock`，`internal/engine/feeder_soak_test.go`）

- 实测时长 3624s；100ms×4800B 分片连续喂流。
- **120 轮 / 36000 帧 / 172800000 字节**；迟到 0 / 告警 0；单轮最大滞后 0s；
  平均轮末漂移 **474.17µs**（断言阈值 |Drift| < 250ms，余量充足）。

### 2.2 加速 8h 等效（`TestSoakFeederDriftAccelerated`）

- `DH_SOAK_ACCEL=1`，ChunkMS=1 压缩 100x。
- **288000 tick / 960 轮 / 墙钟 4m48.5s**；单轮最大滞后 **3.7149ms**。
- 证明节拍计数与漂移校正在大基数下不发散；8h 墙钟下的 OS 定时器行为仍由
  `DH_SOAK_MINUTES=28800` 的真机长跑负责（待执行）。

运行方式：

```powershell
$env:DH_SOAK_MINUTES=60; go test ./internal/engine/ -run TestSoakFeederDriftRealClock -count=1 -v -timeout 90m
$env:DH_SOAK_ACCEL=1;   go test ./internal/engine/ -run TestSoakFeederDriftAccelerated -count=1 -v -timeout 20m
```

## 3. E2E（Playwright 真实浏览器，dev 引擎）

**2026-11-04 实测通过**：在 `web/` 执行 `$env:DH_E2E='1'; npm run e2e`，退出码 **0**；Playwright **2 passed（4.0s）**，编排总耗时 **8414ms**。测试脚本、配置与 fixture 已外置到 `f:\myproject\teamwork\test\digital-human\e2e\`，用法与 CI 门控见 `../../test/digital-human/e2e/README.md`。

- 全路径用例：`F9` 的真实请求顺序为 preflight → Start；随后发言、字幕上屏、动作触发、WAV 点播。`F10` 只打开「下播确认」；重复 `F10` 受弹层守卫，`Esc` 与取消均保持 `live` 并回焦，显式激活「确认下播」后才进入 `ended`。
- 开播按钮用例：真实网络顺序为 `/api/v1/live/preflight` → `/api/v1/live/start`，Start 的 `mode/draftId/expectedRevision/preflightToken` 与预检绑定一致。
- REST 事件实测恰为 `live.start_requested → live.started → transcript.partial → transcript.final → action.sent → song.playing → song.ended → live.stopped`，`seq = 1..8` 连续无跳号。

证据：运行目录 `f:\myproject\teamwork\temp\digital-human\runtime\e2e-2026-11-03-063806\`（`run.json`、`result.json`、`backend.log`、Playwright 输出）；截图 `f:\myproject\teamwork\temp\digital-human\screenshots\e2e-2026-11-03.png`。

## 4. 本批修复的两个真实缺陷

1. **WS 握手 500（真机）**：`auditFilter` 的 `statusRecorder` 未透传 `http.Hijacker`，
   gorilla Upgrade 取不到 Hijacker 直接 500。修复：recorder 实现 Hijack 透传；
   回归测试 `TestWSHandshakeThroughProductionFilters`（`internal/server/ws_long_test.go`）
   走完整生产 filter 链做握手。
2. **`TestContextCancelPropagatesToSidecar` 偶发失败**（`internal/engine/tts/synth_test.go`）：
   根因是测试在边车尚未入队（statusQ 为空）时就 cancel，ctx 已结束的任务根本不会打到边车，
   断言路径随机落空。修法：等 `statusQ > 0` 再 cancel，使断言覆盖真实传播路径。

## 5. WS 1h 长跑

`TestSoakWSEventBus`（`internal/server/soak_test.go`，`DH_SOAK_MINUTES=60` 门控）实测
3630s（2026-11-03）：

- 60 分钟内每 30s 发一条业务事件：**收到 242 条，lastSeq=242，seq 连续无洞无重复**；
- **断开次数 = 0**；并行 100ms ticker 抖动测量：36001 个样本，**最大抖动 2.0066ms**。
- 首次执行曾被 go test 默认 10m 超时截断；必须带 `-timeout 90m`（测试头注释已写明）。

```powershell
$env:DH_SOAK_MINUTES=60; go test ./internal/server/ -run TestSoakWSEventBus -count=1 -v -timeout 90m
```

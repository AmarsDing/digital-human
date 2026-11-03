# 项目外统一产物目录 · 迁移报告（2026-11-03）

## 1. 最终目录树

```
f:\myproject\teamwork\
├─ AGENTS.md                       # 新增：产物与生成物规则
├─ test\
│  ├─ aranea-agents\  legacy\ reports\ scenarios\
│  ├─ HotCraftFlow\                # 空（项目内测试均为 git 跟踪源码）
│  ├─ cyber-xiaoya-live-main\      # 空
│  ├─ NovaTrader\                  # 空
│  └─ digital-human\               # 空（待迁入）
├─ deploy\
│  ├─ aranea-agents\               # 空；部署文件均为 git 跟踪，按约束留在仓库
│  ├─ HotCraftFlow\                # 空；infra/、scripts/ 为跟踪源码
│  ├─ cyber-xiaoya-live-main\      # 空；启动脚本无 git 归属，按源码保留
│  ├─ NovaTrader\                  # 空；backend/scripts 为跟踪源码
│  └─ digital-human\               # 空（待迁入）
└─ temp\
   ├─ aranea-agents\  build\ cache\ competition\ design\ generated\ junk\ logs\ media\ plan\ review\ runtime\ screenshots\
   ├─ NovaTrader\     build\ cache\ design\ logs\ research\ review\ runtime\ tmp\ ...
   ├─ HotCraftFlow\   build\ review\ ...
   ├─ cyber-xiaoya-live-main\ plan\ review\ ...
   └─ digital-human\  review\ plan\ logs\ ...（全部为空，待迁入）
```

磁盘占用：`temp\aranea-agents` 6.68 GB、`test\aranea-agents` 9.94 GB、`temp\NovaTrader` 4.84 GB、`temp\HotCraftFlow` 0.04 GB。

## 2. 逐项目「移出 / 保留」表

### aranea-agents（git 跟踪且迁移前工作区干净）

| 移出 | 去向 |
|---|---|
| `bin/`（687 MB）、`admin.exe`、`memoryeval.exe` | `temp\aranea-agents\build\bin\`（重名保留 `-orig`） |
| `build\deps`、`build\release`、`build\staging` | `temp\aranea-agents\build\` |
| `blender/`（2.70 GB 渲染产物） | `temp\aranea-agents\build\blender\` |
| `web\dist`、`web\build`（electron/tauri）、`web\.quasar` | `temp\aranea-agents\build\|cache\` |
| `web\*.log`、`logs-dev.log`、`*-out.txt`、`vitest-result.json` | `temp\aranea-agents\logs\` |
| `docker\bin`、`docker\volumes`、`docker\.test-*` | `temp\aranea-agents\runtime\` |
| `cmd\admin\data`、`cmd\admin\logs` | `temp\aranea-agents\runtime\` |
| `competition/`（336 MB） | `temp\aranea-agents\competition\` |
| `workspace/`、`internal\agent\workspace\` | `temp\aranea-agents\runtime\` |
| `openapi.yaml`（生成物，未跟踪） | `temp\aranea-agents\generated\` |
| `internal\computeruse\**\{bin,obj,artifacts,TestResults}` | `temp\aranea-agents\build\computeruse\` |
| `docs\reports`、`docs\bugs` | `temp\aranea-agents\review\` |
| `docs\development`、`docs\guides`、`docs\superpowers`、`docs\README.md` | `temp\aranea-agents\plan\` |
| `docs\testing`（182 MB） | `test\aranea-agents\reports\` |
| `docs\scenarios` | `test\aranea-agents\scenarios\` |
| `docs\image`、`docs\promo-videos`、`docs\showcase` | `temp\aranea-agents\media\` |
| `test\` 下全部被忽略文件（67,595 个） | `test\aranea-agents\legacy\`（按原相对路径） |
| 编辑器残留 `*.swp` | `temp\aranea-agents\junk\` |

| 保留 | 原因 |
|---|---|
| `docs\` 下 25 个 **git 跟踪** 文档 | 被跟踪内容不迁移；迁移中一度删除，已 `git checkout -- docs` 原样恢复 |
| `test\agent-memory-challenge\*.ps1/.json/.jsonl`、`test\cua-win-smoke\smoke.ps1` | git 跟踪的测试脚本/结果 |
| `node_modules\`、`web\node_modules\` | 依赖，非中间产物 |
| `configs\config.local.yaml` | 本机密钥配置，运行必需 |
| `.claude\`、`.trae\`、`.vscode\`、`pkg\trpc-agent-go\` | 工具链/第三方源码 |
| `docker-compose*.yml`、`Dockerfile*`、`build\*.ps1` | git 跟踪的部署资产，按约束留在仓库 |

### NovaTrader（git 跟踪；`backend` 属于同一仓库）

| 移出 | 去向 |
|---|---|
| `backend\runtime\acceptance-2026-10-19`、`acceptance-2026-10-22` | `temp\NovaTrader\runtime\` |
| `backend\runtime\bin`、`logs`、`local`、`nats-execution-14223`、`*.exe`、`*.log` | `temp\NovaTrader\runtime\` |
| `backend\build`、`dist`、`cmd.exe`、`server*.log`、`app\**\cmd.exe` | `temp\NovaTrader\build\` |
| `.tmp/`（70 MB，5535 文件） | `temp\NovaTrader\tmp\root-tmp\` |
| `web\dist`、`web\.tmp`、`bundle-stats.html`、`graphics\test-results` | `temp\NovaTrader\build\tmp\` |
| `web\src-tauri\target`（1.27 GB）、`gen` | `temp\NovaTrader\build\` |
| `doc\UI/`（55 MB 设计研究档案） | `temp\NovaTrader\design\UI\` |
| `research-worker\runs` | `temp\NovaTrader\research\runs\` |
| `**\__pycache__`、`**\.pytest_cache`（不含 `.venv`） | `temp\NovaTrader\cache\` |

| 保留 | 原因 |
|---|---|
| **`doc\开发文档\` 全部内容（含 `assets\aether-v2-2026-10-30\`、`assets\stellar-glass-2026-10-30\`、主题/规范文档）** | 用户新增强制约束；另一会话正在合并设计指导，未做任何移动/修改 |
| `backend\runtime\acceptance-2026-10-23`（10 个 exe，约 899 MB） | `start-local.ps1` 的默认 `BinaryDir`，移走会直接破坏启动；作为活动运行目录保留 |
| `backend\runtime\discovery`、`nav-export-contract.json`、`portfolio-preview-contract.json` | git 跟踪契约/运行配置 |
| `pyworker\.venv`、`research-worker\**`（外部研究克隆） | 运行/研究依赖，非一次性产物 |
| `doc\视频\*.mp4`、`doc\开发文档` | 文档媒体与设计资产 |
| `backend\scripts\*.ps1`、`backend\app\**`、`web\src` | git 跟踪源码 |
| 迁移前已存在的 `doc\开发文档` 删除/修改状态 | 属用户在建工作，未恢复、未覆盖 |

### HotCraftFlow

| 移出 | 去向 |
|---|---|
| `apps\server\cmd.exe`（87 MB） | `temp\HotCraftFlow\build\apps-server-cmd.exe` |
| 根目录 `HotCraftFlow-profile.md` | `temp\HotCraftFlow\review\` |

| 保留 | 原因 |
|---|---|
| `apps\server`、`apps\web`、`infra\`、`scripts\`、`patches\`、`docs\`、`doc\`、`HotCraftFlow-seed.zip`、`_patch_ent*.{js,py}` | 全部 git 跟踪，无未跟踪中间产物 |

### cyber-xiaoya-live-main（无 `.git`）

| 移出 | 去向 |
|---|---|
| 根目录 `cyber-xiaoya-live-main-aranea-plan.md` | `temp\cyber-xiaoya-live-main\plan\` |
| 根目录 `cyber-xiaoya-live-main-aranea-plan-review.md` | `temp\cyber-xiaoya-live-main\review\` |

| 保留 | 原因 |
|---|---|
| `src/`、`tools/`、`stage/`、`assets/`、`data/`、`personas/`、`refs/`、`audio/`、启动脚本 | 无版本库可判定跟踪状态，全部按源码/运行资产保留；未发现 logs、`.venv`、`__pycache__` |

### digital-human

- 未移动任何物理文件（另一会话开发中）。
- 已建立 `test\digital-human`、`deploy\digital-human`、`temp\digital-human\{review,plan,logs,...}`。
- 根目录 4 份报告（`digital-human-go-plan.md`、`-review.md`、`review-backend.md`、`review-frontend.md`）同样保持原位，待开发批次结束后迁入。

## 3. 脚本路径修复清单

**本轮无需修改脚本内路径。** 依据：

- 移出的 `dist/bin/build/runtime` 均由原命令在原路径重新生成；未把任何“唯一可执行文件”移走。
- NovaTrader `start-local.ps1` 默认 `runtime/acceptance-2026-10-23` 有意保留，路径仍有效；`runtime/local` 下的 `.env.<service>.yaml`、日志目录、进程清单由脚本启动时自动创建（已核对第 140–260 行逻辑）。
- aranea `Makefile` 仍输出到 `bin/`，`openapi.yaml` 由生成命令重建。
- HotCraftFlow 的 `cmd.exe`、cyber-xiaoya 的报告均无脚本引用。

未改脚本是刻意选择：避免为整理而改动已跟踪源码；若后续决定彻底外置 `bin/`，应改为 `OUTPUT_DIR` 参数，而不是硬编码新绝对路径。

## 4. 验证结果（实际执行）

| 项目 | 命令 | 结果 |
|---|---|---|
| aranea-agents | `go build ./...` | `EXIT=0`，无输出 |
| aranea-agents | `npm run check:types`（web） | `EXIT=0`（仅 npm 配置告警） |
| NovaTrader | `go build ./...`（backend） | `EXIT=0`，无输出 |
| NovaTrader | PowerShell AST 解析 `start-local.ps1` | `parse_errors=0`；`runtime\acceptance-2026-10-23` 存在，10 个 exe |
| cyber-xiaoya-live-main | `py -3` 内存编译 `src/**/*.py` + `tools/**/*.py` | `checked 39 failed 0`（Python 3.14.6，不落 `__pycache__`） |
| HotCraftFlow | `go build ./...` | `EXIT=1`：`framework/pkg/storage` 缺失；迁移前 `git status` 已干净且该路径未被跟踪，判定为**既有缺陷**，与迁移无关 |
| HotCraftFlow / NovaTrader 前端 | 未执行 | 两项目 `web/node_modules` 迁移前即不存在，无法 `npm run build` |

说明：aranea 前端只跑了类型检查，未跑 `quasar build`，避免又在项目内生成 `web/dist`；NovaTrader 未实际启动服务，避免启动 PG/Redis/NATS 依赖进程。

## 5. 手写生成物清单（仅报告，未重写）

| 项目 | 文件 | 现状 | 应改用 |
|---|---|---|---|
| HotCraftFlow | `_patch_ent.js`、`_patch_ent_probe.py` | 硬编码 `E:/project/...`，直接改写 `ent_client.go` 生成块 | `ent generate ./app/<mod>/internal/data/ent/schema`，禁止补丁生成物 |
| HotCraftFlow | `scripts/fix-ent-auth2.mjs`、`fix-ent-client.mjs` | 正则删改 `ent_client.go` 中的 ensure 逻辑 | 同上 |
| HotCraftFlow | `scripts/fix-auth-*.mjs`、`fix-seed-*.mjs`、`fix-server-worker.mjs`、`patch-*.mjs`、`rename-*.mjs`、`slim-*.mjs` | 直接改 `rest.go`、`seed_service.go`、`task_worker.go`、provider/index 等；多为一次性骨架补丁 | 从 proto 重新生成 REST/客户端；Wire 重新生成；业务差异写入 handler/service 源文件 |
| HotCraftFlow | `patches\radar_http_stub.go.snippet`、`seed_menu.snippet.go` | 手工代码片段 | 纳入正式源码或由生成器产出 |
| aranea-agents | `cmd/admin/wire_gen.go` 等 | 头部 `Code generated by Wire. DO NOT EDIT.`，仓库策略允许提交 | `go run github.com/google/wire/cmd/wire ./cmd/admin` |
| aranea-agents | 655 个 `*.pb.go`、`internal/data/ent/**` | 均有 `Code generated` 头 | `make proto` / `ent generate` |
| NovaTrader | `backend\app\**\wire_gen.go`（11 个）、`ent/**`、655 个 `*.pb.go` | 均有生成头 | `wire` / `ent generate` / proto 生成命令 |
| 三项目 | `openapi.yaml` | aranea 未跟踪生成物已外置；NovaTrader 为跟踪文件 | `make api` / `buf generate` |

未发现“无生成头却以生成物命名”的 pb/ent 文件；HotCraftFlow 的补丁脚本是唯一明确的手写生成物风险。

## 6. `.gitignore` 同步

已向 5 个项目 `.gitignore` 追加「项目外统一产物目录约定（2026-11-03）」段落，覆盖：

- 通用：`dist/`、`bin/`、`var/`、`storage/`、`logs/`、`*.log`、`*.tsbuildinfo`、`test-results/`、`__pycache__/`、`.pytest_cache/`
- aranea：`/runtime/`、`/output/`、`/internal/agent/workspace/`、`internal/computeruse/**/{bin,obj,artifacts,TestResults}`、`docker/volumes` 等
- NovaTrader：`backend/runtime/{bin,local,logs,acceptance-*}`、`web/src-tauri/{gen,target}`、`doc/UI/`、`research-worker/runs/` 等
- HotCraftFlow：`apps/*/dist/`、`playwright-report/` 等
- cyber-xiaoya：`config*.bak`、`lyrics-cache/`、`tmp/` 等
- digital-human：`*.log`、`test-results/`、`coverage/` 等

`git status` 最终变化：aranea `M .gitignore`、HotCraftFlow `M .gitignore`、NovaTrader `M .gitignore`（另有 40 条迁移前既有的 `doc/开发文档` 改动，未触碰）。

## 7. AGENTS.md 摘要

新文件 `f:\myproject\teamwork\AGENTS.md` 包含：

1. 三目录约定 `..\test\<project>`、`..\deploy\<project>`、`..\temp\<project>` 及 `temp` 固定子目录、日期后缀。
2. 落盘红线：中间产物不得进仓库；只移动不删除；冲突加 `-orig`；跟踪文件不迁移；相对路径随迁移修复；`.gitignore` 必须覆盖。
3. 禁止手写生成物表格（proto/pb、Ent、Wire、前端产物、lockfile、OpenAPI 客户端）及对应生成命令。
4. 提交前 `git status` 检查、`-orig` 登记、规则变更同步。
5. 新增占位章节 **「前端样式与组件」**：注明“由设计指导文件统一管理，禁止各项目自定义色值与组件风格”，条款留空待设计整理会话补入。

## 8. 仍未迁移 / 遗留

- **digital-human**：物理文件全部未动，仅建空目录；4 份根目录报告待开发批次结束后迁入。
- **NovaTrader `doc\开发文档`**：按新约束完全保留，包含正在合并的设计资产。
- **NovaTrader `backend\runtime\acceptance-2026-10-23`**：活动运行目录保留，属有意例外。
- **HotCraftFlow 构建缺陷**：`pkg/storage` 缺失（迁移前既有），需项目负责人补源码或调整引用。
- **HotCraftFlow / NovaTrader 前端依赖**：`node_modules` 缺失（迁移前既有），`npm run build` 无法执行。
- **aranea `docs` 下 25 个跟踪文档**：留在仓库；其余外置归档。
- **根目录 `.screenshots\.tmp`**：空目录，未处理。

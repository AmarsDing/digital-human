package avatar

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// sync-vidu 202 异步任务化（v3 §2.2，M3）。
//
// 形态：POST /sync-vidu 入队即返 202 {job_id, state:"queued"}；状态经既有
// ListAvatars/GetAvatar 的 vidu_sync_state 字段轮询闭环，不新增轮询路由。
// 状态机：queued → uploading → creating → done | error；参考图随 sync 一并同步
// （refs 上传并入 uploading 阶段，§3.1）。
//
// 幂等与并发（P1-4）：per-avatar 进程内 in-flight 锁——进行中二次调用 200 复放
// 当前 {job_id, state}；已完成且主形象 sha256 未变 200 复放 dedup:true；
// sha256 变了才允许新 job。绝不双扣费、绝不 Vidu 侧双资产。
//
// 补偿（§2.2）：事实基础——Vidu 积分只在开播（create_component）扣除，上传与
// create_avatar 均不扣积分，故不存在"失败但已扣费且无痕"场景。
// ① 上传成功（已得 ssupload uri）但 create 失败 → error(stage=creating)，重试从
//   create 续传不重复上传（uri 落库在 Asset.vidu_uri，90 天平台自过期，本地原件不受影响）；
// ② create 成功但回填写库失败 → error(stage=persist)，重试只做写库；
// ③ create 产物成为孤儿（崩溃于 create 成功与 persist 完成之间）→ 开机对账
//   delete_avatar best-effort 清理，失败仅记日志（平台 90 天兜底）。
//
// 进程内 SyncJob map 重启即丢；开机对账把非终态 vidu_sync_state 重置为
// error:interrupted（与崩溃恢复语义一致）。done/error 终态连同 asset_sha256 与
// 断点续传字段（Asset.vidu_uri、ViduSync.ViduAvatarID）持久化，重启后
// dedup 复放与续传仍然成立。

// 状态机字面值（写进 vidu_sync_state.state / .stage，前端轮询消费）。
const (
	SyncStateQueued    = "queued"
	SyncStateUploading = "uploading"
	SyncStateCreating  = "creating"
	SyncStateDone      = "done"
	SyncStateError     = "error"

	SyncStageUploading = "uploading"
	SyncStageCreating  = "creating"
	SyncStagePersist   = "persist"
	// SyncStageInterrupted 开机对账标记：进程重启时仍有非终态任务。
	SyncStageInterrupted = "interrupted"
)

// 同步管线哨兵错误：server 层映射 503 / 400。
var (
	// ErrViduKeyUnavailable 无 Vidu Key 或引擎非 real 模式（fail-closed，v2 §6.3）。
	ErrViduKeyUnavailable = errors.New("vidu_key_unconfigured")
	// ErrSyncImageRequired 档案未绑定主形象图，无可同步素材。
	ErrSyncImageRequired = errors.New("avatar_image_required")
)

// SyncUploader Vidu 能力窄接口（*vidu.Client 实现；测试注入 fake）。
// 字段形状待真机验证（无真实凭据，蓝本 client.py:130-152）。
type SyncUploader interface {
	Upload(ctx context.Context, content []byte, contentType string) (string, error)
	CreateAvatar(ctx context.Context, body map[string]any) (map[string]any, error)
	DeleteAvatar(ctx context.Context, avatarID string) error
}

// SyncJob 进程内任务（重启即丢；终态与续传点已持久化到 vidu_sync_state/Asset）。
// 字段变更一律持 SyncService.mu，禁止裸读裸写。
type SyncJob struct {
	ID          string `json:"job_id"`
	PersonaID   string `json:"persona_id"`
	AssetSHA256 string `json:"asset_sha256"`
	State       string `json:"state"`
	Stage       string `json:"stage,omitempty"`
	Error       string `json:"error,omitempty"`
	StartedAtMs int64  `json:"started_at_ms"`

	// 断点续传字段（§2.2 补偿①②）：ImageURI 上传成功即落库 Asset.vidu_uri；
	// ViduAvatarID create 成功即落库 vidu_sync_state.vidu_avatar_id。
	ImageURI     string `json:"-"`
	ViduAvatarID string `json:"-"`
}

// SyncService sync-vidu 异步管线：任务队列（进程内）+ 状态机推进 + 补偿。
type SyncService struct {
	store  AvatarStore
	images *ImageService
	vidu   SyncUploader // nil → fail-closed（ErrViduKeyUnavailable）
	logger *slog.Logger

	mu       sync.Mutex
	jobs     map[string]*SyncJob // job_id → job（含终态，重启即丢）
	inflight map[string]string   // persona_id → 进行中 job_id
	last     map[string]*SyncJob // persona_id → 最近终态 job（done 复放 / error 续传种子）
}

func NewSyncService(store AvatarStore, images *ImageService, vidu SyncUploader, logger *slog.Logger) *SyncService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SyncService{
		store:    store,
		images:   images,
		vidu:     vidu,
		logger:   logger,
		jobs:     map[string]*SyncJob{},
		inflight: map[string]string{},
		last:     map[string]*SyncJob{},
	}
}

// Start 发起/复放同步任务。返回值 created=true → 新入队（202）；否则为复放（200），
// dedup=true 表示"已完成且 sha256 未变"的幂等复放。
func (s *SyncService) Start(ctx context.Context, personaID string) (job SyncJob, created, dedup bool, err error) {
	if s.vidu == nil {
		return SyncJob{}, false, false, ErrViduKeyUnavailable
	}
	a, ok, err := s.store.Get(ctx, personaID)
	if err != nil {
		return SyncJob{}, false, false, err
	}
	if !ok {
		return SyncJob{}, false, false, ErrNotFound
	}
	if a.ImageAsset.ID == "" || a.ImageAsset.SHA256 == "" {
		return SyncJob{}, false, false, ErrSyncImageRequired
	}
	sha := a.ImageAsset.SHA256

	s.mu.Lock()
	defer s.mu.Unlock()
	// 进行中 → 复放当前任务（P1-4）。
	if jobID, ok := s.inflight[personaID]; ok {
		if j, ok := s.jobs[jobID]; ok {
			return *j, false, false, nil
		}
		delete(s.inflight, personaID) // 不一致自愈
	}
	// 进程内终态复放/续传。
	if j, ok := s.last[personaID]; ok && j.AssetSHA256 == sha {
		if j.State == SyncStateDone {
			return *j, false, true, nil
		}
		return *s.spawnLocked(a, sha, j), true, false, nil
	}
	// 跨重启复放/续传：终态与续传点已持久化（见包注释）。
	switch {
	case a.ViduSync.State == SyncStateDone && a.ViduSync.AssetSHA256 == sha && a.ViduSync.JobID != "":
		j := SyncJob{
			ID: a.ViduSync.JobID, PersonaID: personaID, AssetSHA256: sha,
			State: SyncStateDone, StartedAtMs: a.ViduSync.UpdatedAtMs,
		}
		s.jobs[j.ID] = &j
		s.last[personaID] = &j
		return j, false, true, nil
	case a.ViduSync.State == SyncStateError && a.ViduSync.AssetSHA256 == sha:
		seed := &SyncJob{ImageURI: a.ImageAsset.URI, ViduAvatarID: a.ViduSync.ViduAvatarID}
		return *s.spawnLocked(a, sha, seed), true, false, nil
	}
	return *s.spawnLocked(a, sha, nil), true, false, nil
}

// spawnLocked 新建并入队任务（持锁内调用）；seed 为断点续传种子（可为 nil）。
func (s *SyncService) spawnLocked(a Avatar, sha string, seed *SyncJob) *SyncJob {
	j := &SyncJob{
		ID: uuid.NewString(), PersonaID: a.ID, AssetSHA256: sha,
		State: SyncStateQueued, StartedAtMs: time.Now().UnixMilli(),
	}
	if seed != nil {
		j.ImageURI = seed.ImageURI
		j.ViduAvatarID = seed.ViduAvatarID
	}
	s.jobs[j.ID] = j
	s.inflight[a.ID] = j.ID
	delete(s.last, a.ID)
	go s.run(j)
	return j
}

// run 任务主循环。必须脱离请求 ctx：全局 timeoutFilter（15s）会杀死请求 ctx，
// 而上传+create 最坏 420s（v3 §2.1），故用 context.Background 独立生命周期（A19）。
func (s *SyncService) run(job *SyncJob) {
	ctx := context.Background()
	defer func() {
		if r := recover(); r != nil {
			s.finishError(ctx, job, "", fmt.Errorf("panic: %v", r))
		}
	}()

	a, ok, err := s.store.Get(ctx, job.PersonaID)
	if err != nil || !ok {
		s.finishError(ctx, job, "", fmt.Errorf("load avatar: %w", coalesceErr(err, ErrNotFound)))
		return
	}
	expires := time.Now().Add(90 * 24 * time.Hour).UnixMilli() // Vidu 资产 90 天有效期（v2 §6.2）

	// uploading 阶段：主形象 + 参考图（refs 随 sync 一并同步，§3.1）；
	// 续传点：Asset.vidu_uri 非空即跳过（补偿①：重试不重复上传）。
	s.transition(ctx, job, SyncStateUploading, "", nil)
	if job.ImageURI == "" {
		uri, err := s.uploadAsset(ctx, a.ImageAsset.ID, expires)
		if err != nil {
			s.finishError(ctx, job, SyncStageUploading, err)
			return
		}
		s.mu.Lock()
		job.ImageURI = uri
		s.mu.Unlock()
	}
	for _, ref := range a.Refs {
		if ref.Asset.URI != "" {
			continue
		}
		if _, err := s.uploadAsset(ctx, ref.AssetID, expires); err != nil {
			s.finishError(ctx, job, SyncStageUploading, fmt.Errorf("ref %s: %w", ref.AssetID, err))
			return
		}
	}

	// creating 阶段（补偿①：失败重试从这里续传）。
	s.transition(ctx, job, SyncStateCreating, "", nil)
	if job.ViduAvatarID == "" {
		// body 字段按蓝本组装（name/image_uri）；字段形状待真机验证。
		resp, err := s.vidu.CreateAvatar(ctx, map[string]any{
			"name":      a.Name,
			"image_uri": job.ImageURI,
		})
		if err != nil {
			s.finishError(ctx, job, SyncStageCreating, err)
			return
		}
		id := extractAvatarID(resp)
		if id == "" {
			s.finishError(ctx, job, SyncStageCreating,
				errors.New("create_avatar 响应缺少 avatar id（字段名待真机验证）"))
			return
		}
		s.mu.Lock()
		job.ViduAvatarID = id
		s.mu.Unlock()
		// create 成功即刻把平台资产 ID 落库：崩溃窗口内孤儿资产可被开机对账清理（补偿③）。
		s.transition(ctx, job, SyncStateCreating, SyncStageCreating, nil)
	}

	// persist 阶段（补偿②：create 已成功，重试只做写库）。
	if err := s.persistResult(ctx, job); err != nil {
		s.finishError(ctx, job, SyncStagePersist, err)
		return
	}

	s.mu.Lock()
	job.State = SyncStateDone
	job.Stage = ""
	job.Error = ""
	delete(s.inflight, job.PersonaID)
	s.last[job.PersonaID] = job
	s.mu.Unlock()
}

// uploadAsset 读本地素材 → Vidu 三步上传 → ssupload URI 与 90 天到期时刻落库。
func (s *SyncService) uploadAsset(ctx context.Context, assetID string, expires int64) (string, error) {
	buf, contentType, err := s.images.ReadAsset(ctx, assetID)
	if err != nil {
		return "", err
	}
	uri, err := s.vidu.Upload(ctx, buf, contentType)
	if err != nil {
		return "", err
	}
	w, ok := s.store.(AssetViduWriter)
	if !ok {
		return "", errors.New("avatar store 未实现 AssetViduWriter（fail-closed）")
	}
	if err := w.SetAssetViduURI(ctx, assetID, uri, expires); err != nil {
		return "", err
	}
	return uri, nil
}

// transition 推进状态机并持久化 vidu_sync_state（CAS 重试）；持久化失败仅记日志，
// 进程内 job 仍是权威状态（轮询短暂滞后可接受，终态 persistResult 强一致）。
func (s *SyncService) transition(ctx context.Context, job *SyncJob, state, stage string, cause error) {
	s.mu.Lock()
	job.State = state
	job.Stage = stage
	job.Error = ""
	if cause != nil {
		job.Error = cause.Error()
	}
	snap := *job
	s.mu.Unlock()
	if err := s.persistSyncState(ctx, snap); err != nil {
		s.logger.Warn("vidu_sync_state persist failed（轮询短暂滞后）", "job", snap.ID, "err", err)
	}
}

func (s *SyncService) finishError(ctx context.Context, job *SyncJob, stage string, cause error) {
	s.transition(ctx, job, SyncStateError, stage, cause)
	s.mu.Lock()
	delete(s.inflight, job.PersonaID)
	s.last[job.PersonaID] = job
	s.mu.Unlock()
	s.logger.Warn("sync-vidu failed", "persona", job.PersonaID, "stage", stage, "err", cause)
}

// persistResult 终态写库（补偿② 的唯一写点）：vidu_avatar_id/vidu_image_uri 与
// vidu_sync_state=done（含 asset_sha256 供跨重启 dedup）一次 CAS 原子落库。
func (s *SyncService) persistResult(ctx context.Context, job *SyncJob) error {
	s.mu.Lock()
	uri, vid, sha, jobID := job.ImageURI, job.ViduAvatarID, job.AssetSHA256, job.ID
	s.mu.Unlock()
	return s.casUpdate(ctx, job.PersonaID, func(a *Avatar) {
		a.ViduAvatarID = vid
		a.ViduImageURI = uri
		a.ViduSync = ViduSyncState{
			State: SyncStateDone, JobID: jobID, AssetSHA256: sha,
			UpdatedAtMs: time.Now().UnixMilli(),
		}
	})
}

// persistSyncState 中间态/错误态写库（CAS 重试）。
func (s *SyncService) persistSyncState(ctx context.Context, snap SyncJob) error {
	return s.casUpdate(ctx, snap.PersonaID, func(a *Avatar) {
		a.ViduSync = ViduSyncState{
			State: snap.State, Stage: snap.Stage, Error: snap.Error,
			JobID: snap.ID, AssetSHA256: snap.AssetSHA256, ViduAvatarID: snap.ViduAvatarID,
			UpdatedAtMs: time.Now().UnixMilli(),
		}
	})
}

// casUpdate 读-改-写 CAS 循环：撞 ErrRevisionConflict 重读重试（与面板并发编辑兼容）。
func (s *SyncService) casUpdate(ctx context.Context, personaID string, mut func(*Avatar)) error {
	var lastErr error
	for i := 0; i < 8; i++ {
		a, ok, err := s.store.Get(ctx, personaID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		mut(&a)
		lastErr = s.store.UpdateWithRevision(ctx, a, a.Revision)
		if errors.Is(lastErr, ErrRevisionConflict) {
			continue
		}
		return lastErr
	}
	return lastErr
}

// extractAvatarID 容错解析 create_avatar 响应：avatar_id / id / data.avatar_id / data.id
// （字段形状待真机验证，蓝本 client.py:130-152）。
func extractAvatarID(resp map[string]any) string {
	for _, m := range []map[string]any{resp, nestedMap(resp, "data")} {
		for _, k := range []string{"avatar_id", "id"} {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

func nestedMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func coalesceErr(err, def error) error {
	if err != nil {
		return err
	}
	return def
}

// ReconcileOnBoot 开机对账（§2.2）：进程内任务重启即丢，把非终态 vidu_sync_state
// 重置为 error:interrupted；create 成功但未 persist 的孤儿资产 delete_avatar
// best-effort 清理，失败仅记日志（平台 90 天兜底）。mem 存储启动即空，天然无活可干。
func (s *SyncService) ReconcileOnBoot(ctx context.Context) {
	items, _, err := s.store.List(ctx, 10000, 0, false)
	if err != nil {
		s.logger.Warn("sync-vidu boot reconcile list failed", "err", err)
		return
	}
	for _, a := range items {
		switch a.ViduSync.State {
		case SyncStateQueued, SyncStateUploading, SyncStateCreating:
		default:
			continue // 终态（done/error）不动：error 保留续传种子
		}
		orphan := a.ViduSync.ViduAvatarID
		if orphan != "" && s.vidu != nil {
			if err := s.vidu.DeleteAvatar(ctx, orphan); err != nil {
				// best-effort：失败仅记日志；保留 ID 供重试收养该资产。
				s.logger.Warn("orphan vidu avatar cleanup failed（平台 90 天兜底）",
					"persona", a.ID, "vidu_avatar_id", orphan, "err", err)
			} else {
				orphan = "" // 已清理：重试将重新 create
			}
		}
		err := s.casUpdate(ctx, a.ID, func(av *Avatar) {
			av.ViduSync = ViduSyncState{
				State: SyncStateError, Stage: SyncStageInterrupted,
				Error: "sync_interrupted",
				JobID: a.ViduSync.JobID, AssetSHA256: a.ViduSync.AssetSHA256,
				ViduAvatarID: orphan,
				UpdatedAtMs:  time.Now().UnixMilli(),
			}
		})
		if err != nil {
			s.logger.Warn("sync-vidu boot reconcile reset failed", "persona", a.ID, "err", err)
		}
	}
}

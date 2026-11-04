package live

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/publish"
)

// Mode 开播模式（深化方案 v2 §7.6）：
//   - ModePublicLive：公开直播，默认严格模式——必须携带新鲜 preflight_token，
//     opMu 临界区内复核 token/能力/PushGate/重新 Resolve，任一失败不创建场次。
//   - ModeLocalPreview：显式本地预览，允许 fake 实现；禁止公开推流语义，
//     响应与事件均标注 mode，审计记录实现类型。
type Mode string

const (
	ModeLocalPreview Mode = "local-preview"
	ModePublicLive   Mode = "public-live"
)

// ModePolicy 是服务端开播模式的唯一判定依据。零值 fail-closed 为 public-live；
// 只有显式允许且引擎为 dev 时才开放 local-preview。
type ModePolicy struct {
	AllowLocalPreview bool
	EngineMode        string
}

func (p ModePolicy) effectiveMode() Mode {
	if p.AllowLocalPreview && p.EngineMode == "dev" {
		return ModeLocalPreview
	}
	return ModePublicLive
}

func resolveRequestedMode(policy ModePolicy, requested Mode) (Mode, error) {
	mode := policy.effectiveMode()
	if requested != "" && requested != mode {
		return mode, fmt.Errorf("%w: requested=%s server=%s", ErrModeNotAllowed, requested, mode)
	}
	return mode, nil
}

// CheckStatus 预检单项状态（§7.5：只允许 pass/warn/fail/unknown/running；
// 外部耗时不可控时 running/unknown，绝不默认 pass）。
type CheckStatus string

const (
	CheckPass    CheckStatus = "pass"
	CheckWarn    CheckStatus = "warn"
	CheckFail    CheckStatus = "fail"
	CheckUnknown CheckStatus = "unknown"
	CheckRunning CheckStatus = "running"
)

// Check 预检单项结果（§7.5 响应 checks 元素）。
type Check struct {
	Code         string      `json:"code"`
	Status       CheckStatus `json:"status"`
	Blocking     bool        `json:"blocking"`
	Source       string      `json:"source"`
	Target       string      `json:"target,omitempty"`
	Detail       string      `json:"detail,omitempty"`
	ObservedAtMs int64       `json:"observed_at_ms"`
}

// PreflightRequest 预检请求（§7.5）。
type PreflightRequest struct {
	Mode             Mode   `json:"mode"`
	PersonaID        string `json:"persona_id"`
	DraftID          string `json:"draft_id,omitempty"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}

// PreflightResult 预检响应（§7.5 契约）。
type PreflightResult struct {
	CheckID        string           `json:"check_id"`
	PersonaID      string           `json:"persona_id"`
	Revision       int64            `json:"revision"`
	PreflightToken string           `json:"preflight_token"`
	Ready          bool             `json:"ready"`
	Mode           Mode             `json:"mode"`
	DraftID        string           `json:"draft_id,omitempty"`
	DraftRevision  int64            `json:"draft_revision,omitempty"`
	InputRevisions map[string]int64 `json:"input_revisions"`
	ResultDigest   string           `json:"result_digest"`
	ObservedAtMs   int64            `json:"observed_at_ms"`
	ExpiresAtMs    int64            `json:"expires_at_ms"`
	Checks         []Check          `json:"checks"`
}

// 严格 Start 错误（服务层映射见 service.mapLiveError）。
var (
	// ErrModeNotAllowed 客户端声明的 mode 与服务端实际判定不一致。
	ErrModeNotAllowed = errors.New("mode_not_allowed")
	// ErrDraftRequired public-live 缺少必须绑定的已提交草稿。
	ErrDraftRequired = errors.New("draft_required")
	// ErrPreflightRequired public-live 缺少/无效/不匹配的 preflight_token。
	ErrPreflightRequired = errors.New("preflight_required")
	// ErrPreflightExpired token 过期或客户端仍持有旧预检摘要，必须重新预检。
	ErrPreflightExpired = errors.New("preflight_expired")
	// ErrProfileChanged persona_id 或档案 revision 与预检绑定不一致。
	ErrProfileChanged = errors.New("profile_changed")
	// ErrResolveFailed 严格模式下档案 Resolve 失败/档案删除/结果 unknown：
	// 直接报错，不回落旧路径、不创建场次（§7.6-3，堵 M1 fail-open）。
	ErrResolveFailed = errors.New("avatar_resolve_failed")
	// ErrCapabilityBlocked 存在 fail/unknown 阻断项（fake/unconfigured/unknown 均阻断 public）。
	ErrCapabilityBlocked = errors.New("capability_blocked")
	// ErrPushGateRequired public 模式 PushGate 必须已装配（§7.6-5），未装配不是「允许未推流」。
	ErrPushGateRequired = errors.New("push_gate_required")
)

// PreflightTTL public Start 新鲜期工程初值（§7.5：建议 30 秒）。
const PreflightTTL = 30 * time.Second

// tokenBinding token 服务端存证：随机 ID 绑定 mode/persona revision/result_digest/过期
// （§7.5「HMAC 或随机 ID 服务端存证」择一——本实现取随机 ID 服务端存证，
// token 本身无可解析内容，复核必须回源本服务）。
type tokenBinding struct {
	mode           Mode
	personaID      string
	personaRev     int64
	draftID        string
	draftRevision  int64
	resultDigest   string
	ready          bool
	expiresAtMs    int64
	blockingFailed []string // 阻断失败的 check code（ready=false 时非空）
}

// CapabilitySource 装配点显式提供的能力矩阵（§2.2：不许只按配置存在就报 real）。
type CapabilitySource func(ctx context.Context) capability.Matrix

// DraftSource 是内容包只读端口；预检和 Start 必须读取同一持久事实源。
type DraftSource interface {
	GetDraft(ctx context.Context, id string) (contentpkg.Draft, bool, error)
}

// Preflight 唯一 ready 判定服务（§7.5）：页面、apply 和 Start 对同 revision
// 必须得到相同阻断结论；UI 只显示原始状态，不保存第二套规则。
type Preflight struct {
	resolver          avatar.ProfileResolver
	drafts            DraftSource
	caps              CapabilitySource
	pushGate          func() (publish.PushGate, bool) // 与 Manager.currentPushGate 同语义
	storagePersistent func() bool
	now               func() time.Time
	modePolicy        ModePolicy

	mu     sync.Mutex
	tokens map[string]tokenBinding
	seq    int64
}

// NewPreflight 装配唯一预检服务；caps/storagePersistent 为 nil 时按 unknown/fail 处理
// （fail-closed：缺探测源不等于就绪）。
func NewPreflight(
	resolver avatar.ProfileResolver,
	caps CapabilitySource,
	pushGate func() (publish.PushGate, bool),
	storagePersistent func() bool,
) *Preflight {
	return &Preflight{
		resolver: resolver, caps: caps, pushGate: pushGate,
		storagePersistent: storagePersistent,
		tokens:            map[string]tokenBinding{}, now: time.Now,
	}
}

// SetDraftSource 注入内容包只读事实源；公开开播携带草稿时未装配会 fail-closed。
func (p *Preflight) SetDraftSource(source DraftSource) {
	p.mu.Lock()
	p.drafts = source
	p.mu.Unlock()
}

// SetModePolicy 注入服务端模式策略；请求中的 mode 仅用于一致性校验，绝不参与判定。
func (p *Preflight) SetModePolicy(policy ModePolicy) {
	p.mu.Lock()
	p.modePolicy = policy
	p.mu.Unlock()
}

func (p *Preflight) currentModePolicy() ModePolicy {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.modePolicy
}

// Run 执行一次预检并签发服务端绑定 token（无论 ready 与否都签发，
// Start 复核时按 token 存证判定，杜绝客户端伪造 ready）。
func (p *Preflight) Run(ctx context.Context, req PreflightRequest) (PreflightResult, error) {
	mode, err := resolveRequestedMode(p.currentModePolicy(), req.Mode)
	if err != nil {
		return PreflightResult{Mode: mode}, err
	}
	if mode == ModePublicLive && req.DraftID == "" {
		return PreflightResult{Mode: mode}, fmt.Errorf("%w: public-live requires draft_id", ErrDraftRequired)
	}
	req.Mode = mode
	return p.run(ctx, req, true), nil
}

func (p *Preflight) run(ctx context.Context, req PreflightRequest, issueToken bool) PreflightResult {
	now := p.now()
	obs := now.UnixMilli()
	mode := req.Mode
	if mode == "" {
		mode = ModePublicLive
	}
	checks := make([]Check, 0, 9)
	inputRevs := map[string]int64{}
	draftRev := req.ExpectedRevision
	snapshotHash := ""
	add := func(code string, st CheckStatus, blocking bool, source, target, detail string) {
		checks = append(checks, Check{
			Code: code, Status: st, Blocking: blocking, Source: source,
			Target: target, Detail: detail, ObservedAtMs: obs,
		})
	}

	// avatar_resolve：public 必须 pass；local-preview 未指定档案为 warn（允许无档案调试），
	// 指定但解析失败为 fail（显式拒绝，不许静默回落旧路径）。
	personaRev := int64(0)
	switch {
	case req.PersonaID == "":
		// 未请求档案：不是前置项（公开链路是否必须有档案由产品决策，本切片不阻断）。
		add("avatar_resolve", CheckWarn, false, "profile_resolver", "avatar", "no_persona")
	case p.resolver == nil:
		// 指定了档案但无 resolver：public fail；local warn（unknown 绝不默认 pass）。
		if mode == ModePublicLive {
			add("avatar_resolve", CheckFail, true, "profile_resolver", "avatar", "resolver_unconfigured")
		} else {
			add("avatar_resolve", CheckWarn, false, "profile_resolver", "avatar", "resolver_unconfigured")
		}
	default:
		prof, err := p.resolver.Resolve(ctx, req.PersonaID)
		switch {
		case err != nil:
			add("avatar_resolve", CheckFail, true, "profile_resolver", "avatar", "resolve_failed: "+err.Error())
		case !prof.ViduSynced || prof.ImageExpired(now):
			add("avatar_resolve", CheckFail, true, "profile_resolver", "avatar", "avatar_expired")
		default:
			personaRev = prof.PersonaRevision
			add("avatar_resolve", CheckPass, true, "profile_resolver", "avatar", "")
		}
	}
	if req.PersonaID != "" {
		inputRevs["avatar"] = personaRev
	}

	// content_package：Start 只能使用已提交的完整不可变草稿。所有嵌套 AssetRef
	// 都必须带 id 且至少绑定 revision 或 content_hash，revision 集合进入 token 与 digest 绑定。
	if req.DraftID == "" {
		add("content_package", CheckWarn, false, "contentpkg", "draft", "not_requested")
	} else {
		p.mu.Lock()
		drafts := p.drafts
		p.mu.Unlock()
		d, ok, err := contentpkg.Draft{}, false, error(nil)
		if drafts == nil {
			err = errors.New("draft_store_unconfigured")
		} else {
			d, ok, err = drafts.GetDraft(ctx, req.DraftID)
		}
		var draftErr string
		switch {
		case err != nil:
			draftErr = err.Error()
		case !ok:
			draftErr = "draft_not_found"
		case d.Status != contentpkg.DraftCommitted:
			draftErr = "draft_not_committed"
		case req.ExpectedRevision <= 0 || d.Revision != req.ExpectedRevision:
			draftErr = "draft_revision_mismatch"
		case d.SnapshotHash == "" || d.Snapshot == nil:
			draftErr = "snapshot_incomplete"
		case d.SnapshotHash != contentpkg.HashSnapshot(d.Snapshot):
			draftErr = "snapshot_hash_mismatch"
		case d.SchemaVersion >= 2 && !draftAvatarMatches(d.Snapshot, req.PersonaID, personaRev):
			draftErr = "draft_avatar_changed: 形象或版本与草稿不一致，请重新保存并提交"
		default:
			var revs map[string]int64
			revs, err = inspectAssetRefs(d.Snapshot)
			if err != nil {
				draftErr = err.Error()
			} else {
				for key, rev := range revs {
					inputRevs[key] = rev
				}
				draftRev = d.Revision
				snapshotHash = d.SnapshotHash
				inputRevs["draft"] = d.Revision
			}
		}
		if draftErr != "" {
			add("content_package", CheckFail, true, "contentpkg", req.DraftID, draftErr)
		} else {
			add("content_package", CheckPass, true, "contentpkg", req.DraftID, "")
		}
	}

	// 能力矩阵硬前置：public 下 fake/unconfigured/unknown 均 fail；
	// local-preview 下 fake 显式标注 warn（允许但可见），unconfigured/unknown 仍 warn 不阻断。
	var mx capability.Matrix
	if p.caps != nil {
		mx = p.caps(ctx)
	}
	capCheck := func(code, target string) {
		s := mx.Get(code)
		st, blocking := CheckFail, true // unknown 与 fail 同阻断，绝不默认 pass
		if s == capability.Real {
			st = CheckPass
		} else if mode != ModePublicLive {
			st, blocking = CheckWarn, false // local-preview：fake/unconfigured/unknown 显式标注可见但不阻断
		}
		detail := "capability=" + string(s)
		if code == "song_player" {
			// 本地真实可播与云端送达是两个独立证据项，ready 不得暗示观众可听。
			detail += "; song_player 只证明本地真实可播，不证明音频已送达云端"
		}
		if code == "action_sender" && s != capability.Real {
			detail += "; action_sender_" + string(s)
			switch s {
			case capability.Unconfigured:
				detail += "; 动作下发通道未配置，请配置 Vidu Key 并确认真实引擎装配"
			case capability.Fake:
				detail += "; 当前为 dev 假动作下发，仅允许本地预览"
			default:
				detail += "; 动作下发通道当前不可用，请检查 Vidu 网络、凭据与真实建场状态"
			}
		}
		if code == "koubo_actiongen" && s != capability.Real {
			detail += "; koubo_actiongen_" + string(s)
			switch s {
			case capability.Unconfigured:
				detail += "; 口播动作生成器或 choreo 动作内容未配置"
			case capability.Fake:
				detail += "; 当前为假动作生成，仅允许本地预览"
			default:
				detail += "; 口播动作生成当前不可用，请检查 choreo 内容与 LLM 配置"
			}
		}
		add(code, st, blocking, "capability_probe", target, detail)
	}
	capCheck("action_sender", "actions")
	capCheck("koubo_actiongen", "koubo")
	capCheck("content_storage", "content")
	capCheck("song_player", "songs")
	capCheck("tts", "tts")
	capCheck("vidu", "vidu")

	// push_gate：能力矩阵与真实 PushGate 同源读取；状态可见，public 仍由 CheckStartLive fail-closed。
	pushCapability := "capability=" + string(mx.Get("push_gate"))
	if p.pushGate != nil {
		g, allowUnpushed := p.pushGate()
		switch {
		case g == nil:
			if mode == ModePublicLive {
				add("push_gate", CheckFail, true, "push_gate", "publish", pushCapability+"; push_gate_not_wired")
			} else {
				add("push_gate", CheckWarn, false, "push_gate", "publish", pushCapability+"; push_gate_not_wired")
			}
		default:
			if err := publish.CheckStartLive(g, allowUnpushed && mode == ModeLocalPreview); err != nil {
				if mode == ModePublicLive {
					add("push_gate", CheckFail, true, "push_gate", "publish", pushCapability+"; "+err.Error())
				} else {
					add("push_gate", CheckWarn, false, "push_gate", "publish", pushCapability+"; "+err.Error())
				}
			} else {
				add("push_gate", CheckPass, true, "push_gate", "publish", pushCapability)
			}
		}
	} else {
		add("push_gate", CheckUnknown, mode == ModePublicLive, "push_gate", "publish", pushCapability+"; probe_unconfigured")
	}

	// storage_persistent：Ent 已安装且 PG 当前可达才 pass；故障时公开场阻断，本地预览显式 warn。
	persistent := false
	if p.storagePersistent != nil {
		persistent = p.storagePersistent()
	}
	storageCapability := mx.Get("storage_persistent")
	storageDetail := "capability=" + string(storageCapability)
	switch {
	case persistent && storageCapability == capability.Real:
		add("storage_persistent", CheckPass, true, "session_store", "storage", storageDetail)
	case mode == ModePublicLive:
		reason := "memory_store_public_blocked"
		if persistent {
			reason = "postgres_unreachable"
		}
		add("storage_persistent", CheckFail, true, "session_store", "storage", storageDetail+"; "+reason)
	default:
		reason := "memory_store"
		if persistent {
			reason = "postgres_unreachable"
		}
		add("storage_persistent", CheckWarn, false, "session_store", "storage", storageDetail+"; "+reason)
	}

	ready := true
	var blockingFailed []string
	for _, c := range checks {
		// unknown/running 同样阻断（绝不默认 pass）；warn 不阻断。
		if c.Blocking && c.Status != CheckPass {
			ready = false
			blockingFailed = append(blockingFailed, c.Code+":"+string(c.Status))
		}
	}
	sort.Strings(blockingFailed)

	digest := digestChecks(mode, req.PersonaID, personaRev, req.DraftID, draftRev, snapshotHash, inputRevs, checks)
	p.mu.Lock()
	p.seq++
	checkID := fmt.Sprintf("pf_%d_%d", obs, p.seq)
	token := ""
	if issueToken {
		token = "pft_" + randToken()
		p.tokens[token] = tokenBinding{
			mode: mode, personaID: req.PersonaID, personaRev: personaRev,
			draftID: req.DraftID, draftRevision: draftRev,
			resultDigest: digest, ready: ready,
			expiresAtMs:    now.Add(PreflightTTL).UnixMilli(),
			blockingFailed: blockingFailed,
		}
	}
	p.mu.Unlock()

	return PreflightResult{
		CheckID: checkID, PersonaID: req.PersonaID, Revision: personaRev,
		PreflightToken: token, Ready: ready, Mode: mode,
		DraftID: req.DraftID, DraftRevision: draftRev,
		InputRevisions: inputRevs, ResultDigest: digest,
		ObservedAtMs: obs, ExpiresAtMs: now.Add(PreflightTTL).UnixMilli(),
		Checks: checks,
	}
}

// VerifyRequest 是 public Start 的完整预检绑定；任一字段缺失都不能走兼容旁路。
type VerifyRequest struct {
	Token            string
	Mode             Mode
	PersonaID        string
	PersonaRevision  int64
	DraftID          string
	ExpectedRevision int64
	ResultDigest     string
}

// Verify 在 Start 临界区内用同一套求值逻辑重新探测外部状态并计算 digest。
// token、客户端摘要、当前摘要三者必须一致，能力/推流/资产退化后只能重新预检。
func (p *Preflight) Verify(ctx context.Context, req VerifyRequest) error {
	mode, err := resolveRequestedMode(p.currentModePolicy(), req.Mode)
	if err != nil {
		return err
	}
	req.Mode = mode
	if mode == ModePublicLive && req.DraftID == "" {
		return fmt.Errorf("%w: public-live requires draft_id", ErrDraftRequired)
	}
	if req.Token == "" {
		return fmt.Errorf("%w: missing preflight_token", ErrPreflightRequired)
	}
	p.mu.Lock()
	b, ok := p.tokens[req.Token]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: unknown token", ErrPreflightRequired)
	}
	if p.now().UnixMilli() > b.expiresAtMs {
		return fmt.Errorf("%w: %w: token expired", ErrPreflightRequired, ErrPreflightExpired)
	}
	if b.mode != req.Mode {
		return fmt.Errorf("%w: token binding mismatch (mode)", ErrPreflightRequired)
	}
	if b.personaID != req.PersonaID {
		return fmt.Errorf("%w: persona_id changed after preflight", ErrProfileChanged)
	}
	if b.personaID != "" && req.PersonaRevision == 0 {
		return fmt.Errorf("%w: revision is required", ErrPreflightRequired)
	}
	if b.personaID != "" && b.personaRev != req.PersonaRevision {
		return fmt.Errorf("%w: revision does not match preflight", ErrProfileChanged)
	}
	if req.ResultDigest != "" && req.ResultDigest != b.resultDigest {
		return fmt.Errorf("%w: %w: result digest mismatch", ErrPreflightRequired, ErrPreflightExpired)
	}
	if !b.ready {
		return fmt.Errorf("%w: preflight not ready (%v)", ErrCapabilityBlocked, b.blockingFailed)
	}
	if req.Mode == ModePublicLive && (req.DraftID == "" || req.ExpectedRevision <= 0 || req.ResultDigest == "") {
		return fmt.Errorf("%w: draft_id, expected_revision and result_digest are required", ErrPreflightRequired)
	}
	if b.draftID != req.DraftID || b.draftRevision != req.ExpectedRevision {
		return fmt.Errorf("%w: draft binding mismatch", ErrPreflightRequired)
	}
	current := p.run(ctx, PreflightRequest{
		Mode: req.Mode, PersonaID: req.PersonaID, DraftID: req.DraftID, ExpectedRevision: req.ExpectedRevision,
	}, false)
	if !current.Ready {
		return fmt.Errorf("%w: current preflight not ready", ErrPreflightRequired)
	}
	if current.PersonaID != b.personaID || current.Revision != b.personaRev {
		return fmt.Errorf("%w: profile revision changed after preflight", ErrProfileChanged)
	}
	if current.ResultDigest != b.resultDigest {
		return fmt.Errorf("%w: %w: external state changed after preflight", ErrPreflightRequired, ErrPreflightExpired)
	}
	// token 单次消费：Manager.opMu 串行化 Start；成功复核后立即删除存证，重放即 unknown token。
	p.mu.Lock()
	delete(p.tokens, req.Token)
	p.mu.Unlock()
	return nil
}

// digestChecks 对稳定输入与结论规范化后取 sha256。观测时间不参与摘要，
// 否则 Start 重算必然变化；checks 与 map 序列化顺序均固定。
func digestChecks(mode Mode, personaID string, personaRev int64, draftID string, draftRev int64, snapshotHash string, inputRevs map[string]int64, checks []Check) string {
	sorted := append([]Check(nil), checks...)
	for i := range sorted {
		sorted[i].ObservedAtMs = 0
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Code < sorted[j].Code })
	payload, _ := json.Marshal(struct {
		Mode         Mode             `json:"mode"`
		Persona      string           `json:"persona_id"`
		PRev         int64            `json:"persona_revision"`
		Draft        string           `json:"draft_id"`
		DRev         int64            `json:"draft_revision"`
		SnapshotHash string           `json:"snapshot_hash"`
		Inputs       map[string]int64 `json:"input_revisions"`
		Checks       []Check          `json:"checks"`
	}{mode, personaID, personaRev, draftID, draftRev, snapshotHash, inputRevs, sorted})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func inspectAssetRefs(snapshot map[string]any) (map[string]int64, error) {
	revisions := map[string]int64{}
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			_, hasID := v["id"]
			_, hasRevision := v["revision"]
			_, hasHash := v["content_hash"]
			if hasID && (hasRevision || hasHash) {
				id, idOK := v["id"].(string)
				rev, revOK := revisionNumber(v["revision"])
				hash, hashOK := v["content_hash"].(string)
				revisionBound := revOK && rev > 0
				hashBound := hashOK && hash != ""
				if !idOK || id == "" || (!revisionBound && !hashBound) {
					return errors.New("asset_ref_incomplete")
				}
				if revisionBound {
					key := "asset:" + id
					if previous, exists := revisions[key]; exists && previous != rev {
						return errors.New("asset_revision_conflict")
					}
					revisions[key] = rev
				}
			}
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(snapshot); err != nil {
		return nil, err
	}
	return revisions, nil
}

// v2 草稿由准备页创建，必须绑定本次解析出的真实档案版本。
func draftAvatarMatches(snapshot map[string]any, personaID string, revision int64) bool {
	ref, ok := snapshot["avatar"].(map[string]any)
	if !ok || personaID == "" || revision <= 0 {
		return false
	}
	id, _ := ref["id"].(string)
	rev, valid := revisionNumber(ref["revision"])
	return valid && id == personaID && rev == revision
}

func revisionNumber(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), v == float64(int64(v))
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

func randToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

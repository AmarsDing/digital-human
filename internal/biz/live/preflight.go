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
	// ErrPreflightRequired public-live 缺少/无效/过期/不匹配的 preflight_token。
	ErrPreflightRequired = errors.New("preflight_required")
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

// Preflight 唯一 ready 判定服务（§7.5）：页面、apply 和 Start 对同 revision
// 必须得到相同阻断结论；UI 只显示原始状态，不保存第二套规则。
type Preflight struct {
	resolver          avatar.ProfileResolver
	caps              CapabilitySource
	pushGate          func() (publish.PushGate, bool) // 与 Manager.currentPushGate 同语义
	storagePersistent func() bool
	now               func() time.Time

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

// Run 执行一次预检并签发服务端绑定 token（无论 ready 与否都签发，
// Start 复核时按 token 存证判定，杜绝客户端伪造 ready）。
func (p *Preflight) Run(ctx context.Context, req PreflightRequest) PreflightResult {
	now := p.now()
	obs := now.UnixMilli()
	mode := req.Mode
	if mode == "" {
		mode = ModePublicLive
	}
	checks := make([]Check, 0, 8)
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

	// 能力矩阵四项（§2.2/§2.3 硬前置）：public 下 fake/unconfigured/unknown 均 fail；
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
		add(code, st, blocking, "capability_probe", target, "capability="+string(s))
	}
	capCheck("action_sender", "actions")
	capCheck("song_player", "songs")
	capCheck("tts", "tts")
	capCheck("vidu", "vidu")

	// push_gate：public 必须已装配且推流检查通过（§7.6-5）；local-preview 未装配为 warn。
	if p.pushGate != nil {
		g, allowUnpushed := p.pushGate()
		switch {
		case g == nil:
			if mode == ModePublicLive {
				add("push_gate", CheckFail, true, "push_gate", "publish", "push_gate_not_wired")
			} else {
				add("push_gate", CheckWarn, false, "push_gate", "publish", "push_gate_not_wired")
			}
		default:
			if err := publish.CheckStartLive(g, allowUnpushed); err != nil {
				if mode == ModePublicLive {
					add("push_gate", CheckFail, true, "push_gate", "publish", err.Error())
				} else {
					add("push_gate", CheckWarn, false, "push_gate", "publish", err.Error())
				}
			} else {
				add("push_gate", CheckPass, true, "push_gate", "publish", "")
			}
		}
	} else {
		add("push_gate", CheckUnknown, mode == ModePublicLive, "push_gate", "publish", "probe_unconfigured")
	}

	// storage_persistent：§2.2 SessionStore/EventSink——内存实现只能 local-preview，
	// 公开场必须 persistent。
	persistent := false
	if p.storagePersistent != nil {
		persistent = p.storagePersistent()
	}
	switch {
	case persistent:
		add("storage_persistent", CheckPass, true, "session_store", "storage", "")
	case mode == ModePublicLive:
		add("storage_persistent", CheckFail, true, "session_store", "storage", "memory_store_public_blocked")
	default:
		add("storage_persistent", CheckWarn, false, "session_store", "storage", "memory_store")
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

	digest := digestChecks(mode, req.PersonaID, personaRev, req.DraftID, req.ExpectedRevision, checks)
	p.mu.Lock()
	p.seq++
	checkID := fmt.Sprintf("pf_%d_%d", obs, p.seq)
	token := "pft_" + randToken()
	p.tokens[token] = tokenBinding{
		mode: mode, personaID: req.PersonaID, personaRev: personaRev,
		draftID: req.DraftID, draftRevision: req.ExpectedRevision,
		resultDigest: digest, ready: ready,
		expiresAtMs:    now.Add(PreflightTTL).UnixMilli(),
		blockingFailed: blockingFailed,
	}
	p.mu.Unlock()

	inputRevs := map[string]int64{}
	if req.PersonaID != "" {
		inputRevs["avatar"] = personaRev
	}
	if req.DraftID != "" {
		inputRevs["draft"] = req.ExpectedRevision
	}
	return PreflightResult{
		CheckID: checkID, PreflightToken: token, Ready: ready, Mode: mode,
		DraftID: req.DraftID, DraftRevision: req.ExpectedRevision,
		InputRevisions: inputRevs, ResultDigest: digest,
		ObservedAtMs: obs, ExpiresAtMs: now.Add(PreflightTTL).UnixMilli(),
		Checks: checks,
	}
}

// Verify public Start 临界区复核（§7.6-2）：token 存在、未过期、mode/persona 一致、
// ready=true 且 digest 匹配（复核方传入当前重算的 digest）。任一不符显式报错。
func (p *Preflight) Verify(token string, mode Mode, personaID string, personaRev int64, redigest string) error {
	if token == "" {
		return fmt.Errorf("%w: missing preflight_token", ErrPreflightRequired)
	}
	p.mu.Lock()
	b, ok := p.tokens[token]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: unknown token", ErrPreflightRequired)
	}
	if p.now().UnixMilli() > b.expiresAtMs {
		return fmt.Errorf("%w: token expired", ErrPreflightRequired)
	}
	if b.mode != mode || b.personaID != personaID {
		return fmt.Errorf("%w: token binding mismatch (mode/persona)", ErrPreflightRequired)
	}
	if personaRev != 0 && b.personaRev != 0 && b.personaRev != personaRev {
		return fmt.Errorf("%w: persona revision changed after preflight", ErrPreflightRequired)
	}
	if redigest != "" && redigest != b.resultDigest {
		return fmt.Errorf("%w: result digest mismatch", ErrPreflightRequired)
	}
	if !b.ready {
		return fmt.Errorf("%w: preflight not ready (%v)", ErrCapabilityBlocked, b.blockingFailed)
	}
	return nil
}

// digestChecks 对 mode/persona/revision/checks 规范化后取 sha256；
// checks 排序后序列化，保证同结论同 digest。
func digestChecks(mode Mode, personaID string, personaRev int64, draftID string, draftRev int64, checks []Check) string {
	sorted := append([]Check(nil), checks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Code < sorted[j].Code })
	payload, _ := json.Marshal(struct {
		Mode    Mode    `json:"mode"`
		Persona string  `json:"persona_id"`
		PRev    int64   `json:"persona_revision"`
		Draft   string  `json:"draft_id"`
		DRev    int64   `json:"draft_revision"`
		Checks  []Check `json:"checks"`
	}{mode, personaID, personaRev, draftID, draftRev, sorted})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func randToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

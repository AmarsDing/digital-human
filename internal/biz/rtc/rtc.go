package rtc

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// ErrUnavailable 表示 RTC 已声明需要真实签发器但尚未装配，必须显式不可用。
var ErrUnavailable = errors.New("rtc_issuer_not_wired")

// Config 凭据来源统一走配置/环境变量；敏感值不写 yaml。
type Config struct {
	Mode       string        // disabled | dev | managed
	Provider   string        // artc | trtc
	RoomPrefix string        // 房间前缀，默认 dh-
	TokenTTL   time.Duration // 默认 600s
	APIKey     string        // 环境变量 DH_RTC_API_KEY
	APISecret  string        // 环境变量 DH_RTC_API_SECRET
	RealIssuer RealIssuer    // managed 模式的真实签发器；nil 即 unavailable
}

// RealIssuer 真实厂商签发器接口；未装配时 managed 模式 fail-closed。
type RealIssuer interface {
	Issue(sessionID, roomID, userID string, ttl time.Duration) (token string, expiresAt time.Time, err error)
}

// Tokens 一次签发结果。Mode 使用前端契约值（managed）或 unconfigured。
type Tokens struct {
	Mode      string
	Provider  string
	Token     string
	RoomID    string
	UserID    string
	ExpiresAt int64 // epoch ms
	OK        bool
	Reason    string
}

type Issuer struct {
	cfg      Config
	now      func() time.Time
	issueSeq atomic.Int64
}

func NewIssuer(cfg Config) *Issuer {
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = 10 * time.Minute
	}
	if cfg.RoomPrefix == "" {
		cfg.RoomPrefix = "dh-"
	}
	return &Issuer{cfg: cfg, now: time.Now}
}

// Configured 只有显式 dev 且凭据齐全，或 managed 且真实签发器已装配，才算可用。
func (i *Issuer) Configured() bool {
	switch strings.ToLower(strings.TrimSpace(i.cfg.Mode)) {
	case "dev":
		return i.cfg.APIKey != "" && i.cfg.APISecret != "" && i.validProvider()
	case "managed":
		return i.cfg.RealIssuer != nil
	default:
		return false
	}
}

func (i *Issuer) validProvider() bool {
	p := strings.ToLower(strings.TrimSpace(i.cfg.Provider))
	return p == "artc" || p == "trtc"
}

// Status 返回状态展示值；未配置时明确 unconfigured/none。
func (i *Issuer) Status() (mode, provider string) {
	if !i.Configured() {
		return "unconfigured", "none"
	}
	return "managed", strings.ToLower(strings.TrimSpace(i.cfg.Provider))
}

// Issue 为指定场次签发 token；未配置时返回 OK=false，不伪造可用。
func (i *Issuer) Issue(sessionID string) (Tokens, error) {
	if sessionID == "" {
		return Tokens{}, errors.New("session_id_required")
	}
	if !i.Configured() {
		return Tokens{Mode: "unconfigured", Provider: "none", OK: false, Reason: "rtc_not_configured"}, nil
	}
	roomID := i.cfg.RoomPrefix + sessionID
	userID := "viewer-" + sessionID
	exp := i.now().Add(i.cfg.TokenTTL)

	mode := strings.ToLower(strings.TrimSpace(i.cfg.Mode))
	if mode == "managed" {
		token, at, err := i.cfg.RealIssuer.Issue(sessionID, roomID, userID, i.cfg.TokenTTL)
		if err != nil {
			return Tokens{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return Tokens{
			Mode: "managed", Provider: i.cfg.Provider, Token: token,
			RoomID: roomID, UserID: userID, ExpiresAt: at.UnixMilli(), OK: true,
		}, nil
	}

	// dev 模式：本地 HMAC 凭据，仅用于联调，不能直接用于真实厂商 SDK。
	// 每次签发带自增序号，确保刷新必然得到新 token（exp 相同也不会复用）。
	nonce := i.issueSeq.Add(1)
	msg := strings.Join([]string{roomID, userID, fmt.Sprint(exp.UnixMilli()), fmt.Sprint(nonce)}, "|")
	mac := hmac.New(sha256.New, []byte(i.cfg.APISecret))
	mac.Write([]byte(msg))
	token := "rtc-dev." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return Tokens{
		Mode: "managed", Provider: i.cfg.Provider, Token: token,
		RoomID: roomID, UserID: userID, ExpiresAt: exp.UnixMilli(), OK: true,
	}, nil
}

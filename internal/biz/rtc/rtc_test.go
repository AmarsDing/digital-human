package rtc

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 默认 disabled：必须明确 unconfigured/none，且签发不产出 token。
func TestIssuerUnconfiguredByDefault(t *testing.T) {
	iss := NewIssuer(Config{})
	if iss.Configured() {
		t.Fatal("默认配置不得视为可用")
	}
	mode, provider := iss.Status()
	if mode != "unconfigured" || provider != "none" {
		t.Fatalf("status=%s/%s want unconfigured/none", mode, provider)
	}
	tok, err := iss.Issue("s-1")
	if err != nil {
		t.Fatalf("unconfigured issue must not error: %v", err)
	}
	if tok.OK || tok.Token != "" || tok.Mode != "unconfigured" || tok.Reason != "rtc_not_configured" {
		t.Fatalf("unconfigured tokens=%+v", tok)
	}
}

// dev 模式缺 key/secret 或 provider 非法时仍不可用。
func TestIssuerDevRequiresCredentials(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"missing_secret", Config{Mode: "dev", Provider: "artc", APIKey: "ak"}},
		{"missing_key", Config{Mode: "dev", Provider: "artc", APISecret: "sk"}},
		{"bad_provider", Config{Mode: "dev", Provider: "foo", APIKey: "ak", APISecret: "sk"}},
		{"empty_provider", Config{Mode: "dev", APIKey: "ak", APISecret: "sk"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			iss := NewIssuer(c.cfg)
			if iss.Configured() {
				t.Fatalf("%s 不应视为可用", c.name)
			}
			tok, _ := iss.Issue("s-1")
			if tok.OK {
				t.Fatalf("%s 不得签发: %+v", c.name, tok)
			}
		})
	}
}

// dev 可用：签发带 room/user/expiresAt 的本地凭据，TTL 正确。
func TestIssuerDevIssuesTokenWithTTL(t *testing.T) {
	iss := NewIssuer(Config{
		Mode: "dev", Provider: "artc", RoomPrefix: "dh-",
		APIKey: "ak", APISecret: "sk", TokenTTL: 5 * time.Minute,
	})
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	iss.now = func() time.Time { return now }

	tok, err := iss.Issue("s-1")
	if err != nil {
		t.Fatal(err)
	}
	if !tok.OK || tok.Mode != "managed" || tok.Provider != "artc" {
		t.Fatalf("tokens=%+v", tok)
	}
	if !strings.HasPrefix(tok.Token, "rtc-dev.") {
		t.Fatalf("dev token prefix=%q", tok.Token)
	}
	if tok.RoomID != "dh-s-1" || tok.UserID != "viewer-s-1" {
		t.Fatalf("room/user=%s/%s", tok.RoomID, tok.UserID)
	}
	if tok.ExpiresAt != now.Add(5*time.Minute).UnixMilli() {
		t.Fatalf("expiresAt=%d want %d", tok.ExpiresAt, now.Add(5*time.Minute).UnixMilli())
	}

	// 刷新：同一 room/user，token 必须轮换。
	tok2, err := iss.Issue("s-1")
	if err != nil {
		t.Fatal(err)
	}
	if tok2.Token == tok.Token {
		t.Fatalf("refresh 必须换新 token: %q", tok2.Token)
	}
	if tok2.RoomID != tok.RoomID || tok2.UserID != tok.UserID {
		t.Fatalf("refresh 不得换 room/user: %+v", tok2)
	}
}

// 空 session 必须报错，避免为"无名场次"签发凭据。
func TestIssuerRejectsEmptySession(t *testing.T) {
	iss := NewIssuer(Config{Mode: "dev", Provider: "artc", APIKey: "ak", APISecret: "sk"})
	if _, err := iss.Issue(""); err == nil {
		t.Fatal("empty session 必须报错")
	}
}

type errIssuer struct{ err error }

func (e errIssuer) Issue(string, string, string, time.Duration) (string, time.Time, error) {
	return "", time.Time{}, e.err
}

type okIssuer struct {
	token string
	at    time.Time
}

func (o okIssuer) Issue(string, string, string, time.Duration) (string, time.Time, error) {
	return o.token, o.at, nil
}

// managed 但未装配真实签发器：fail-closed，绝不降级成 dev 凭据。
func TestIssuerManagedWithoutRealIssuerFailsClosed(t *testing.T) {
	iss := NewIssuer(Config{Mode: "managed", Provider: "artc", TokenTTL: time.Minute})
	if iss.Configured() {
		t.Fatal("managed 无 RealIssuer 不得视为可用")
	}
	tok, err := iss.Issue("s-1")
	if err != nil {
		t.Fatal(err)
	}
	if tok.OK || tok.Mode != "unconfigured" {
		t.Fatalf("managed unavailable tokens=%+v", tok)
	}
	if strings.HasPrefix(tok.Token, "rtc-dev.") {
		t.Fatalf("不得用 dev 凭据冒充 managed: %q", tok.Token)
	}
}

// managed 真实签发器成功：透传 token 与厂商过期时间。
func TestIssuerManagedDelegatesToRealIssuer(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 5, 0, 0, time.UTC)
	iss := NewIssuer(Config{
		Mode: "managed", Provider: "trtc", RoomPrefix: "dh-", TokenTTL: 5 * time.Minute,
		RealIssuer: okIssuer{token: "vendor-token", at: at},
	})
	if !iss.Configured() {
		t.Fatal("装配 RealIssuer 后应可用")
	}
	if mode, provider := iss.Status(); mode != "managed" || provider != "trtc" {
		t.Fatalf("status=%s/%s", mode, provider)
	}
	tok, err := iss.Issue("s-1")
	if err != nil {
		t.Fatal(err)
	}
	if !tok.OK || tok.Token != "vendor-token" || tok.ExpiresAt != at.UnixMilli() {
		t.Fatalf("tokens=%+v", tok)
	}
}

// managed 真实签发器报错：必须返回 ErrUnavailable，不能伪造成功。
func TestIssuerManagedRealErrorPropagates(t *testing.T) {
	boom := errors.New("vendor_denied")
	iss := NewIssuer(Config{Mode: "managed", Provider: "artc", RealIssuer: errIssuer{err: boom}})
	_, err := iss.Issue("s-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "vendor_denied") {
		t.Fatalf("err 应保留根因: %v", err)
	}
}

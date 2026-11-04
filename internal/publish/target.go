package publish

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"unicode"
)

// TargetVerification 描述当前目标已经取得的发布回执等级，只存在于进程内存。
type TargetVerification string

const (
	TargetUnknown TargetVerification = "unknown"
	TargetLocal   TargetVerification = "local"
	TargetDouyin  TargetVerification = "douyin"
)

// Error 是可直接展示给用户的推流错误；Message 不得包含推流码。
type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	msg := "[" + e.Code + "] " + e.Message
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// ErrorCode 返回稳定错误码；非推流错误返回空串。
func ErrorCode(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func invalidTarget(message string) error {
	return &Error{Code: "publish_target_invalid", Message: message}
}

// normalizeTarget 校验并规范化推流目标。key 为空时，完整 URL 的末段作为 key。
func normalizeTarget(t Target) (Target, error) {
	raw := strings.TrimSpace(t.RTMPURL)
	if raw == "" {
		return Target{}, invalidTarget("推流地址不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "rtmp" {
		return Target{}, invalidTarget("推流地址必须使用 rtmp:// 协议")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return Target{}, invalidTarget("推流地址不得包含用户信息、query 或 fragment")
	}
	host := u.Hostname()
	if !validHostname(host) {
		return Target{}, invalidTarget("推流地址域名无效")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Target{}, invalidTarget("推流地址端口必须在 1-65535 之间")
		}
	}

	path := strings.Trim(u.Path, "/")
	if path == "" {
		return Target{}, invalidTarget("推流地址缺少 app")
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || hasSpaceOrControl(part) {
			return Target{}, invalidTarget("推流地址 app 路径无效")
		}
	}
	key := t.Key
	if key == "" {
		key = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
		if len(parts) == 0 {
			return Target{}, invalidTarget("推流地址缺少 app")
		}
	}
	if key == "" || hasSpaceOrControl(key) {
		return Target{}, invalidTarget("推流码不能为空或包含空白字符")
	}
	app := strings.Join(parts, "/")
	t.RTMPURL = (&url.URL{Scheme: "rtmp", Host: u.Host, Path: "/" + app}).String()
	t.Key = key
	return t, nil
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 || hasSpaceOrControl(host) {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func hasSpaceOrControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0
}

// classifyTarget 只识别明确的抖音 ingest 与本地协议目标，其他一律 unknown。
func classifyTarget(rawURL string) TargetVerification {
	u, err := url.Parse(rawURL)
	if err != nil {
		return TargetUnknown
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" {
		return TargetLocal
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		return TargetLocal
	}
	first, _, _ := strings.Cut(host, ".")
	if strings.HasPrefix(first, "push-") && strings.HasSuffix(host, ".douyincdn.com") {
		return TargetDouyin
	}
	return TargetUnknown
}

func classifyNetworkError(err error) error {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return &Error{Code: "publish_dns_failed", Message: "推流域名解析失败，请检查域名和网络", Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: "publish_network_timeout", Message: "连接推流服务超时，请检查网络和端口", Err: err}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Code: "publish_network_timeout", Message: "连接推流服务超时，请检查网络和端口", Err: err}
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return &Error{Code: "publish_port_unreachable", Message: "推流端口拒绝连接，请检查端口和推流服务", Err: err}
	}
	return &Error{Code: "publish_network_unreachable", Message: "无法连接推流服务，请检查域名、网络和端口", Err: err}
}

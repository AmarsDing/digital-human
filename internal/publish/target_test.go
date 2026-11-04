package publish

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestNormalizeTarget(t *testing.T) {
	tests := []struct {
		name    string
		in      Target
		wantURL string
		wantKey string
	}{
		{
			name:    "分离地址和推流码",
			in:      Target{RTMPURL: " rtmp://live.example.com/live ", Key: "secret"},
			wantURL: "rtmp://live.example.com/live",
			wantKey: "secret",
		},
		{
			name:    "完整地址提取末段推流码",
			in:      Target{RTMPURL: "rtmp://push-rtmp-l1.douyincdn.com/third/secret"},
			wantURL: "rtmp://push-rtmp-l1.douyincdn.com/third",
			wantKey: "secret",
		},
		{
			name:    "显式推流码时保留多级app",
			in:      Target{RTMPURL: "rtmp://live.example.com/live/app", Key: "secret"},
			wantURL: "rtmp://live.example.com/live/app",
			wantKey: "secret",
		},
		{
			name:    "显式端口",
			in:      Target{RTMPURL: "rtmp://127.0.0.1:19350/live", Key: "secret"},
			wantURL: "rtmp://127.0.0.1:19350/live",
			wantKey: "secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTarget(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.RTMPURL != tt.wantURL || got.Key != tt.wantKey {
				t.Fatalf("got url=%q key=%q", got.RTMPURL, got.Key)
			}
		})
	}
}

func TestNormalizeTargetRejectsInvalidInputWithoutLeakingKey(t *testing.T) {
	tests := []struct {
		name string
		in   Target
	}{
		{name: "空地址", in: Target{Key: "secret"}},
		{name: "非rtmp协议", in: Target{RTMPURL: "rtmps://live.example.com/live", Key: "secret"}},
		{name: "空域名", in: Target{RTMPURL: "rtmp:///live", Key: "secret"}},
		{name: "空app", in: Target{RTMPURL: "rtmp://live.example.com", Key: "secret"}},
		{name: "空推流码", in: Target{RTMPURL: "rtmp://live.example.com/live"}},
		{name: "推流码空白", in: Target{RTMPURL: "rtmp://live.example.com/live", Key: " secret "}},
		{name: "非法端口", in: Target{RTMPURL: "rtmp://live.example.com:99999/live", Key: "secret"}},
		{name: "userinfo", in: Target{RTMPURL: "rtmp://user:secret@live.example.com/live", Key: "secret"}},
		{name: "query", in: Target{RTMPURL: "rtmp://live.example.com/live?key=secret", Key: "secret"}},
		{name: "fragment", in: Target{RTMPURL: "rtmp://live.example.com/live#secret", Key: "secret"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeTarget(tt.in)
			if ErrorCode(err) != "publish_target_invalid" {
				t.Fatalf("error=%v code=%q", err, ErrorCode(err))
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("错误泄露推流码: %v", err)
			}
		})
	}
}

func TestClassifyTarget(t *testing.T) {
	tests := []struct {
		host string
		want TargetVerification
	}{
		{host: "push-rtmp-l1.douyincdn.com", want: TargetDouyin},
		{host: "PUSH-RTMP-L2.DOUYINCDN.COM", want: TargetDouyin},
		{host: "douyincdn.com", want: TargetUnknown},
		{host: "live.douyincdn.com", want: TargetUnknown},
		{host: "push-rtmp-l1.douyincdn.com.example.com", want: TargetUnknown},
		{host: "localhost", want: TargetLocal},
		{host: "127.0.0.1", want: TargetLocal},
		{host: "::1", want: TargetLocal},
		{host: "192.168.1.10", want: TargetLocal},
		{host: "10.0.0.2", want: TargetLocal},
		{host: "172.16.1.1", want: TargetLocal},
		{host: "172.32.0.1", want: TargetUnknown},
		{host: "example.com", want: TargetUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			raw := "rtmp://" + tt.host + "/live"
			if tt.host == "::1" {
				raw = "rtmp://[::1]/live"
			}
			if got := classifyTarget(raw); got != tt.want {
				t.Fatalf("got=%s want=%s", got, tt.want)
			}
		})
	}
}

func TestClassifyNetworkError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "DNS", err: &net.DNSError{Err: "no such host", IsNotFound: true}, want: "publish_dns_failed"},
		{name: "超时", err: context.DeadlineExceeded, want: "publish_network_timeout"},
		{name: "端口拒绝", err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, want: "publish_port_unreachable"},
		{name: "其他网络", err: errors.New("network unreachable"), want: "publish_network_unreachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ErrorCode(classifyNetworkError(tt.err)); got != tt.want {
				t.Fatalf("code=%q want=%q err=%v", got, tt.want, tt.err)
			}
		})
	}
}

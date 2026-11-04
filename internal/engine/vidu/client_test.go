package vidu

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, base string, tweak func(*Options)) *Client {
	t.Helper()
	opt := Options{BaseURL: base, APIKey: "vda_test", RetryDelay: 10 * time.Millisecond}
	if tweak != nil {
		tweak(&opt)
	}
	return NewClient(opt)
}

func TestCreateSessionTimeoutMapsToCreateTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte(`{"live_id":"x"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, func(o *Options) { o.CreateTimeout = 30 * time.Millisecond })
	_, err := c.CreateSession(context.Background(), map[string]any{"avatar_id": "a"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeCreateTimeout || !apiErr.Timeout {
		t.Fatalf("err=%v want %s", err, CodeCreateTimeout)
	}
}

// 带 action 建场遇 403：客户端只分类上报 ActionForbidden，不做静默重试
// （降级重建职责在引擎层 controller.go，G3-2）。
func TestCreateSession403ReportsActionForbidden(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"ACTION_FORBIDDEN","message":"action not allowed"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	_, err := c.CreateSession(context.Background(), map[string]any{"action": "wave", "avatar_id": "a"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeActionForbidden || !apiErr.ActionForbidden {
		t.Fatalf("err=%v want %s+ActionForbidden", err, CodeActionForbidden)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1 (客户端不得静默重试)", calls)
	}
	// 不带 action 的 403 归普通 CodeForbidden。
	_, err = c.CreateSession(context.Background(), map[string]any{"avatar_id": "a"})
	if !errors.As(err, &apiErr) || apiErr.Code != CodeForbidden || apiErr.ActionForbidden {
		t.Fatalf("err=%v want %s", err, CodeForbidden)
	}
}

func TestUploadPUtfailureReportsOrphan(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"up-9","put_url":"` + srv.URL + `"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest) // PUT 对象存储失败
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	_, err := c.Upload(context.Background(), []byte("pcm"), "audio/wav")
	if err == nil || !strings.Contains(err.Error(), "orphan upload id=up-9") {
		t.Fatalf("err=%v want orphan id recorded", err)
	}
}

func TestUploadFinishEmptyURIReturnsError(t *testing.T) {
	var putSrv *httptest.Server
	putSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("etag", `"abc"`)
	}))
	defer putSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"up-1","put_url":"` + putSrv.URL + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`)) // finish 无 uri
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	_, err := c.Upload(context.Background(), []byte("pcm"), "audio/wav")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeUploadNoURI {
		t.Fatalf("err=%v want %s", err, CodeUploadNoURI)
	}
}

func TestCreateSessionDoesNotRetryPOST(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"bad gateway"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	_, err := c.CreateSession(context.Background(), map[string]any{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeUpstreamError {
		t.Fatalf("err=%v want %s", err, CodeUpstreamError)
	}
	if calls != 1 {
		t.Fatalf("POST calls=%d want 1 (create must not retry)", calls)
	}
}

func TestGetLiveRetriesOnceOn5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"oops"}`))
			return
		}
		_, _ = w.Write([]byte(`{"live_id":"live-1","status":"live"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	out, err := c.GetLive(context.Background(), "live-1")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("GET calls=%d want 2 (one retry)", calls)
	}
	if out["live_id"] != "live-1" {
		t.Fatalf("out=%v", out)
	}
}

func TestErrorCodeMapping(t *testing.T) {
	cases := []struct {
		status int
		header string
		want   string
		retry  time.Duration
	}{
		{http.StatusUnauthorized, "", CodeAuthFailed, 0},
		{http.StatusPaymentRequired, "", CodeInsufficient, 0},
		{http.StatusForbidden, "", CodeForbidden, 0},
		{http.StatusNotFound, "", CodeLiveNotFound, 0},
		{http.StatusGone, "", CodeLiveNotFound, 0},
		{http.StatusTooManyRequests, "2", CodeRateLimited, 2 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.header != "" {
					w.Header().Set("Retry-After", c.header)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(`{"code":"X","message":"m"}`))
			}))
			defer srv.Close()

			cl := newTestClient(t, srv.URL, nil)
			_, err := cl.GetLive(context.Background(), "id")
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err=%v not APIError", err)
			}
			if apiErr.Code != c.want {
				t.Fatalf("code=%s want %s", apiErr.Code, c.want)
			}
			if c.retry > 0 && apiErr.RetryAfter != c.retry {
				t.Fatalf("retryAfter=%v want %v", apiErr.RetryAfter, c.retry)
			}
		})
	}
}

func TestCreateSessionCreditInsufficientMapsToInsufficientCredits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":400,"reason":"CreditInsufficient","message":"insufficient credits","metadata":{"trace_id":"trace-credit-1"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	_, err := c.CreateSession(context.Background(), map[string]any{"managed_rtc": map[string]any{}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInsufficient {
		t.Fatalf("err=%v want %s", err, CodeInsufficient)
	}
	if apiErr.UpstreamCode != "CreditInsufficient" {
		t.Fatalf("upstream code=%q want CreditInsufficient", apiErr.UpstreamCode)
	}
	if !strings.Contains(err.Error(), "trace-credit-1") {
		t.Fatalf("err=%v want upstream trace_id", err)
	}
}

func TestBadRequestKeepsUpstreamBodyPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"INVALID_PARAM","message":"avatar not found"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, nil)
	_, err := c.CreateSession(context.Background(), map[string]any{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeBadRequest {
		t.Fatalf("err=%v", err)
	}
	if apiErr.UpstreamCode != "INVALID_PARAM" || !strings.Contains(apiErr.UpstreamMessage, "avatar not found") {
		t.Fatalf("upstream=%+v", apiErr)
	}
}

func TestUploadThreeStepsAndNoAuthOnPUT(t *testing.T) {
	var putAuth string
	var putCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == PathUploads:
			_, _ = w.Write([]byte(`{"id":"f1","put_url":"` + "http://" + r.Host + `/obj/f1"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/obj/f1":
			putAuth = r.Header.Get("Authorization")
			atomic.AddInt32(&putCalls, 1)
			w.Header().Set("etag", `"abc123"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/finish"):
			_, _ = w.Write([]byte(`{"uri":"ssupload:abc"}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	uri, err := c.Upload(context.Background(), []byte("img"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "ssupload:abc" {
		t.Fatalf("uri=%s", uri)
	}
	if putCalls != 1 || putAuth != "" {
		t.Fatalf("putCalls=%d auth=%q; step2 must not send Authorization", putCalls, putAuth)
	}
}

func TestUploadMissingETagFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"f1","put_url":"http://` + r.Host + `/obj/f1"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/obj/f1":
			w.WriteHeader(http.StatusOK) // 故意不带 etag
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL, nil)
	_, err := c.Upload(context.Background(), []byte("img"), "image/png")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeUploadETagMissing {
		t.Fatalf("err=%v want %s", err, CodeUploadETagMissing)
	}
}

// 把契约里的真实超时固定下来：建场 120s、GET 8s、录制 15s、上传 60/300/60s。
func TestClientDefaultsMatchBlueprint(t *testing.T) {
	c := NewClient(Options{})
	if c.opt.BaseURL != "https://api.vidu.cn" {
		t.Fatalf("base url=%q", c.opt.BaseURL)
	}
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"create", c.opt.CreateTimeout, 120 * time.Second},
		{"live", c.opt.LiveTimeout, 8 * time.Second},
		{"recording", c.opt.RecordingTimeout, 15 * time.Second},
		{"list", c.opt.ListTimeout, 8 * time.Second},
		{"credits", c.opt.CreditsTimeout, 8 * time.Second},
		{"upload_start", c.opt.UploadStartTimeout, 60 * time.Second},
		{"upload_put", c.opt.UploadPUTTimeout, 300 * time.Second},
		{"upload_finish", c.opt.UploadFinishTimeout, 60 * time.Second},
		{"avatar_list", c.opt.AvatarListTimeout, 8 * time.Second},
		{"avatar_create", c.opt.AvatarCreateTimeout, 300 * time.Second},
		{"avatar_delete", c.opt.AvatarDeleteTimeout, 30 * time.Second},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s timeout=%v want %v", tc.name, tc.got, tc.want)
		}
	}
}

// ---------- avatars 组契约测（M3；路径/方法/重试语义固定，字段形状待真机验证） ----------

func TestAvatarsEndpointsPathsAndMethods(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		_, _ = w.Write([]byte(`{"avatars":[],"id":"va-1"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	if _, err := c.ListAvatars(context.Background(), 2, 20); err != nil {
		t.Fatal(err)
	}
	out, err := c.CreateAvatar(context.Background(), map[string]any{"name": "小雅", "image_uri": "ssupload:x"})
	if err != nil {
		t.Fatal(err)
	}
	if out["id"] != "va-1" {
		t.Fatalf("create out=%v", out)
	}
	if err := c.DeleteAvatar(context.Background(), "va-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /live/v1/avatars?pager.page=2&pager.pagesz=20",
		"POST /live/v1/avatars?",
		"DELETE /live/v1/avatars/va-1?",
	}
	if len(got) != len(want) {
		t.Fatalf("calls=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call[%d]=%q want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

func TestCreateAvatarNeverRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"bad gateway"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	_, err := c.CreateAvatar(context.Background(), map[string]any{"name": "x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeUpstreamError {
		t.Fatalf("err=%v want %s", err, CodeUpstreamError)
	}
	if calls != 1 {
		t.Fatalf("POST calls=%d want 1 (create_avatar 不重试)", calls)
	}
}

func TestListAvatarsRetriesOnceOn5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"avatars":[]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	if _, err := c.ListAvatars(context.Background(), 1, 20); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("GET calls=%d want 2 (one retry)", calls)
	}
}

// DeleteAvatar 幂等，5xx 重试 1 次；404 归入 CodeLiveNotFound（孤儿清理据此判定"已不在"）。
func TestDeleteAvatarRetryAndNotFound(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NOT_FOUND","message":"gone"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	err := c.DeleteAvatar(context.Background(), "va-9")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeLiveNotFound {
		t.Fatalf("err=%v want %s", err, CodeLiveNotFound)
	}
	if calls != 2 {
		t.Fatalf("DELETE calls=%d want 2 (one retry)", calls)
	}
}

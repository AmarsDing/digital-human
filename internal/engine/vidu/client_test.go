package vidu

import (
	"context"
	"encoding/json"
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

func TestCreateSession403ActionDegradesOnce(t *testing.T) {
	var withAction, withoutAction int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["action"]; ok {
			atomic.AddInt32(&withAction, 1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"ACTION_FORBIDDEN","message":"action not allowed"}`))
			return
		}
		atomic.AddInt32(&withoutAction, 1)
		_, _ = w.Write([]byte(`{"live_id":"live-1"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	// 动作库降级：带 action 建场遇 403，客户端去 action 重建一次并成功。
	out, err := c.CreateSession(context.Background(), map[string]any{"action": "wave", "avatar_id": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if out["live_id"] != "live-1" {
		t.Fatalf("out=%v", out)
	}
	if withAction != 1 || withoutAction != 1 {
		t.Fatalf("withAction=%d withoutAction=%d (降级必须恰好一次)", withAction, withoutAction)
	}
	// 原本就不带 action 的请求不受影响。
	if _, err = c.CreateSession(context.Background(), map[string]any{"avatar_id": "a"}); err != nil {
		t.Fatal(err)
	}
	if withAction != 1 || withoutAction != 2 {
		t.Fatalf("withAction=%d withoutAction=%d", withAction, withoutAction)
	}
}

// 降级后仍失败（403 与 action 无关）时，错误必须如实上抛，不得无限重建。
func TestCreateSession403DegradeStillFails(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"ACTION_FORBIDDEN","message":"action not allowed"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	_, err := c.CreateSession(context.Background(), map[string]any{"action": "wave"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeForbidden {
		t.Fatalf("err=%v want %s (降级一次后仍以 403 上抛)", err, CodeForbidden)
	}
	if calls != 2 {
		t.Fatalf("calls=%d want 2 (首次 + 降级一次)", calls)
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
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s timeout=%v want %v", tc.name, tc.got, tc.want)
		}
	}
}

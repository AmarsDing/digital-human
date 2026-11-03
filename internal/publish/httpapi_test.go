package publish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAPI(t *testing.T) {
	p := NewPublisher(Config{}, &MemTargetStore{})
	a := NewHTTPAPI(p)
	mux := http.NewServeMux()
	a.Register(mux)

	// status: 初始 idle
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stream/status", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"idle"`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}

	// target：保存返回指纹，响应不含 key
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/stream/target",
		strings.NewReader(`{"platform":"douyin","rtmp_url":"rtmp://127.0.0.1:1/live","stream_key":"sk_live_SUPERSECRET_9f8e7d"}`)))
	if rec.Code != 200 {
		t.Fatalf("target: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if fp, _ := resp["key_fingerprint8"].(string); len(fp) != 8 {
		t.Fatalf("fingerprint: %v", resp)
	}
	if strings.Contains(rec.Body.String(), "SUPERSECRET") {
		t.Fatal("响应泄露 stream key")
	}

	// start：RTMP 连接会失败（127.0.0.1:1 拒绝），但 Start 本身应受理（异步重连）
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/stream/start",
		strings.NewReader(`{"idempotency_key":"k1"}`)))
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	// 幂等重放
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/stream/start",
		strings.NewReader(`{"idempotency_key":"k1"}`)))
	if !strings.Contains(rec.Body.String(), "duplicate_ignored") {
		t.Fatalf("幂等重放未识别: %s", rec.Body.String())
	}

	// stop 幂等
	for i := 0; i < 2; i++ {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/stream/stop",
			strings.NewReader(`{"idempotency_key":"s1"}`)))
		if rec.Code != 200 {
			t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
		}
	}
	p.Wait()

	// 方法检查
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stream/start", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

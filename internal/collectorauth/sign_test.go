package collectorauth

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	body := []byte(`{"kind":"chat","text":"hello"}`)
	ts := time.Now().Unix()
	nonce := "n-1"
	sign := Sign("s3cret", "POST", "/api/danmaku", body, ts, nonce)
	if !Verify("s3cret", "POST", "/api/danmaku", body, i64s(ts), nonce, sign, time.Now()) {
		t.Fatal("roundtrip failed")
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	body := []byte(`{"a":1}`)
	ts := time.Now().Unix()
	sign := Sign("k", "POST", "/api/danmaku", body, ts, "n")
	cases := []struct {
		name                 string
		secret, method, path string
		body                 []byte
		ts, nonce, sign      string
	}{
		{"body tampered", "k", "POST", "/api/danmaku", []byte(`{"a":2}`), i64s(ts), "n", sign},
		{"wrong secret", "k2", "POST", "/api/danmaku", body, i64s(ts), "n", sign},
		{"wrong path", "k", "POST", "/api/v1/danmaku", body, i64s(ts), "n", sign},
		{"wrong method", "k", "PUT", "/api/danmaku", body, i64s(ts), "n", sign},
		{"stale ts", "k", "POST", "/api/danmaku", body, i64s(ts - 301), "n", Sign("k", "POST", "/api/danmaku", body, ts-301, "n")},
		{"empty nonce", "k", "POST", "/api/danmaku", body, i64s(ts), "", sign},
	}
	for _, c := range cases {
		if Verify(c.secret, c.method, c.path, c.body, c.ts, c.nonce, c.sign, time.Now()) {
			t.Fatalf("%s: want reject", c.name)
		}
	}
}

func i64s(v int64) string { return strconv.FormatInt(v, 10) }

func TestNonceRandomHex(t *testing.T) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	if len(hex.EncodeToString(b[:])) != 32 {
		t.Fatal("want 32 hex chars")
	}
}

// Package collectorauth 实现采集器 HMAC 签名/验签（与 server 端 checkCollectorHMAC
// 同一口径：HMAC_SHA256(secret, method\npath\nhex(sha256(body))\nts\nnonce)，
// 时间窗 300s）。供 dh-relay（签名）与采集器心跳处理器（验签）使用。
package collectorauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Window 是签名时间窗（秒），与服务端约定一致。
const Window = 300

// Header 名称常量。
const (
	HeaderTs    = "X-Dh-Ts"
	HeaderNonce = "X-Dh-Nonce"
	HeaderSign  = "X-Dh-Sign"
)

// Sign 计算签名串。ts 为 Unix 秒；nonce 由调用方保证唯一。
func Sign(secret, method, path string, body []byte, ts int64, nonce string) string {
	sum := sha256.Sum256(body)
	msg := strings.Join([]string{method, path, hex.EncodeToString(sum[:]),
		strconv.FormatInt(ts, 10), nonce}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify 校验签名：时间窗 + HMAC 常量时间比较。nonce 去重由调用方负责。
func Verify(secret, method, path string, body []byte, ts, nonce, sign string, now time.Time) bool {
	if secret == "" || ts == "" || nonce == "" || sign == "" {
		return false
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	d := now.Unix() - n
	if d < 0 {
		d = -d
	}
	if d > Window {
		return false
	}
	want := Sign(secret, method, path, body, n, nonce)
	return hmac.Equal([]byte(want), []byte(strings.ToLower(sign)))
}

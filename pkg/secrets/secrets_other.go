//go:build !windows

package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
)

// 非 Windows 环境（CI/Linux）没有 DPAPI，退化为 AES-256-GCM；
// 密钥必须由 DH_SECRET_KEY 提供（32 字节 hex），否则拒绝启动加密路径。
func key() ([]byte, error) {
	raw := os.Getenv("DH_SECRET_KEY")
	if len(raw) != 64 {
		return nil, errors.New("非 Windows 环境需要 DH_SECRET_KEY（64 位 hex）")
	}
	_, err := hex.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(raw))
	return sum[:], nil
}

func Encrypt(plain []byte) ([]byte, error) {
	k, err := key()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

func Decrypt(cipherText []byte) ([]byte, error) {
	k, err := key()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(cipherText) < gcm.NonceSize() {
		return nil, errors.New("密文过短")
	}
	return gcm.Open(nil, cipherText[:gcm.NonceSize()], cipherText[gcm.NonceSize():], nil)
}

func Available() bool {
	raw := os.Getenv("DH_SECRET_KEY")
	return len(raw) == 64
}

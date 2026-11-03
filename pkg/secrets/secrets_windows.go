//go:build windows

package secrets

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	crypt32            = syscall.NewLazyDLL("crypt32.dll")
	procCryptProtect   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotect = crypt32.NewProc("CryptUnprotectData")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree      = kernel32.NewProc("LocalFree")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(d []byte) *dataBlob {
	if len(d) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{cbData: uint32(len(d)), pbData: &d[0]}
}

func (b *dataBlob) bytes() []byte {
	d := make([]byte, b.cbData)
	copy(d, (*[1 << 30]byte)(unsafe.Pointer(b.pbData))[:b.cbData])
	return d
}

func (b *dataBlob) free() {
	if b.pbData != nil {
		_, _, _ = procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
	}
}

// Encrypt 使用当前用户 DPAPI（CryptProtectData），密文只能同一 Windows 用户解开。
func Encrypt(plain []byte) ([]byte, error) {
	in := newBlob(plain)
	var out dataBlob
	ret, _, err := procCryptProtect.Call(
		uintptr(unsafe.Pointer(in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if ret == 0 {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer out.free()
	return out.bytes(), nil
}

// Decrypt 解开 DPAPI 密文。
func Decrypt(cipher []byte) ([]byte, error) {
	in := newBlob(cipher)
	var out dataBlob
	ret, _, err := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if ret == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer out.free()
	return out.bytes(), nil
}

// Available 表示本机具备系统级密钥保护。
func Available() bool { return true }

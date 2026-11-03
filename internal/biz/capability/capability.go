// Package capability 实现深化方案 v2 §2.2 能力矩阵状态词与 §2.3 三条链路硬前置。
//
// 状态词固定为：real（真实实现且本次探测通过）、fake（显式开发实现）、
// unconfigured（缺配置/依赖）、unknown（实现或端到端结果未证实）。
// 配置存在但未探测只能算 unknown；unknown 绝不默认 pass（§7.5）。
package capability

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Status 能力状态词（§2.2 固定四值）。
type Status string

const (
	Real         Status = "real"
	Fake         Status = "fake"
	Unconfigured Status = "unconfigured"
	Unknown      Status = "unknown"
)

// ErrNotReal 硬前置不满足：目标能力不是 real（§2.3 杜绝假 ready）。
var ErrNotReal = errors.New("capability_not_real")

// Matrix 能力名 → 状态词；由装配点显式计算（wire.go），禁止按配置存在就报 real。
type Matrix map[string]Status

// Get 取能力状态；未登记的能力按 unknown（绝不默认 real）。
func (m Matrix) Get(name string) Status {
	if m == nil {
		return Unknown
	}
	if s, ok := m[name]; ok {
		return s
	}
	return Unknown
}

// NotRealError 携带链路与能力明细的拒绝原因，UI 可直接展示（含 fake 标注）。
type NotRealError struct {
	Link   string   // 链路名：content_pack / dual_avatar / song_request / public_live
	Detail []string // 如 "action_sender=fake"
}

func (e *NotRealError) Error() string {
	sort.Strings(e.Detail)
	return fmt.Sprintf("%s: %v: %s", ErrNotReal, e.Link, strings.Join(e.Detail, ","))
}

func (e *NotRealError) Unwrap() error { return ErrNotReal }

// RequireReal §2.3 硬前置：link 链路要求的全部能力必须为 real；
// 任一非 real 显式拒绝并逐条标注状态词（fake 可见），不静默放行。
func (m Matrix) RequireReal(link string, names ...string) error {
	var bad []string
	for _, n := range names {
		if s := m.Get(n); s != Real {
			bad = append(bad, n+"="+string(s))
		}
	}
	if len(bad) > 0 {
		return &NotRealError{Link: link, Detail: bad}
	}
	return nil
}

// Blocking 预检阻断判定：fail 语义的非 real 状态（unknown 同样阻断，绝不默认 pass）。
func Blocking(s Status) bool { return s != Real }

package publish

// 开播前置检查：StartLive 前校验推流状态，未推上则拒绝开播并给出可操作原因。
// 接线（在 live.StartLive 前调用 CheckStartLive）留给主实施线，
// 见 temp/digital-human/plan/dz-integration-notes.md。

import (
	"errors"
	"fmt"
)

// ErrPushRequired 表示推流未建立导致开播被拒。
var ErrPushRequired = errors.New("publish: 推流未建立，拒绝开播")

// PushGate 是检查所需的最小依赖（由 Publisher 或 fake 实现）。
type PushGate interface {
	Status() Status
}

// CheckStartLive 前置检查。allowUnpushed 为"允许无推流本地预览"显式开关
// （默认 false；仅本地调试时由配置显式打开）。
// 返回 nil 表示允许开播；否则错误信息为可操作原因。
func CheckStartLive(g PushGate, allowUnpushed bool) error {
	if g == nil {
		if allowUnpushed {
			return nil
		}
		return fmt.Errorf("%w: 推流模块未装配（检查 internal/publish 接线）", ErrPushRequired)
	}
	st := g.Status()
	if allowUnpushed && st.State != StatePushing {
		return nil
	}
	switch st.State {
	case StatePushing:
		if st.TargetVerification != TargetDouyin {
			return &Error{Code: "publish_target_unverified", Message: "需先完成一次真实抖音推流验证"}
		}
		return nil
	case StateRejected:
		return fmt.Errorf("%w: 推流被平台拒绝（stream key 可能已失效），请在平台接入面板更新推流凭据", ErrPushRequired)
	case StateDegraded:
		return fmt.Errorf("%w: 推流断线重连中（%s），请等待恢复或检查网络", ErrPushRequired, st.LastError)
	case StateStarting:
		return fmt.Errorf("%w: 推流正在建立连接，请稍后重试", ErrPushRequired)
	default: // idle / stopped
		return fmt.Errorf("%w: 当前状态 %s，请先在平台接入面板点击「开始推流」", ErrPushRequired, st.State)
	}
}

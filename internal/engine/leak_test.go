package engine

import (
	"testing"

	"go.uber.org/goleak"
)

// F06：引擎关键生命周期 goroutine 泄漏红线——包级兜底。
// 任何 Start/Say/回收路径泄漏的 goroutine 都会在包测试结束时失败。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

package action

import (
	"testing"

	"go.uber.org/goleak"
)

// F06：动作调度关键生命周期 goroutine 泄漏红线——包级兜底。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

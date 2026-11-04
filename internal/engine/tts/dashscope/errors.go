package dashscope

import (
	"errors"
	"fmt"
	"strings"
)

// ServerError 是服务端 error 事件，保留真实 code/message 供 smoke 分类。
type ServerError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ServerError) Error() string {
	if e == nil {
		return "dashscope: unknown server error"
	}
	return fmt.Sprintf("dashscope: server error code=%s message=%s", e.Code, e.Message)
}

// FailureClass 区分换配置可解决的模型可用性问题与需要排查代码的协议问题。
type FailureClass string

const (
	FailureModelUnavailable FailureClass = "model_unavailable"
	FailureProtocol         FailureClass = "protocol"
)

func ClassifyFailure(err error) FailureClass {
	var serverErr *ServerError
	if errors.As(err, &serverErr) && serverErr != nil && serverErr.modelUnavailable() {
		return FailureModelUnavailable
	}
	return FailureProtocol
}

func (e *ServerError) modelUnavailable() bool {
	code := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(e.Code))
	switch code {
	case "modelnotenabled", "modelnotfound", "modelnotsupported", "modelunavailable":
		return true
	}
	message := strings.ToLower(e.Message)
	if !strings.Contains(message, "model") {
		return false
	}
	for _, reason := range []string{"not enabled", "not enable", "not supported", "not exist", "not found", "unavailable", "no access", "not authorized", "permission"} {
		if strings.Contains(message, reason) {
			return true
		}
	}
	return false
}

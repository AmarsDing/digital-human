package refs

import "context"

// PromptOperator 是 Vidu type11 prompt_operation 出口的最小面，
// 由 engine.ViduController（生产装配）或 *vidu.Signaling（契约测试）实现。
type PromptOperator interface {
	PromptOperation(ctx context.Context, opType string, images []map[string]any) error
}

// ViduSignaler G3-1：refs.SetPrompt 的真实生产桥——op（switch/remove）与图片集合
// 如实透传到 Vidu type11 prompt_operation，错误原样上抛（type14 回显由 Service 处理）。
type ViduSignaler struct{ op PromptOperator }

// NewViduSignaler 装配生产信令出口；op=nil 表示引擎侧无 type11 出口
// （如 real 模式凭据缺失的 UnavailableController），SetPrompt 如实返回 ErrNoSignal，
// 绝不静默成功（开播时序走 refs_push_failed 降级）。
func NewViduSignaler(op PromptOperator) *ViduSignaler { return &ViduSignaler{op: op} }

func (v *ViduSignaler) SetPrompt(ctx context.Context, op string, images []Image) error {
	if v == nil || v.op == nil {
		return ErrNoSignal
	}
	imgs := make([]map[string]any, 0, len(images))
	for _, im := range images {
		m := map[string]any{}
		if im.ImageID != "" {
			m["image_id"] = im.ImageID
		}
		if im.URL != "" {
			m["url"] = im.URL
		}
		imgs = append(imgs, m)
	}
	return v.op.PromptOperation(ctx, op, imgs)
}

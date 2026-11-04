package voice

import (
	"context"
	"errors"
	"sync"

	"github.com/teamwork/digital-human/internal/biz/capability"
)

// FakeRecognizer 是显式 dev 实现；文本由配置/测试给定，绝不伪装真实识别。
type FakeRecognizer struct{ Text string }

func NewFakeRecognizer(text string) *FakeRecognizer { return &FakeRecognizer{Text: text} }

func (f *FakeRecognizer) Capability(context.Context) Capability {
	return Capability{Source: capability.Fake, Ready: true, Device: "fake", Model: "explicit-dev"}
}

func (f *FakeRecognizer) Start(ctx context.Context, onPartial func(string)) (Recognition, error) {
	return &fakeRecognition{ctx: ctx, text: f.Text, onPartial: onPartial}, nil
}

type fakeRecognition struct {
	ctx       context.Context
	text      string
	onPartial func(string)
	once      sync.Once
}

func (f *fakeRecognition) WritePCM(ctx context.Context, _ []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.once.Do(func() {
		if f.text != "" {
			f.onPartial(f.text)
		}
	})
	return nil
}

func (f *fakeRecognition) Finalize(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return Result{Text: f.text}, nil
}
func (f *fakeRecognition) Close() error { return nil }

// UnavailableRecognizer 用于缺模型、缺依赖或加载失败的 fail-closed 装配。
type UnavailableRecognizer struct {
	Reason  string
	Message string
}

func NewUnavailableRecognizer(reason string) *UnavailableRecognizer {
	return &UnavailableRecognizer{Reason: reason, Message: reason}
}

func (r *UnavailableRecognizer) Capability(context.Context) Capability {
	return Capability{Source: capability.Unconfigured, Ready: false, ErrorCode: "asr_unavailable", Message: r.Message}
}
func (r *UnavailableRecognizer) Start(context.Context, func(string)) (Recognition, error) {
	return nil, errors.Join(ErrASRUnavailable, errors.New(r.Reason))
}

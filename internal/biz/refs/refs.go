// Package refs 实现参考图（refs）切换/移除与 type14 错误回显。
//
// 蓝本事实：bridge.py `/api/prompt` 透传 engine.prompt_op("switch"/"remove")；
// type14 是 Vidu 的 prompt 操作失败回执，engine 只记录 ack、不自动重试。
// 这里用 Signaler 端口隔离信令，FakeSignaler 可注入失败以验证回显与“不重发”。
package refs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

var (
	ErrTooManyImages = errors.New("参考图最多 3 张")
	ErrNoSignal      = errors.New("信令通道未就绪")
)

type Image struct {
	ImageID string `json:"image_id"`
	URL     string `json:"url"`
	Kind    string `json:"kind,omitempty"`
}

type Signaler interface {
	SetPrompt(ctx context.Context, op string, images []Image) error
}

type Call struct {
	Op     string
	Images []Image
	AtMs   int64
}

type FakeSignaler struct {
	mu    sync.Mutex
	calls []Call
	fail  error
}

func NewFakeSignaler() *FakeSignaler { return &FakeSignaler{} }

func (f *FakeSignaler) SetPrompt(_ context.Context, op string, images []Image) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Op: op, Images: append([]Image(nil), images...), AtMs: time.Now().UnixMilli()})
	if f.fail != nil {
		return f.fail
	}
	return nil
}

func (f *FakeSignaler) SetFail(err error) {
	f.mu.Lock()
	f.fail = err
	f.mu.Unlock()
}

func (f *FakeSignaler) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

type Ack struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	AtMs    int64  `json:"at_ms"`
}

type State struct {
	Images     []Image `json:"images"`
	LastOp     string  `json:"last_op,omitempty"`
	LastOpAtMs int64   `json:"last_op_at_ms,omitempty"`
	Ack        *Ack    `json:"ack,omitempty"`
	Source     string  `json:"source"`
}

type Service struct {
	mu    sync.Mutex
	state State
	sig   Signaler
	bus   *eventbus.Bus
	now   func() time.Time
}

func NewService(sig Signaler, bus *eventbus.Bus) *Service {
	// source 由真实装配决定：FakeSignaler → fake，真实信令通道 → real。
	src := "real"
	if _, ok := sig.(*FakeSignaler); ok || sig == nil {
		src = "fake"
	}
	return &Service{sig: sig, bus: bus, now: time.Now, state: State{Images: []Image{}, Source: src}}
}

func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

func (s *Service) snapshot() State {
	out := s.state
	out.Images = append([]Image(nil), s.state.Images...)
	if s.state.Ack != nil {
		ack := *s.state.Ack
		out.Ack = &ack
	}
	return out
}

// Switch 用给定图片集合替换当前参考图（去重后最多 3 张）；失败时记录 type14 回显，不重发。
func (s *Service) Switch(ctx context.Context, images []Image) (State, error) {
	uniq := make([]Image, 0, len(images))
	seen := map[string]bool{}
	for _, im := range images {
		id := strings.TrimSpace(im.ImageID)
		url := strings.TrimSpace(im.URL)
		if id == "" && url == "" {
			continue
		}
		key := id
		if key == "" {
			key = url
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		im.ImageID, im.URL = id, url
		uniq = append(uniq, im)
	}
	if len(uniq) > 3 {
		return s.State(), ErrTooManyImages
	}
	if len(uniq) == 0 {
		return s.Remove(ctx)
	}
	return s.send(ctx, "switch", uniq)
}

// Remove 移除全部参考图。
func (s *Service) Remove(ctx context.Context) (State, error) {
	return s.send(ctx, "remove", []Image{})
}

func (s *Service) send(ctx context.Context, op string, images []Image) (State, error) {
	if s.sig == nil {
		return s.State(), ErrNoSignal
	}
	err := s.sig.SetPrompt(ctx, op, images)
	s.mu.Lock()
	s.state.LastOp = op
	s.state.LastOpAtMs = s.now().UnixMilli()
	if err != nil {
		s.state.Ack = &Ack{Success: false, Error: err.Error(), AtMs: s.now().UnixMilli()}
	} else {
		s.state.Images = append([]Image(nil), images...)
		s.state.Ack = &Ack{Success: true, AtMs: s.now().UnixMilli()}
	}
	out := s.snapshot()
	s.mu.Unlock()
	if err != nil {
		// type14 失败回执：事件里显式带 type=14，供前端错误条展示。
		s.publish(ctx, "refs.error", map[string]any{"type": 14, "op": op, "ok": false, "error": err.Error()})
		return out, err
	}
	s.publish(ctx, "refs.mounted", map[string]any{"type": 14, "op": op, "ok": true, "images": images})
	return out, nil
}

// NoteType14 供信令层把异步 type14 错误交回面板（不改变已挂载图片）。
func (s *Service) NoteType14(ctx context.Context, errMsg string) {
	s.mu.Lock()
	s.state.Ack = &Ack{Success: false, Error: errMsg, AtMs: s.now().UnixMilli()}
	s.mu.Unlock()
	s.publish(ctx, "refs.error", map[string]any{"type": 14, "ok": false, "error": errMsg, "async": true})
}

func (s *Service) publish(ctx context.Context, typ string, payload map[string]any) {
	if s.bus != nil {
		s.bus.PublishScoped(ctx, typ, payload)
	}
}

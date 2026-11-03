// Package refs 实现参考图（refs）切换/移除与 type14 错误回显。
//
// 蓝本事实：bridge.py `/api/prompt` 透传 engine.prompt_op("switch"/"remove")；
// type14 是 Vidu 的 prompt 操作失败回执，engine 只记录 ack、不自动重试。
// 这里用 Signaler 端口隔离信令，FakeSignaler 可注入失败以验证回显与“不重发”。
//
// F33（蓝本 bridge.py:1348 /api/refs）：refs 目录的本地文件列表与 multipart 上传；
// 上传校验顺序对齐 avatar/images.go（≤10MB → 嗅探 png/jpeg → 宽高 ≤4096），webp 拒绝上传
// （列表仍按蓝本收 .webp，便于展示手工放入的文件）。
package refs

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg" // DecodeConfig 需要注册格式
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	// dir F33 本地 refs 文件目录；空串表示文件功能未配置（ListFiles 返回空、SaveUpload 报错）。
	dir string
}

func NewService(sig Signaler, bus *eventbus.Bus) *Service {
	// source 由真实装配决定：FakeSignaler → fake，真实信令通道 → real。
	src := "real"
	if _, ok := sig.(*FakeSignaler); ok || sig == nil {
		src = "fake"
	}
	return &Service{sig: sig, bus: bus, now: time.Now, state: State{Images: []Image{}, Source: src}}
}

// SetDir 配置 F33 本地 refs 目录（装配层注入 storage/refs）。
func (s *Service) SetDir(dir string) {
	s.mu.Lock()
	s.dir = dir
	s.mu.Unlock()
}

func (s *Service) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
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

// ---------- F33 本地 refs 文件（蓝本 bridge.py:1348 /api/refs） ----------

const (
	// MaxRefBytes / MaxRefPixels 对齐 avatar/images.go §5.3 上传约束（同顺序校验管线）。
	MaxRefBytes  = 10 << 20
	MaxRefPixels = 4096
)

var (
	ErrRefTooLarge = errors.New("image_too_large")
	ErrRefType     = errors.New("unsupported_image_type")
	ErrRefPixels   = errors.New("image_pixels_exceeded")
	ErrRefName     = errors.New("invalid_file_name")
	ErrRefNoDir    = errors.New("refs_dir_not_configured")
)

// RefFile 目录列表项（对齐蓝本 {name, size}）。
type RefFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// listExts 列表收蓝本四种扩展名（含 webp——手工放入的可展示）。
var listExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}

// uploadExts 上传只收 png/jpeg（嗅探结果 → 落盘扩展名；与 avatar 管线一致，webp 拒绝）。
var uploadExts = map[string]string{"image/png": ".png", "image/jpeg": ".jpg"}

// ListFiles 扫描 refs 目录（扁平、不递归），按名字排序返回 {name, size}；目录不存在返回空。
func (s *Service) ListFiles() ([]RefFile, error) {
	dir := s.Dir()
	if dir == "" {
		return []RefFile{}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []RefFile{}, nil
		}
		return nil, err
	}
	out := []RefFile{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !listExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, RefFile{Name: e.Name(), Size: info.Size()})
	}
	return out, nil
}

// SaveUpload 保存上传图片：校验顺序与 avatar/images.go §5.3 一致
// （≤10MB → http.DetectContentType 白名单 png/jpeg → DecodeConfig 宽高 ≤4096）→ 安全文件名落盘。
// 同名文件已存在时加时间戳后缀，不覆盖用户文件。
func (s *Service) SaveUpload(filename string, r io.Reader) (RefFile, error) {
	dir := s.Dir()
	if dir == "" {
		return RefFile{}, ErrRefNoDir
	}
	buf, err := io.ReadAll(io.LimitReader(r, MaxRefBytes+1))
	if err != nil {
		return RefFile{}, err
	}
	if len(buf) > MaxRefBytes {
		return RefFile{}, ErrRefTooLarge
	}
	ext, ok := uploadExts[http.DetectContentType(buf)]
	if !ok {
		return RefFile{}, ErrRefType // webp/svg/gif 等拒绝上传
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil {
		return RefFile{}, ErrRefType
	}
	if cfg.Width > MaxRefPixels || cfg.Height > MaxRefPixels {
		return RefFile{}, ErrRefPixels
	}
	name := safeRefName(filename)
	if name == "" {
		name = "ref" + ext
	}
	// 统一扩展名为嗅探结果（防伪造扩展名），再去路径分隔符。
	name = strings.TrimSuffix(name, filepath.Ext(name)) + ext
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return RefFile{}, err
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		// 同名已存在：加时间戳后缀，保留两份（不覆盖用户文件）。
		name = strings.TrimSuffix(name, ext) + "-" + time.Now().Format("20060102150405") + ext
		dst = filepath.Join(dir, name)
	}
	if err := os.WriteFile(dst, buf, 0o644); err != nil {
		return RefFile{}, err
	}
	return RefFile{Name: name, Size: int64(len(buf))}, nil
}

// safeRefName 只保留文件名片段（去目录穿越），拒绝空名与隐藏文件。
func safeRefName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "\x00", "")
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return ""
	}
	return name
}

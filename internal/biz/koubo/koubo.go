// Package koubo 实现口播工程（project.json）与 14 条口播路由。
//
// 蓝本事实：koubo.py 的 project.json 结构、split_text 切分、逐段 TTS/试听/动作、
// 全量合成/动作/连续播放、stop、/api/koubo/audio/{name} 下载；
// 以及“同时只合成一段”的串行锁。TTS/播放均为端口 + fake，真实实现后续接 OmniVoice/Vidu。
package koubo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/pkg/audio"
)

var (
	ErrNotLive      = errors.New("未开播")
	ErrNoSelection  = errors.New("该段还没有选中音频")
	ErrSegmentGone  = errors.New("段落不存在")
	ErrInvalidSplit = errors.New("切分点无效")
)

type Take struct {
	File string  `json:"file"`
	Dur  float64 `json:"dur"`
	Ts   float64 `json:"ts"`
}

type Segment struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Takes    []Take `json:"takes"`
	Selected *int   `json:"selected"`
	Action   string `json:"action"`
	Status   string `json:"status"`
}

type Job struct {
	Running bool    `json:"running"`
	Done    int     `json:"done"`
	Total   int     `json:"total"`
	Idx     *string `json:"idx"`
	Stop    bool    `json:"stop"`
	Error   string  `json:"error,omitempty"`
}

type State struct {
	Text     string    `json:"text"`
	MaxChars int       `json:"max_chars"`
	Segments []Segment `json:"segments"`
	Playing  bool      `json:"playing"`
	PlayIdx  *string   `json:"play_idx"`
	Stop     bool      `json:"stop"`
	Updated  float64   `json:"updated"`
	TTSJob   Job       `json:"tts_job"`
}

func defaultState() State {
	return State{MaxChars: 120, Segments: []Segment{}, Updated: float64(time.Now().UnixMilli()) / 1000}
}

type Store interface {
	Load() (State, error)
	Save(State) error
}

type MemoryStore struct {
	mu sync.Mutex
	st State
}

func NewMemoryStore(st State) *MemoryStore {
	if st.MaxChars <= 0 {
		st = defaultState()
	}
	return &MemoryStore{st: cloneState(st)}
}

func (m *MemoryStore) Load() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneState(m.st), nil
}

func (m *MemoryStore) Save(st State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st = cloneState(st)
	return nil
}

// FileStore 对应蓝本 data/koubo/project.json；原子写避免半截文件。
type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (s *FileStore) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultState(), nil
		}
		return State{}, err
	}
	var st State
	if err := decodeJSON(raw, &st); err != nil {
		return defaultState(), nil
	}
	if st.MaxChars <= 0 {
		st.MaxChars = 120
	}
	if st.Segments == nil {
		st.Segments = []Segment{}
	}
	return st, nil
}

func (s *FileStore) Save(st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := encodeJSON(st)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Synth 返回 24k s16le mono PCM；voice 由音色服务决定，空值表示当前音色。
type Synth interface {
	Synth(ctx context.Context, text, voice string) ([]byte, error)
}

// ActionGen 生成动作描述（蓝本 engine.gen_action，未接 LLM 时由 fake 承接）。
type ActionGen interface {
	Generate(ctx context.Context, text string) (string, error)
}

type FakeActionGen struct{ Fixed string }

func (f FakeActionGen) Generate(context.Context, string) (string, error) {
	if f.Fixed != "" {
		return f.Fixed, nil
	}
	return "微笑并点头", nil
}

type Service struct {
	mu      sync.Mutex
	store   Store
	synth   Synth
	synthMu sync.Mutex // 同时只合成一段（蓝本 KOUBO TTS 串行锁）
	player  media.Player
	actions ActionGen
	dir     string
	isLive  func() bool

	job        Job
	playCancel context.CancelFunc
	now        func() time.Time
	busyPlay   bool
}

func NewService(store Store, synth Synth, player media.Player, gen ActionGen, audioDir string, isLive func() bool) *Service {
	if isLive == nil {
		isLive = func() bool { return true }
	}
	return &Service{store: store, synth: synth, player: player, actions: gen, dir: audioDir, isLive: isLive, now: time.Now}
}

func (s *Service) SetLive(fn func() bool) {
	s.mu.Lock()
	s.isLive = fn
	s.mu.Unlock()
}

func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Service) snapshotLocked() State {
	st, err := s.store.Load()
	if err != nil {
		st = defaultState()
	}
	st.TTSJob = s.job
	return st
}

func (s *Service) saveLocked(st State) {
	st.Updated = float64(s.now().UnixMilli()) / 1000
	st.TTSJob = Job{}
	_ = s.store.Save(st)
}

func (s *Service) Split(text string, maxChars int) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return State{}, err
	}
	if strings.TrimSpace(text) == "" {
		text = st.Text
	}
	if strings.TrimSpace(text) == "" {
		return State{}, errors.New("text 为空")
	}
	if maxChars > 0 {
		st.MaxChars = maxChars
	}
	if st.MaxChars <= 0 {
		st.MaxChars = 120
	}
	parts := SplitText(text, st.MaxChars)
	segs := make([]Segment, 0, len(parts))
	for i, p := range parts {
		segs = append(segs, Segment{ID: fmt.Sprintf("s%d", i+1), Text: p, Takes: []Take{}})
	}
	st.Text = text
	st.Segments = segs
	s.saveLocked(st)
	return st, nil
}

func (s *Service) UpdateSegment(id, text string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return State{}, err
	}
	seg := findSeg(st.Segments, id)
	if seg == nil {
		return State{}, ErrSegmentGone
	}
	if seg.Text != text {
		seg.Text = text
		seg.Takes = []Take{}
		seg.Selected = nil
		seg.Action = ""
		seg.Status = ""
	}
	s.saveLocked(st)
	return st, nil
}

func (s *Service) Merge(ids []string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return State{}, err
	}
	if len(ids) < 2 {
		return State{}, errors.New("至少选择两段合并")
	}
	idx := make([]int, 0, len(ids))
	for _, id := range ids {
		i := indexSeg(st.Segments, id)
		if i < 0 {
			return State{}, ErrSegmentGone
		}
		idx = append(idx, i)
	}
	// 按工程顺序合并，避免前端乱序点击导致文本错位。
	for i := 1; i < len(idx); i++ {
		for j := i; j > 0 && idx[j] < idx[j-1]; j-- {
			idx[j], idx[j-1] = idx[j-1], idx[j]
		}
	}
	first := st.Segments[idx[0]]
	texts := []string{first.Text}
	takes := append([]Take(nil), first.Takes...)
	for _, i := range idx[1:] {
		texts = append(texts, st.Segments[i].Text)
		takes = append(takes, st.Segments[i].Takes...)
	}
	merged := Segment{ID: first.ID, Text: strings.Join(texts, ""), Takes: takes}
	if len(takes) > 0 {
		last := len(takes) - 1
		merged.Selected = &last
	}
	out := make([]Segment, 0, len(st.Segments))
	for i, seg := range st.Segments {
		if i == idx[0] {
			out = append(out, merged)
			continue
		}
		skip := false
		for _, j := range idx[1:] {
			if i == j {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, seg)
		}
	}
	st.Segments = out
	s.saveLocked(st)
	return st, nil
}

func (s *Service) SplitSegment(id string, pos int) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return State{}, err
	}
	seg := findSeg(st.Segments, id)
	if seg == nil {
		return State{}, ErrSegmentGone
	}
	if pos >= len(seg.Text)-1 || pos < 1 {
		return State{}, ErrInvalidSplit
	}
	pos = SafePos(seg.Text, pos)
	left := strings.TrimSpace(seg.Text[:pos])
	right := strings.TrimSpace(seg.Text[pos:])
	if left == "" || right == "" {
		return State{}, ErrInvalidSplit
	}
	idx := indexSeg(st.Segments, id)
	// 右半段 ID 必须全局唯一：seg.ID+"b" 会与已切分出的 s1b 碰撞，
	// 改为在基础名上追加单调序号直到不冲突。
	newID := seg.ID + "b"
	for n := 2; indexSeg(st.Segments, newID) >= 0; n++ {
		newID = fmt.Sprintf("%sb%d", seg.ID, n)
	}
	st.Segments[idx] = Segment{ID: seg.ID, Text: left, Takes: []Take{}}
	st.Segments = append(st.Segments, Segment{})
	copy(st.Segments[idx+2:], st.Segments[idx+1:])
	st.Segments[idx+1] = Segment{ID: newID, Text: right, Takes: []Take{}}
	s.saveLocked(st)
	return st, nil
}

// TTS 合成单段；追加为新 take 并选中（蓝本保留历史 take）。
func (s *Service) TTS(ctx context.Context, id string) (State, error) {
	s.mu.Lock()
	st, err := s.store.Load()
	s.mu.Unlock()
	if err != nil {
		return State{}, err
	}
	seg := findSeg(st.Segments, id)
	if seg == nil {
		return State{}, ErrSegmentGone
	}
	text := strings.TrimSpace(seg.Text)
	if text == "" {
		return State{}, errors.New("该段文本为空")
	}
	file, take, err := s.synthesize(ctx, seg.ID, text)
	if err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err = s.store.Load()
	if err != nil {
		return State{}, err
	}
	seg = findSeg(st.Segments, id)
	if seg == nil {
		return State{}, ErrSegmentGone
	}
	seg.Takes = append(seg.Takes, take)
	n := len(seg.Takes) - 1
	seg.Selected = &n
	seg.Status = "tts"
	s.saveLocked(st)
	_ = file
	return st, nil
}

func (s *Service) synthesize(ctx context.Context, segID, text string) (string, Take, error) {
	s.synthMu.Lock()
	defer s.synthMu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", Take{}, err
	}
	pcm, err := s.synth.Synth(ctx, text, "")
	if err != nil {
		return "", Take{}, err
	}
	ts := s.now()
	name := fmt.Sprintf("%s_t%d.wav", segID, ts.UnixMilli())
	raw := audio.WavBytes(pcm)
	if err := os.WriteFile(filepath.Join(s.dir, name), raw, 0o644); err != nil {
		return "", Take{}, err
	}
	dur := float64(audio.PCMDurationMS(pcm)) / 1000
	return name, Take{File: name, Dur: dur, Ts: float64(ts.UnixMilli()) / 1000}, nil
}

// TTSAll 异步合成所有段落；只处理缺音频的段（onlyMissing）。
func (s *Service) TTSAll(ctx context.Context, onlyMissing bool) (Job, error) {
	s.mu.Lock()
	if s.job.Running {
		job := s.job
		s.mu.Unlock()
		return job, nil
	}
	st, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return Job{}, err
	}
	targets := make([]string, 0)
	for _, seg := range st.Segments {
		if strings.TrimSpace(seg.Text) == "" {
			continue
		}
		if onlyMissing && len(seg.Takes) > 0 {
			continue
		}
		targets = append(targets, seg.ID)
	}
	s.job = Job{Running: true, Total: len(targets)}
	job := s.job
	s.mu.Unlock()

	go func() {
		for i, id := range targets {
			s.mu.Lock()
			if s.job.Stop {
				s.job.Running = false
				s.job.Idx = nil
				s.mu.Unlock()
				return
			}
			idx := id
			s.job.Idx = &idx
			s.mu.Unlock()

			if _, err := s.TTS(context.WithoutCancel(ctx), id); err != nil {
				s.mu.Lock()
				s.job.Error = err.Error()
				s.mu.Unlock()
			}
			s.mu.Lock()
			s.job.Done = i + 1
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.job.Running = false
		s.job.Idx = nil
		s.mu.Unlock()
	}()
	return job, nil
}

func (s *Service) Select(id string, take int) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return State{}, err
	}
	seg := findSeg(st.Segments, id)
	if seg == nil {
		return State{}, ErrSegmentGone
	}
	if take < 0 || take >= len(seg.Takes) {
		return State{}, errors.New("take 下标越界")
	}
	seg.Selected = &take
	s.saveLocked(st)
	return st, nil
}

func (s *Service) Action(ctx context.Context, id string) (State, error) {
	s.mu.Lock()
	st, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return State{}, err
	}
	seg := findSeg(st.Segments, id)
	if seg == nil {
		s.mu.Unlock()
		return State{}, ErrSegmentGone
	}
	text := seg.Text
	s.mu.Unlock()
	out, err := s.actions.Generate(ctx, text)
	if err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err = s.store.Load()
	if err != nil {
		return State{}, err
	}
	seg = findSeg(st.Segments, id)
	if seg == nil {
		return State{}, ErrSegmentGone
	}
	seg.Action = out
	s.saveLocked(st)
	return st, nil
}

func (s *Service) ActionsAll(ctx context.Context) (State, error) {
	s.mu.Lock()
	st, err := s.store.Load()
	s.mu.Unlock()
	if err != nil {
		return State{}, err
	}
	for _, seg := range st.Segments {
		if strings.TrimSpace(seg.Text) == "" {
			continue
		}
		out, err := s.actions.Generate(ctx, seg.Text)
		if err != nil {
			return State{}, err
		}
		s.mu.Lock()
		cur, err := s.store.Load()
		if err != nil {
			s.mu.Unlock()
			return State{}, err
		}
		if sg := findSeg(cur.Segments, seg.ID); sg != nil {
			sg.Action = out
			_ = s.store.Save(cur)
		}
		s.mu.Unlock()
	}
	return s.State(), nil
}

// Play 试听单段；必须已开播且已选中音频。
func (s *Service) Play(ctx context.Context, id string) error {
	if !s.live() {
		return ErrNotLive
	}
	s.mu.Lock()
	st, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	seg := findSeg(st.Segments, id)
	if seg == nil {
		s.mu.Unlock()
		return ErrSegmentGone
	}
	if seg.Selected == nil || *seg.Selected >= len(seg.Takes) {
		s.mu.Unlock()
		return ErrNoSelection
	}
	take := seg.Takes[*seg.Selected]
	path := filepath.Join(s.dir, filepath.Base(take.File))
	s.mu.Unlock()
	if err := s.player.Play(ctx, path, seg.Text); err != nil {
		return err
	}
	s.mu.Lock()
	if st2, err := s.store.Load(); err == nil {
		if sg := findSeg(st2.Segments, id); sg != nil {
			sg.Status = "played"
			_ = s.store.Save(st2)
		}
	}
	s.mu.Unlock()
	return nil
}

// PlayAll 按工程顺序连续播报；stop 可在段间生效。返回实际播放段数。
func (s *Service) PlayAll(ctx context.Context, gapMs int) (int, error) {
	if !s.live() {
		return 0, ErrNotLive
	}
	s.mu.Lock()
	if s.busyPlay {
		s.mu.Unlock()
		return 0, errors.New("已在连续播报")
	}
	st, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	items := make([]struct {
		id, path, text string
	}, 0)
	for _, seg := range st.Segments {
		if seg.Selected == nil || *seg.Selected >= len(seg.Takes) || strings.TrimSpace(seg.Text) == "" {
			continue
		}
		items = append(items, struct{ id, path, text string }{
			id: seg.ID, path: filepath.Join(s.dir, filepath.Base(seg.Takes[*seg.Selected].File)), text: seg.Text,
		})
	}
	if len(items) == 0 {
		s.mu.Unlock()
		return 0, ErrNoSelection
	}
	// 复位上次 stop 标志，否则 stop 一次后再次连续播报永远 0 段（sticky stop）。
	if st.Stop {
		st.Stop = false
		s.saveLocked(st)
	}
	pctx, cancel := context.WithCancel(ctx)
	s.playCancel = cancel
	s.busyPlay = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busyPlay = false
		s.playCancel = nil
		s.mu.Unlock()
		s.clearPlaying()
		cancel()
	}()
	if gapMs <= 0 {
		gapMs = 1200
	}
	played := 0
	for _, it := range items {
		if s.Stopped() {
			break
		}
		s.setPlaying(it.id)
		if err := s.player.Play(pctx, it.path, it.text); err != nil {
			return played, err
		}
		played++
		select {
		case <-pctx.Done():
			return played, pctx.Err()
		case <-time.After(time.Duration(gapMs) * time.Millisecond):
		}
	}
	return played, nil
}

// setPlaying / clearPlaying 在连续播报期间把 Playing/PlayIdx 写入 store，
// 结束后清回，供状态接口读取真实播放位。
func (s *Service) setPlaying(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return
	}
	st.Playing = true
	idx := id
	st.PlayIdx = &idx
	s.saveLocked(st)
}

func (s *Service) clearPlaying() {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	if err != nil {
		return
	}
	st.Playing = false
	st.PlayIdx = nil
	s.saveLocked(st)
}

// AbortTTSAll 请求停止进行中的全量合成任务（置 stop 标志，不打断当前段合成）。
func (s *Service) AbortTTSAll() {
	s.mu.Lock()
	s.job.Stop = true
	s.mu.Unlock()
}

func (s *Service) Stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Load()
	return err == nil && st.Stop
}

// Stop 停止当前播放并清空 TTS 全量任务（蓝本 /api/koubo/stop）。
func (s *Service) Stop(ctx context.Context) (State, error) {
	s.mu.Lock()
	if s.playCancel != nil {
		s.playCancel()
		s.playCancel = nil
	}
	s.job.Stop = true
	st, err := s.store.Load()
	s.mu.Unlock()
	if err != nil {
		return State{}, err
	}
	_ = s.player.Stop(ctx)
	last := ""
	if st.PlayIdx != nil {
		last = *st.PlayIdx
	}
	st.Playing = false
	st.PlayIdx = nil
	st.Stop = true
	s.mu.Lock()
	s.saveLocked(st)
	out := s.snapshotLocked()
	s.mu.Unlock()
	_ = last
	return out, nil
}

// AudioPath 返回可下载的 wav 路径；拒绝目录穿越。
func (s *Service) AudioPath(name string) (string, error) {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == ".." || base == "" || strings.ContainsAny(name, `/\`) {
		return "", os.ErrNotExist
	}
	p := filepath.Join(s.dir, base)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "", os.ErrNotExist
	}
	return p, nil
}

// WaitIdle 等待后台合成/连续播放结束（供测试与脚本使用）。
func (s *Service) WaitIdle(ctx context.Context) error {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		s.mu.Lock()
		busy := s.job.Running || s.busyPlay
		s.mu.Unlock()
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (s *Service) live() bool {
	s.mu.Lock()
	fn := s.isLive
	s.mu.Unlock()
	return fn == nil || fn()
}

func findSeg(segs []Segment, id string) *Segment {
	for i := range segs {
		if segs[i].ID == id {
			return &segs[i]
		}
	}
	return nil
}

func indexSeg(segs []Segment, id string) int {
	for i := range segs {
		if segs[i].ID == id {
			return i
		}
	}
	return -1
}

func cloneState(st State) State {
	out := st
	out.Segments = make([]Segment, len(st.Segments))
	for i, seg := range st.Segments {
		cp := seg
		cp.Takes = append([]Take(nil), seg.Takes...)
		if seg.Selected != nil {
			v := *seg.Selected
			cp.Selected = &v
		}
		out.Segments[i] = cp
	}
	if st.PlayIdx != nil {
		v := *st.PlayIdx
		out.PlayIdx = &v
	}
	return out
}

func decodeJSON(raw []byte, v any) error { return json.Unmarshal(raw, v) }

func encodeJSON(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

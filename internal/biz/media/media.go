// Package media 提供音频文件库（点播/口播素材）与解码/播放端口。
//
// 蓝本事实：bridge.py `_files_dir/_song_dirs/_song_files/_clean_upload_name`
// 与 `/api/tts/files|upload|import-folder|delete`；这里只做文件系统事实，
// 不实现 ffmpeg 解码与 Vidu 喂流（由 Decoder/Player 端口 + fake 承接）。
package media

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/teamwork/digital-human/pkg/audio"
)

// AudioExts 与蓝本 _AUDIO_EXTS 一致。
var AudioExts = map[string]bool{
	".wav": true, ".mp3": true, ".flac": true, ".ogg": true,
	".m4a": true, ".aac": true, ".opus": true, ".webm": true,
}

const maxUploadBytes = 200 << 20 // 蓝本 200MB

var (
	errBadName  = errors.New("file_name 非法")
	errNotFound = errors.New("音频文件不存在")
	// 文件名只允许常规字符；同时禁止路径分隔与上跳，避免目录穿越。
	nameRE = regexp.MustCompile(`^[\w.\-()\[\] \p{Han}]+$`)
)

// File 对齐蓝本 /api/tts/files 的单项：name/size/mtime/lib。
type File struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	Lib   string `json:"lib"`
}

// Library 是可写点播目录 + 若干只读歌单目录的合集。
// 上传/删除只允许 main 目录；只读目录仅参与列表与播放（与蓝本一致）。
type Library struct {
	mu    sync.RWMutex
	dir   string
	extra map[string]string // lib 名 -> 目录
}

func NewLibrary(dir string) *Library {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("storage", "audio")
	}
	return &Library{dir: dir, extra: map[string]string{}}
}

// SetExtraDirs 设置只读歌单目录（lib 名 -> 路径）。
func (l *Library) SetExtraDirs(dirs map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := make(map[string]string, len(dirs))
	for k, v := range dirs {
		if v != "" {
			cp[k] = v
		}
	}
	l.extra = cp
}

func (l *Library) Dir() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.dir
}

// List 返回 main + 只读目录中的音频，按 name 排序；同名以 main 优先（蓝本 seen 语义）。
func (l *Library) List() ([]File, error) {
	l.mu.RLock()
	dir, extra := l.dir, l.extra
	l.mu.RUnlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out := make([]File, 0)
	seen := map[string]bool{}
	appendDir := func(d, lib string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !AudioExts[strings.ToLower(filepath.Ext(e.Name()))] || seen[e.Name()] {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, File{Name: e.Name(), Size: info.Size(), Mtime: info.ModTime().Unix(), Lib: lib})
			seen[e.Name()] = true
		}
	}
	appendDir(dir, "main")
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		appendDir(extra[k], k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Path 在 main 与只读目录中查找文件；返回绝对/相对路径与 lib。
func (l *Library) Path(name string) (string, string, error) {
	if err := validateName(name); err != nil {
		return "", "", err
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	p := filepath.Join(l.dir, name)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, "main", nil
	}
	keys := make([]string, 0, len(l.extra))
	for k := range l.extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		pp := filepath.Join(l.extra[k], name)
		if st, err := os.Stat(pp); err == nil && !st.IsDir() {
			return pp, k, nil
		}
	}
	return "", "", errNotFound
}

// SaveUpload 把上传流写入 main 目录；同名文件直接覆盖（蓝本 write_bytes 语义）。
func (l *Library) SaveUpload(rawName string, r io.Reader) (File, error) {
	name := CleanName(rawName)
	l.mu.RLock()
	dir := l.dir
	l.mu.RUnlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return File{}, err
	}
	if validateName(name) != nil {
		return File{}, errBadName
	}
	buf, err := io.ReadAll(io.LimitReader(r, maxUploadBytes+1))
	if err != nil {
		return File{}, err
	}
	if len(buf) > maxUploadBytes {
		return File{}, fmt.Errorf("文件过大(>200MB): %s", name)
	}
	dst := filepath.Join(dir, name)
	if err := os.WriteFile(dst, buf, 0o644); err != nil {
		return File{}, err
	}
	return File{Name: name, Size: int64(len(buf)), Mtime: time.Now().Unix(), Lib: "main"}, nil
}

// Delete 只删除 main 目录中的文件（与蓝本 tts_delete 一致）。
func (l *Library) Delete(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	l.mu.RLock()
	dir := l.dir
	l.mu.RUnlock()
	p := filepath.Join(dir, name)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return errNotFound
	}
	return os.Remove(p)
}

// ImportResult 对齐蓝本 import-folder 返回。
type ImportResult struct {
	OK       bool     `json:"ok"`
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors"`
	Dir      string   `json:"dir"`
}

// ImportFolder 递归拷贝 source 下的音频到 main 目录；同名同大小视为重复跳过，
// 同名不同大小自动加 " (n)" 序号（蓝本 do_copy）。
func (l *Library) ImportFolder(source string) (ImportResult, error) {
	src := filepath.Clean(strings.Trim(strings.TrimSpace(source), `"`))
	st, err := os.Stat(src)
	if err != nil || !st.IsDir() {
		return ImportResult{}, fmt.Errorf("文件夹不存在: %s", src)
	}
	found := make([]string, 0)
	err = filepath.Walk(src, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return nil
		}
		if AudioExts[strings.ToLower(filepath.Ext(p))] && !strings.HasPrefix(info.Name(), ".") {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	if len(found) == 0 {
		return ImportResult{}, errors.New("该文件夹(含子文件夹)里没有音频文件")
	}
	l.mu.RLock()
	dir := l.dir
	l.mu.RUnlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ImportResult{}, err
	}
	res := ImportResult{OK: true, Dir: dir, Errors: []string{}}
	for _, f := range found {
		info, err := os.Stat(f)
		if err != nil {
			res.Failed++
			res.addErr(fmt.Sprintf("%s: %v", filepath.Base(f), err))
			continue
		}
		dst := filepath.Join(dir, info.Name())
		stem, ext := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name())), strings.ToLower(filepath.Ext(info.Name()))
		for n := 2; ; n++ {
			dstInfo, statErr := os.Stat(dst)
			if statErr != nil {
				break
			}
			if dstInfo.Size() == info.Size() { // 同名同大小 -> 重复
				res.Skipped++
				dst = ""
				break
			}
			dst = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, n, ext))
			if n > 99 {
				break
			}
		}
		if dst == "" {
			continue
		}
		if err := copyFile(f, dst); err != nil {
			res.Failed++
			res.addErr(fmt.Sprintf("%s: %v", info.Name(), err))
			continue
		}
		res.Imported++
	}
	return res, nil
}

func (r *ImportResult) addErr(s string) {
	if len(r.Errors) < 5 {
		r.Errors = append(r.Errors, s)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// CleanName 还原四种常见上传文件名形态（RFC2047 / RFC5987 / __utf-8_B_ / 普通 UTF-8），
// 非法字符替换为下划线；扩展名缺失补 .wav（蓝本 _clean_upload_name）。
func CleanName(raw string) string {
	name := strings.TrimSpace(raw)
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	// 1) RFC2047: =?utf-8?B?<base64>?=
	if m := regexp.MustCompile(`=\?([\w-]+)\?[Bb]\?([^?]*)\?=`).FindStringSubmatch(name); len(m) == 3 {
		if b, err := base64.StdEncoding.DecodeString(m[2]); err == nil && printable(string(b)) {
			name = string(b)
		}
	}
	// 2) RFC5987: filename*=utf-8''%E7...
	if m := regexp.MustCompile(`utf-8''([^;\s]+)`).FindStringSubmatch(name); len(m) == 2 {
		if u, err := url.QueryUnescape(m[1]); err == nil {
			name = u
		}
	} else if m := regexp.MustCompile(`__utf-8_B_(.+?)_{0,4}$`).FindStringSubmatch(name); len(m) == 2 {
		// 3) __utf-8_B_<base64>__（base64 的 / 与 = 可能被替换成 _）
		inner := m[1]
		for _, cand := range []string{inner, strings.ReplaceAll(inner, "_", "/")} {
			pad := cand + strings.Repeat("=", (4-len(cand)%4)%4)
			if b, err := base64.StdEncoding.DecodeString(pad); err == nil {
				if s := string(b); printable(s) && !strings.HasPrefix(s, "__utf-8") {
					name = s
					break
				}
			}
		}
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' || r == ' ' || r == '(' || r == ')' || r == '[' || r == ']' {
			return r
		}
		return '_'
	}, name)
	name = strings.Trim(name, " _")
	if name == "" {
		name = fmt.Sprintf("clip-%d", time.Now().Unix())
	}
	if !AudioExts[strings.ToLower(path.Ext(name))] {
		name += ".wav"
	}
	return name
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return s != ""
}

func validateName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || !nameRE.MatchString(name) {
		return errBadName
	}
	return nil
}

// Decoder 是音频解码端口（真实实现走 ffmpeg，M3 只提供 fake）。
type Decoder interface {
	Decode(pcmPath string) ([]byte, error)
	Name() string
}

// FakeDecoder 按文件大小估算时长并用静音/音调代替解码，保证 48B/ms 口径可测。
type FakeDecoder struct {
	// MS 固定产出时长；<=0 时按文件大小每 KB 约 20ms 估算（上限 30s）。
	MS int64
}

func (d FakeDecoder) Name() string { return "fake-ffmpeg" }

func (d FakeDecoder) Decode(filePath string) ([]byte, error) {
	st, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}
	ms := d.MS
	if ms <= 0 {
		ms = st.Size() / 1024 * 20
		if ms < 100 {
			ms = 100
		}
		if ms > 30000 {
			ms = 30000
		}
	}
	return toneOrSilence(ms), nil
}

func toneOrSilence(ms int64) []byte {
	// 使用静音，避免 fake 解码在测试中产生无意义的大块正弦计算。
	return audio.SilencePCM(ms)
}

// Player 是喂流端口：真实实现持有 Vidu 喂流权；fake 只记录状态并按时长结束。
type Player interface {
	Play(ctx context.Context, filePath, script string) error
	Stop(ctx context.Context) error
	Name() string
}

// FakePlayer 可配置播放时长；Stop 会提前结束当前播放。
type FakePlayer struct {
	mu      sync.Mutex
	playing bool
	last    string
	stopCh  chan struct{}
	delay   time.Duration
}

func NewFakePlayer() *FakePlayer { return &FakePlayer{delay: 120 * time.Millisecond} }

func (p *FakePlayer) Name() string { return "fake-vidu" }

// SetDelay 便于测试缩短/延长“播放中”窗口。
func (p *FakePlayer) SetDelay(d time.Duration) {
	p.mu.Lock()
	p.delay = d
	p.mu.Unlock()
}

func (p *FakePlayer) Play(ctx context.Context, filePath, script string) error {
	if _, err := os.Stat(filePath); err != nil {
		return err
	}
	p.mu.Lock()
	p.playing = true
	p.last = script
	ch := make(chan struct{})
	p.stopCh = ch
	delay := p.delay
	p.mu.Unlock()
	if delay > 0 {
		select {
		case <-ch:
		case <-ctx.Done():
		case <-time.After(delay):
		}
	}
	p.mu.Lock()
	if p.stopCh == ch {
		p.playing = false
	}
	p.mu.Unlock()
	return nil
}

func (p *FakePlayer) Stop(context.Context) error {
	p.mu.Lock()
	if p.playing && p.stopCh != nil {
		close(p.stopCh)
		p.stopCh = nil
	}
	p.playing = false
	p.mu.Unlock()
	return nil
}

func (p *FakePlayer) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playing
}

func (p *FakePlayer) Last() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

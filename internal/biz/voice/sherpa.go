package voice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teamwork/digital-human/internal/biz/capability"
)

// SherpaOptions 只配置 CPU 流式模型；模型文件和 Python sidecar 缺一即保持 unconfigured。
type commandOptions struct {
	name string
	args []string
	env  []string
}

type SherpaOptions struct {
	PythonBin      string
	ScriptPath     string
	ModelType      string // zipformer | paraformer
	Tokens         string
	Encoder        string
	Decoder        string
	Joiner         string // paraformer 为空
	Threads        int
	StartupTimeout time.Duration
}

type SherpaRecognizer struct {
	opts      SherpaOptions
	command   commandOptions
	configErr error

	startMu  sync.Mutex
	turnMu   sync.Mutex
	writeMu  sync.Mutex
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	ready    chan error
	current  *sherpaRecognition
	cap      Capability
	procErr  error
	nextTurn atomic.Uint64
}

func NewSherpaRecognizer(opts SherpaOptions) *SherpaRecognizer {
	if opts.PythonBin == "" {
		opts.PythonBin = "python"
	}
	if opts.ModelType == "" {
		opts.ModelType = "zipformer"
	}
	if opts.Threads <= 0 {
		opts.Threads = 2
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = 15 * time.Second
	}
	args := []string{opts.ScriptPath, "--model-type", opts.ModelType,
		"--tokens", opts.Tokens, "--encoder", opts.Encoder, "--decoder", opts.Decoder,
		"--joiner", opts.Joiner, "--threads", strconv.Itoa(opts.Threads)}
	r := &SherpaRecognizer{opts: opts, command: commandOptions{name: opts.PythonBin, args: args}}
	r.cap = Capability{Source: capability.Unconfigured, Ready: false, Device: "cpu", Model: filepath.Base(opts.Encoder)}
	for _, path := range []string{opts.ScriptPath, opts.Tokens, opts.Encoder, opts.Decoder} {
		if path == "" || !regularFile(path) {
			r.configErr = fmt.Errorf("required model/sidecar file missing: %s", path)
			break
		}
	}
	if r.configErr == nil && opts.ModelType == "zipformer" && !regularFile(opts.Joiner) {
		r.configErr = errors.New("zipformer joiner model missing")
	}
	if r.configErr == nil && opts.ModelType != "zipformer" && opts.ModelType != "paraformer" {
		r.configErr = fmt.Errorf("unsupported sherpa model_type %q", opts.ModelType)
	}
	return r
}

func newSherpaRecognizer(command commandOptions, opts SherpaOptions) *SherpaRecognizer {
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = 2 * time.Second
	}
	return &SherpaRecognizer{command: command, opts: opts,
		cap: Capability{Source: capability.Unconfigured, Ready: false, Device: "cpu", Model: "helper"}}
}

func regularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func (r *SherpaRecognizer) Capability(ctx context.Context) Capability {
	if err := r.ensureReady(ctx); err != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		return Capability{Source: capability.Unconfigured, Ready: false, Device: "cpu",
			Model: r.cap.Model, ErrorCode: "asr_unavailable", Message: err.Error()}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cap
}

func (r *SherpaRecognizer) ensureReady(ctx context.Context) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	r.mu.Lock()
	if r.configErr != nil {
		err := r.configErr
		r.mu.Unlock()
		return err
	}
	if r.cap.Ready && r.procErr == nil {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	cmd := exec.Command(r.command.name, r.command.args...)
	cmd.Env = append(os.Environ(), r.command.env...)
	cmd.Stderr = &bytes.Buffer{}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	ready := make(chan error, 1)
	r.mu.Lock()
	r.cmd, r.stdin, r.ready, r.ready = cmd, stdin, ready, ready
	r.procErr = nil
	r.mu.Unlock()
	go r.readLoop(cmd, stdout, ready)

	waitCtx, cancel := context.WithTimeout(ctx, r.opts.StartupTimeout)
	defer cancel()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			return err
		}
		r.mu.Lock()
		r.cap = Capability{Source: capability.Real, Ready: true, Device: "cpu", Model: r.cap.Model}
		r.mu.Unlock()
		return nil
	case <-waitCtx.Done():
		_ = cmd.Process.Kill()
		return fmt.Errorf("sherpa startup: %w", waitCtx.Err())
	}
}

func (r *SherpaRecognizer) readLoop(cmd *exec.Cmd, stdout io.Reader, ready chan error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	readySignalled := false
	for scanner.Scan() {
		var msg struct {
			Cmd     string `json:"cmd"`
			Text    string `json:"text"`
			Code    string `json:"code"`
			Message string `json:"message"`
			TurnID  string `json:"turn_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		r.mu.Lock()
		current := r.current
		r.mu.Unlock()
		if current != nil && msg.TurnID != current.id {
			current = nil
		}
		switch msg.Cmd {
		case "ready":
			readySignalled = true
			ready <- nil
		case "partial":
			if current != nil {
				current.onPartial(msg.Text)
			}
		case "final":
			if current != nil {
				select {
				case current.final <- Result{Text: msg.Text}:
				default:
				}
			}
		case "error":
			err := errors.New(msg.Message)
			if msg.Code != "" {
				err = fmt.Errorf("%s: %w", msg.Code, err)
			}
			if current != nil {
				notifySherpaError(current.errs, err)
			} else if !readySignalled && msg.TurnID == "" {
				readySignalled = true
				ready <- err
			}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = errors.New("sherpa sidecar exited")
	}
	_ = cmd.Wait()
	r.mu.Lock()
	currentProcess := r.cmd == cmd
	if currentProcess {
		r.procErr = err
		r.cap.Ready = false
		r.cap.Source = capability.Unconfigured
	}
	current := r.current
	r.mu.Unlock()
	if currentProcess && current != nil {
		notifySherpaError(current.errs, err)
	}
	if !readySignalled {
		ready <- err
	}
}

func notifySherpaError(ch chan error, err error) {
	select {
	case ch <- err:
	default:
	}
}

func (r *SherpaRecognizer) Start(ctx context.Context, onPartial func(string)) (Recognition, error) {
	r.turnMu.Lock()
	if err := r.ensureReady(ctx); err != nil {
		r.turnMu.Unlock()
		return nil, errors.Join(ErrASRUnavailable, err)
	}
	s := &sherpaRecognition{parent: r, onPartial: onPartial, id: strconv.FormatUint(r.nextTurn.Add(1), 10),
		final: make(chan Result, 1), errs: make(chan error, 1)}
	r.mu.Lock()
	r.current = s
	r.mu.Unlock()
	if err := r.send(map[string]any{"cmd": "begin", "turn_id": s.id}); err != nil {
		r.mu.Lock()
		if r.current == s {
			r.current = nil
		}
		r.mu.Unlock()
		r.turnMu.Unlock()
		return nil, errors.Join(ErrASRUnavailable, err)
	}
	return s, nil
}

func (r *SherpaRecognizer) send(v map[string]any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.mu.Lock()
	stdin, err := r.stdin, r.procErr
	r.mu.Unlock()
	if stdin == nil {
		return errors.New("sherpa sidecar not running")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdin).Encode(v)
}

// Close 终止 sidecar；进程内所有在途识别都会收到明确错误。
func (r *SherpaRecognizer) Close() error {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	r.mu.Lock()
	cmd, stdin, current := r.cmd, r.stdin, r.current
	r.cmd, r.stdin = nil, nil
	r.cap.Ready = false
	r.cap.Source = capability.Unconfigured
	r.mu.Unlock()
	if current != nil {
		notifySherpaError(current.errs, errors.New("sherpa sidecar closed"))
	}
	var err error
	if cmd != nil && cmd.Process != nil {
		err = cmd.Process.Kill()
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	return err
}

type sherpaRecognition struct {
	parent    *SherpaRecognizer
	onPartial func(string)
	id        string
	final     chan Result
	errs      chan error
	mu        sync.Mutex
	finalized bool
	closed    bool
	closeOnce sync.Once
}

func (s *sherpaRecognition) WritePCM(ctx context.Context, pcm []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrTurnClosed
	}
	return s.parent.send(map[string]any{"cmd": "audio", "turn_id": s.id, "data": base64.StdEncoding.EncodeToString(pcm)})
}

func (s *sherpaRecognition) Finalize(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return Result{}, ErrTurnClosed
	}
	if err := s.parent.send(map[string]any{"cmd": "end", "turn_id": s.id}); err != nil {
		return Result{}, err
	}
	select {
	case result := <-s.final:
		s.mu.Lock()
		s.finalized = true
		s.mu.Unlock()
		return result, nil
	case err := <-s.errs:
		return Result{}, err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (s *sherpaRecognition) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		finalized := s.finalized
		s.closed = true
		s.mu.Unlock()
		if !finalized {
			err = s.parent.send(map[string]any{"cmd": "cancel", "turn_id": s.id})
		}
		s.parent.mu.Lock()
		if s.parent.current == s {
			s.parent.current = nil
		}
		s.parent.mu.Unlock()
		s.parent.turnMu.Unlock()
	})
	return err
}

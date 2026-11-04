package voice

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/capability"
)

func TestSherpaMissingModelFailsClosed(t *testing.T) {
	r := NewSherpaRecognizer(SherpaOptions{PythonBin: "python", ScriptPath: "missing.py", Tokens: "missing.tokens"})
	cap := r.Capability(context.Background())
	if cap.Ready || cap.Source != capability.Unconfigured || cap.ErrorCode != "asr_unavailable" {
		t.Fatalf("capability = %+v", cap)
	}
	_, err := r.Start(context.Background(), func(string) {})
	if err == nil {
		t.Fatal("missing model must not start")
	}
}

func TestSherpaProcessStreamsFinalizesAndCancels(t *testing.T) {
	if os.Getenv("GO_WANT_SHERPA_HELPER") == "1" {
		sherpaHelperProcess()
		return
	}
	r := newSherpaTestRecognizer(t)
	t.Cleanup(func() { _ = r.Close() })
	cap := r.Capability(context.Background())
	if !cap.Ready || cap.Source != capability.Real || cap.Device != "cpu" {
		t.Fatalf("capability = %+v", cap)
	}
	partials := make(chan string, 1)
	rec, err := r.Start(context.Background(), func(text string) { partials <- text })
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.WritePCM(context.Background(), loudFrames(1)); err != nil {
		t.Fatal(err)
	}
	select {
	case partial := <-partials:
		if partial != "识别中" {
			t.Fatalf("partial = %q", partial)
		}
	case <-time.After(time.Second):
		t.Fatal("partial did not arrive")
	}
	result, err := rec.Finalize(context.Background())
	if err != nil || result.Text != "识别完成" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	rec2, err := r.Start(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := rec2.Finalize(ctx)
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled Finalize returned nil")
		}
	case <-time.After(time.Second):
		t.Fatal("Finalize did not observe cancellation")
	}
	_ = rec2.Close()
}

func TestSherpaIgnoresStaleFinalFromPreviousTurn(t *testing.T) {
	r := newSherpaTestRecognizerMode(t, "stale")
	t.Cleanup(func() { _ = r.Close() })
	first, err := r.Start(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := first.Finalize(firstCtx)
		firstResult <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancelFirst()
	<-firstResult
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := r.Start(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	result, err := second.Finalize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "新回合" {
		t.Fatalf("stale final crossed turns: %+v", result)
	}
}

func TestSherpaErrorThenProcessExitDoesNotDeadlockClose(t *testing.T) {
	r := newSherpaTestRecognizerMode(t, "crash")
	rec, err := r.Start(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	typed := rec.(*sherpaRecognition)
	if err := rec.WritePCM(context.Background(), loudFrames(1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-typed.errs:
		case <-time.After(time.Second):
			t.Fatal("sidecar errors did not arrive")
		}
	}
	time.Sleep(100 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case <-closed:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Close deadlocked after sidecar error and process exit")
	}
}

func TestSherpaOldReadLoopCannotPolluteRestartedProcess(t *testing.T) {
	newCmd := exec.Command(os.Args[0])
	newCurrent := &sherpaRecognition{id: "new-turn", errs: make(chan error, 1)}
	r := &SherpaRecognizer{
		cmd: newCmd, current: newCurrent, cap: Capability{Source: capability.Real, Ready: true},
	}
	r.readLoop(exec.Command(os.Args[0]), strings.NewReader(""), make(chan error, 1))
	if r.procErr != nil || !r.cap.Ready || r.cap.Source != capability.Real {
		t.Fatalf("old readLoop polluted restarted process: %+v, %v", r.cap, r.procErr)
	}
	select {
	case err := <-newCurrent.errs:
		t.Fatalf("old readLoop notified new recognition: %v", err)
	default:
	}
}

func TestSherpaCloseWakesInflightFinalizeAndReleasesTurn(t *testing.T) {
	r := newSherpaTestRecognizerMode(t, "hang")
	rec, err := r.Start(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	finalized := make(chan error, 1)
	go func() {
		_, err := rec.Finalize(context.Background())
		finalized <- err
	}()
	time.Sleep(30 * time.Millisecond)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finalized:
		if err == nil {
			t.Fatal("Close must fail the in-flight Finalize")
		}
	case <-time.After(time.Second):
		t.Fatal("Close left Finalize blocked")
	}
	_ = rec.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	next, err := r.Start(ctx, func(string) {})
	if err != nil {
		t.Fatalf("Start after Close remained blocked: %v", err)
	}
	_ = next.Close()
}

func TestSherpaRealModelIntegration(t *testing.T) {
	if os.Getenv("DH_TEST_SHERPA") != "1" {
		t.Skip("真实 sherpa 集成测试需显式设置 DH_TEST_SHERPA=1、模型许可确认、模型路径和 16kHz PCM 音频")
	}
	if os.Getenv("DH_TEST_SHERPA_LICENSE_ACK") != "1" {
		t.Skip("真实模型测试需显式确认已核查模型许可证：DH_TEST_SHERPA_LICENSE_ACK=1")
	}
	paths := map[string]string{
		"DH_TEST_SHERPA_TOKENS":  os.Getenv("DH_TEST_SHERPA_TOKENS"),
		"DH_TEST_SHERPA_ENCODER": os.Getenv("DH_TEST_SHERPA_ENCODER"),
		"DH_TEST_SHERPA_DECODER": os.Getenv("DH_TEST_SHERPA_DECODER"),
		"DH_TEST_SHERPA_AUDIO":   os.Getenv("DH_TEST_SHERPA_AUDIO"),
	}
	for name, path := range paths {
		if path == "" || !regularFile(path) {
			t.Skipf("真实 sherpa 测试缺少有效文件 %s=%q", name, path)
		}
	}
	modelType := os.Getenv("DH_TEST_SHERPA_MODEL_TYPE")
	if modelType == "" {
		modelType = "zipformer"
	}
	joiner := os.Getenv("DH_TEST_SHERPA_JOINER")
	if modelType == "zipformer" && (joiner == "" || !regularFile(joiner)) {
		t.Skipf("真实 zipformer 测试缺少有效 DH_TEST_SHERPA_JOINER=%q", joiner)
	}
	pythonBin := os.Getenv("DH_TEST_SHERPA_PYTHON")
	if pythonBin == "" {
		pythonBin = "python"
	}
	r := NewSherpaRecognizer(SherpaOptions{
		PythonBin: pythonBin, ScriptPath: "sherpa_sidecar.py", ModelType: modelType,
		Tokens: paths["DH_TEST_SHERPA_TOKENS"], Encoder: paths["DH_TEST_SHERPA_ENCODER"],
		Decoder: paths["DH_TEST_SHERPA_DECODER"], Joiner: joiner, Threads: 2, StartupTimeout: 30 * time.Second,
	})
	t.Cleanup(func() { _ = r.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	capability := r.Capability(ctx)
	if !capability.Ready {
		t.Fatalf("真实 sherpa 未就绪: %+v", capability)
	}
	rec, err := r.Start(ctx, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	pcm, err := os.ReadFile(paths["DH_TEST_SHERPA_AUDIO"])
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) == 0 || len(pcm)%640 != 0 {
		t.Fatalf("测试音频必须是完整的 640 字节 PCM 帧: %d bytes", len(pcm))
	}
	for offset := 0; offset < len(pcm); offset += 640 {
		if err := rec.WritePCM(ctx, pcm[offset:offset+640]); err != nil {
			t.Fatal(err)
		}
	}
	result, err := rec.Finalize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text == "" {
		t.Fatal("真实 sherpa 返回空识别文本")
	}
	t.Logf("真实 sherpa 识别结果: %q", result.Text)
}

func newSherpaTestRecognizer(t *testing.T) *SherpaRecognizer {
	return newSherpaTestRecognizerMode(t, "")
}

func newSherpaTestRecognizerMode(t *testing.T, mode string) *SherpaRecognizer {
	t.Helper()
	return newSherpaRecognizer(commandOptions{
		name: os.Args[0],
		args: []string{"-test.run=TestSherpaProcessStreamsFinalizesAndCancels"},
		env:  []string{"GO_WANT_SHERPA_HELPER=1", "SHERPA_HELPER_MODE=" + mode},
	}, SherpaOptions{StartupTimeout: 2 * time.Second})
}

type sherpaWire struct {
	Cmd     string `json:"cmd"`
	Text    string `json:"text,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	TurnID  string `json:"turn_id,omitempty"`
}

func sherpaHelperProcess() {
	scanner := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	mode := os.Getenv("SHERPA_HELPER_MODE")
	_ = enc.Encode(sherpaWire{Cmd: "ready"})
	endCount := 0
	turnID := ""
	for scanner.Scan() {
		var in sherpaWire
		if json.Unmarshal(scanner.Bytes(), &in) != nil {
			return
		}
		switch in.Cmd {
		case "begin":
			turnID = in.TurnID
			if mode == "stale" && endCount == 1 {
				_ = enc.Encode(sherpaWire{Cmd: "final", Text: "旧回合", TurnID: "stale-turn"})
			}
		case "audio":
			if mode == "crash" {
				_ = enc.Encode(sherpaWire{Cmd: "error", Code: "asr_unavailable", Message: "first", TurnID: turnID})
				_ = enc.Encode(sherpaWire{Cmd: "error", Code: "asr_unavailable", Message: "second", TurnID: turnID})
				_ = enc.Encode(sherpaWire{Cmd: "error", Code: "asr_unavailable", Message: "third", TurnID: turnID})
				os.Exit(0)
			}
			_ = enc.Encode(sherpaWire{Cmd: "partial", Text: "识别中", TurnID: turnID})
		case "end":
			endCount++
			if mode == "hang" {
				continue
			}
			if mode == "stale" && endCount == 1 {
				continue
			}
			if mode == "" && endCount > 1 {
				time.Sleep(500 * time.Millisecond)
			}
			text := "识别完成"
			if mode == "stale" {
				text = "新回合"
			}
			_ = enc.Encode(sherpaWire{Cmd: "final", Text: text, TurnID: turnID})
		case "cancel":
			_ = enc.Encode(sherpaWire{Cmd: "cancelled", TurnID: turnID})
		}
	}
	os.Exit(0)
}

package dashscope

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/engine/tts"
	"github.com/teamwork/digital-human/pkg/audio"
)

type smokeEvidence struct {
	Timestamp                 string              `json:"timestamp"`
	Model                     string              `json:"model"`
	Voice                     string              `json:"voice"`
	Capability                tts.CapabilityState `json:"capability"`
	FailureClass              FailureClass        `json:"failure_class,omitempty"`
	ErrorCode                 string              `json:"error_code,omitempty"`
	Error                     string              `json:"error,omitempty"`
	Guidance                  string              `json:"guidance,omitempty"`
	NegotiatedResponseFormat  string              `json:"negotiated_response_format"`
	NegotiatedSampleRate      int                 `json:"negotiated_sample_rate"`
	WAVFormat                 uint16              `json:"wav_format"`
	WAVChannels               uint16              `json:"wav_channels"`
	WAVSampleRate             uint32              `json:"wav_sample_rate"`
	WAVBitsPerSample          uint16              `json:"wav_bits_per_sample"`
	WAVDataBytes              int                 `json:"wav_data_bytes"`
	PCMBytes                  int                 `json:"pcm_bytes"`
	PCMSampleAligned          bool                `json:"pcm_sample_aligned"`
	FullChunks100MS           int                 `json:"full_chunks_100ms"`
	RemainderBytes            int                 `json:"remainder_bytes"`
	Chunks100MS               float64             `json:"chunks_100ms"`
	DurationSeconds           float64             `json:"duration_seconds"`
	DurationFromChunksSeconds float64             `json:"duration_from_chunks_seconds"`
	NegotiationMatches        bool                `json:"negotiation_matches"`
	WAVFmtMatches             bool                `json:"wav_fmt_matches"`
	DurationMatches           bool                `json:"duration_matches"`
}

type smokeWAVFmt struct {
	format        uint16
	channels      uint16
	sampleRate    uint32
	bitsPerSample uint16
	dataBytes     int
}

func TestQwen3RealtimeSmokeEvidenceChain(t *testing.T) {
	pcm := audio.TonePCM(250)
	evidence, _, err := buildSmokeEvidence("qwen3-tts-flash-realtime", "Cherry", streamResult{
		Session:  sessionConfig{ResponseFormat: "pcm", SampleRate: audio.SampleRate},
		PCMBytes: len(pcm),
	}, pcm)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Capability != tts.CapabilityReal || !evidence.NegotiationMatches || !evidence.WAVFmtMatches || !evidence.DurationMatches {
		t.Fatalf("evidence checks failed: %+v", evidence)
	}
	if evidence.PCMBytes != 12000 || evidence.FullChunks100MS != 2 || evidence.RemainderBytes != 2400 {
		t.Fatalf("PCM chunk evidence = %+v", evidence)
	}
	if evidence.Chunks100MS != 2.5 || evidence.DurationSeconds != 0.25 || evidence.DurationFromChunksSeconds != 0.25 {
		t.Fatalf("PCM duration evidence = %+v", evidence)
	}
	odd, _, err := buildSmokeEvidence("qwen3-tts-flash-realtime", "Cherry", streamResult{
		Session:  sessionConfig{ResponseFormat: "pcm", SampleRate: audio.SampleRate},
		PCMBytes: 1,
	}, []byte{0})
	if err != nil {
		t.Fatal(err)
	}
	if odd.Capability != tts.CapabilityUnknown || odd.PCMSampleAligned {
		t.Fatalf("odd PCM must remain unknown: %+v", odd)
	}
}

func TestQwen3RealtimeSmoke(t *testing.T) {
	names := []string{
		"DH_TEST_DASHSCOPE_API_KEY",
		"DH_TEST_DASHSCOPE_WS_BASE_URL",
		"DH_TEST_DASHSCOPE_MODEL",
		"DH_TEST_DASHSCOPE_VOICE",
	}
	var missing []string
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Skipf("真实 DashScope smoke 未启用，缺少环境变量: %s", strings.Join(missing, ", "))
	}

	outputDir := smokeOutputDir(t)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("create smoke output directory: %v", err)
	}
	model := os.Getenv("DH_TEST_DASHSCOPE_MODEL")
	voice := os.Getenv("DH_TEST_DASHSCOPE_VOICE")
	s, err := NewSynthesizer(tts.CloudConfig{
		Provider:   tts.CloudProviderDashscope,
		BaseURL:    os.Getenv("DH_TEST_DASHSCOPE_WS_BASE_URL"),
		Model:      model,
		APIKey:     os.Getenv("DH_TEST_DASHSCOPE_API_KEY"),
		Voice:      voice,
		SampleRate: audio.SampleRate,
		Timeout:    60 * time.Second,
	})
	if err != nil {
		failSmoke(t, outputDir, smokeEvidence{
			Model: model, Voice: voice, Capability: tts.CapabilityUnknown,
		}, err)
	}

	var pcm bytes.Buffer
	result, err := s.run(context.Background(), tts.JobRequest{
		Text:    "你好，这是一次 Qwen3 实时语音合成冒烟验证。",
		VoiceID: voice,
	}, func(_ context.Context, delta []byte) error {
		_, err := pcm.Write(delta)
		return err
	})
	if err != nil {
		failSmoke(t, outputDir, smokeEvidence{
			Model: model, Voice: voice, Capability: tts.CapabilityUnknown,
			NegotiatedResponseFormat: result.Session.ResponseFormat,
			NegotiatedSampleRate:     result.Session.SampleRate,
			PCMBytes:                 result.PCMBytes,
		}, err)
	}

	evidence, wav, err := buildSmokeEvidence(model, voice, result, pcm.Bytes())
	if err != nil {
		failSmoke(t, outputDir, smokeEvidence{
			Model: model, Voice: voice, Capability: tts.CapabilityUnknown,
			NegotiatedResponseFormat: result.Session.ResponseFormat,
			NegotiatedSampleRate:     result.Session.SampleRate,
			PCMBytes:                 result.PCMBytes,
		}, err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "output.pcm"), pcm.Bytes(), 0o600); err != nil {
		t.Fatalf("write smoke PCM: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "output.wav"), wav, 0o600); err != nil {
		t.Fatalf("write smoke WAV: %v", err)
	}
	writeSmokeEvidence(t, outputDir, evidence)
	if evidence.Capability != tts.CapabilityReal {
		t.Fatalf("真实 smoke 证据链不一致，capability=unknown，证据: %s", filepath.Join(outputDir, "evidence.json"))
	}
	t.Logf("真实 smoke 通过，证据: %s", outputDir)
}

func buildSmokeEvidence(model, voice string, result streamResult, pcm []byte) (smokeEvidence, []byte, error) {
	evidence := smokeEvidence{
		Timestamp:                time.Now().UTC().Format(time.RFC3339Nano),
		Model:                    model,
		Voice:                    voice,
		Capability:               tts.CapabilityUnknown,
		NegotiatedResponseFormat: result.Session.ResponseFormat,
		NegotiatedSampleRate:     result.Session.SampleRate,
		PCMBytes:                 len(pcm),
		PCMSampleAligned:         len(pcm)%audio.BytesPerSample == 0,
		FullChunks100MS:          len(pcm) / audio.DefaultChunkBytes,
		RemainderBytes:           len(pcm) % audio.DefaultChunkBytes,
		Chunks100MS:              float64(len(pcm)) / float64(audio.DefaultChunkBytes),
		DurationSeconds:          audio.SecondsForBytes(len(pcm)),
	}
	evidence.DurationFromChunksSeconds = evidence.Chunks100MS * float64(audio.DefaultChunkMS) / 1000
	evidence.DurationMatches = math.Abs(evidence.DurationSeconds-evidence.DurationFromChunksSeconds) < 1e-9
	evidence.NegotiationMatches = result.Session.ResponseFormat == "pcm" && result.Session.SampleRate == audio.SampleRate

	wav := audio.WavBytes(pcm)
	format, err := parseSmokeWAV(wav)
	if err != nil {
		return evidence, nil, err
	}
	evidence.WAVFormat = format.format
	evidence.WAVChannels = format.channels
	evidence.WAVSampleRate = format.sampleRate
	evidence.WAVBitsPerSample = format.bitsPerSample
	evidence.WAVDataBytes = format.dataBytes
	evidence.WAVFmtMatches = format.format == 1 && format.channels == audio.Channels &&
		format.sampleRate == uint32(result.Session.SampleRate) && format.bitsPerSample == audio.BytesPerSample*8 &&
		format.dataBytes == len(pcm)
	if len(pcm) > 0 && evidence.PCMSampleAligned && result.PCMBytes == len(pcm) && evidence.NegotiationMatches && evidence.WAVFmtMatches && evidence.DurationMatches {
		evidence.Capability = tts.CapabilityReal
	} else {
		evidence.FailureClass = FailureProtocol
		evidence.Guidance = "真实音频或证据链不一致，能力保持 unknown；协议不可用，需要排查代码或服务事件。"
	}
	return evidence, wav, nil
}

func parseSmokeWAV(wav []byte) (smokeWAVFmt, error) {
	var result smokeWAVFmt
	if len(wav) < 12 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return result, fmt.Errorf("invalid RIFF/WAVE header")
	}
	var fmtFound, dataFound bool
	for offset := 12; offset+8 <= len(wav); {
		size := int(binary.LittleEndian.Uint32(wav[offset+4 : offset+8]))
		body := offset + 8
		if size < 0 || body+size > len(wav) {
			return result, fmt.Errorf("invalid WAV chunk size %d", size)
		}
		switch string(wav[offset : offset+4]) {
		case "fmt ":
			if size < 16 {
				return result, fmt.Errorf("short WAV fmt chunk: %d", size)
			}
			result.format = binary.LittleEndian.Uint16(wav[body : body+2])
			result.channels = binary.LittleEndian.Uint16(wav[body+2 : body+4])
			result.sampleRate = binary.LittleEndian.Uint32(wav[body+4 : body+8])
			result.bitsPerSample = binary.LittleEndian.Uint16(wav[body+14 : body+16])
			fmtFound = true
		case "data":
			result.dataBytes = size
			dataFound = true
		}
		offset = body + size + size%2
	}
	if !fmtFound || !dataFound {
		return result, fmt.Errorf("WAV fmt/data chunk missing: fmt=%t data=%t", fmtFound, dataFound)
	}
	return result, nil
}

func failSmoke(t *testing.T, outputDir string, evidence smokeEvidence, err error) {
	t.Helper()
	evidence.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	evidence.Capability = tts.CapabilityUnknown
	evidence.FailureClass = ClassifyFailure(err)
	evidence.ErrorCode = tts.ErrorCode(err)
	evidence.Error = err.Error()
	if evidence.FailureClass == FailureModelUnavailable {
		evidence.Guidance = "协议可用但模型在当前业务空间未启用或不可达；更换模型、业务空间或 API Key 配置，无需修改 adapter。"
	} else {
		evidence.Guidance = "协议或连接失败，能力保持 unknown；协议不可用时需要排查并修改代码。"
	}
	writeSmokeEvidence(t, outputDir, evidence)
	t.Fatalf("真实 smoke 失败: %v；证据: %s", err, filepath.Join(outputDir, "evidence.json"))
}

func writeSmokeEvidence(t *testing.T, outputDir string, evidence smokeEvidence) {
	t.Helper()
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatalf("marshal smoke evidence: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(outputDir, "evidence.json"), data, 0o600); err != nil {
		t.Fatalf("write smoke evidence: %v", err)
	}
}

func smokeOutputDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate smoke test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
	now := time.Now()
	name := fmt.Sprintf("qwen3-realtime-%s-%09d", now.Format("20060102-150405"), now.Nanosecond())
	return filepath.Join(repoRoot, "..", "temp", "digital-human", "runtime", name)
}

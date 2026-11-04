// Package dashscope 实现百炼 Qwen3 Realtime 的流式 PCM 出口。
package dashscope

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/internal/engine/tts"
	"github.com/teamwork/digital-human/pkg/audio"
)

type sessionConfig struct {
	Voice          string `json:"voice,omitempty"`
	Mode           string `json:"mode,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	SampleRate     int    `json:"sample_rate,omitempty"`
}

type serverEvent struct {
	Type    string        `json:"type"`
	Delta   string        `json:"delta"`
	Session sessionConfig `json:"session"`
	Error   *ServerError  `json:"error"`
}

type streamResult struct {
	Session  sessionConfig
	PCMBytes int
}

// Synthesizer 每次合成建立独立 Realtime 会话，直接输出 24kHz/s16le/mono PCM。
type Synthesizer struct {
	cfg      tts.CloudConfig
	dialer   *websocket.Dialer
	cache    *tts.CapabilityCache
	identity string
}

func NewSynthesizer(cfg tts.CloudConfig) (*Synthesizer, error) {
	normalized, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	protocol, _ := tts.ProtocolForModel(normalized.Model)
	if protocol != tts.CloudProtocolQwen3Realtime {
		return nil, &tts.CloudError{Code: "cloud_tts_protocol_unimplemented", Message: "云 TTS 模型协议尚未实现: " + normalized.Model}
	}
	if normalized.SampleRate != audio.SampleRate {
		return nil, fmt.Errorf("dashscope: qwen3 realtime requires sample_rate=24000, got %d", normalized.SampleRate)
	}
	apiKey, err := normalized.ResolveAPIKey()
	if err != nil {
		return nil, err
	}
	normalized.APIKey = apiKey
	identity, err := normalized.Identity()
	if err != nil {
		return nil, err
	}
	dialer := *websocket.DefaultDialer
	dialer.EnableCompression = false
	dialer.HandshakeTimeout = normalized.Timeout
	return &Synthesizer{
		cfg:      normalized,
		dialer:   &dialer,
		cache:    tts.NewCapabilityCache(normalized.ProbeCacheTTL),
		identity: identity,
	}, nil
}

// SynthesizeStream 在首个 response.audio.delta 到达时立即调用 emit，不等待整段合成完成。
func (s *Synthesizer) SynthesizeStream(ctx context.Context, req tts.JobRequest, emit func(context.Context, []byte) error) error {
	result, err := s.run(ctx, req, emit)
	if err != nil || result.PCMBytes <= 0 {
		s.cache.RecordFailure(s.identity)
		return err
	}
	s.cache.RecordSuccess(s.identity, result.PCMBytes)
	return nil
}

func (s *Synthesizer) run(ctx context.Context, req tts.JobRequest, emit func(context.Context, []byte) error) (streamResult, error) {
	var result streamResult
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	endpoint, err := url.Parse(s.cfg.BaseURL)
	if err != nil {
		return result, fmt.Errorf("dashscope: parse realtime url: %w", err)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/")
	if !strings.HasSuffix(endpoint.Path, "/realtime") {
		endpoint.Path += "/realtime"
	}
	query := endpoint.Query()
	query.Set("model", s.cfg.Model)
	endpoint.RawQuery = query.Encode()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	conn, _, err := s.dialer.DialContext(ctx, endpoint.String(), header)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("dashscope: realtime dial: %w", err)
	}
	defer conn.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()

	voice := req.VoiceID
	if voice == "" {
		voice = s.cfg.Voice
	}
	if err := s.writeJSON(conn, map[string]any{
		"event_id": uuid.NewString(),
		"type":     "session.update",
		"session": sessionConfig{
			Voice:          voice,
			Mode:           "commit",
			ResponseFormat: "pcm",
			SampleRate:     audio.SampleRate,
		},
	}); err != nil {
		return result, err
	}
	for {
		var event serverEvent
		if err := readJSON(conn, &event); err != nil {
			return result, readError(ctx, err)
		}
		switch event.Type {
		case "session.created":
			continue
		case "error":
			return result, realtimeServerError(event)
		case "session.updated":
			if event.Session.ResponseFormat != "pcm" || event.Session.SampleRate != audio.SampleRate {
				return result, fmt.Errorf("dashscope: session.updated format=%q sample_rate=%d, want pcm/24000",
					event.Session.ResponseFormat, event.Session.SampleRate)
			}
			result.Session = event.Session
			goto negotiated
		}
	}

negotiated:
	// session.finish 表示不再输入文本；官方协议随后返回剩余音频，不能等待 response.done 后再发送。
	for _, event := range []map[string]any{
		{"event_id": uuid.NewString(), "type": "input_text_buffer.append", "text": req.Text},
		{"event_id": uuid.NewString(), "type": "input_text_buffer.commit"},
		{"event_id": uuid.NewString(), "type": "session.finish"},
	} {
		if err := s.writeJSON(conn, event); err != nil {
			return result, err
		}
	}

	responseFinished := false
	for {
		var event serverEvent
		if err := readJSON(conn, &event); err != nil {
			return result, readError(ctx, err)
		}
		switch event.Type {
		case "response.audio.delta":
			pcm, err := base64.StdEncoding.DecodeString(event.Delta)
			if err != nil {
				return result, fmt.Errorf("dashscope: decode response.audio.delta: %w", err)
			}
			if len(pcm) == 0 {
				continue
			}
			result.PCMBytes += len(pcm)
			if err := emit(ctx, pcm); err != nil {
				return result, err
			}
		case "response.done":
			responseFinished = true
		case "session.finished":
			if !responseFinished {
				return result, fmt.Errorf("dashscope: session.finished before response.done")
			}
			if result.PCMBytes%audio.BytesPerSample != 0 {
				return result, fmt.Errorf("dashscope: PCM byte count %d is not s16le sample aligned", result.PCMBytes)
			}
			return result, nil
		case "error":
			return result, realtimeServerError(event)
		}
	}
}

func realtimeServerError(event serverEvent) error {
	if event.Error == nil {
		return fmt.Errorf("dashscope: malformed error event without error object")
	}
	return event.Error
}

func (s *Synthesizer) writeJSON(conn *websocket.Conn, value any) error {
	if err := conn.SetWriteDeadline(time.Now().Add(s.cfg.Timeout)); err != nil {
		return fmt.Errorf("dashscope: set write deadline: %w", err)
	}
	if err := conn.WriteJSON(value); err != nil {
		return fmt.Errorf("dashscope: write realtime event: %w", err)
	}
	return nil
}

func readJSON(conn *websocket.Conn, value any) error {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("dashscope: decode realtime event: %w", err)
	}
	return nil
}

func readError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("dashscope: read realtime event: %w", err)
}

// Package eventcontract 定义前后端共享的事件契约常量。
// 唯一事实来源是 web/src/services/api/events.contract.json；
// contract_test.go 会读取该文件校验此处常量与字段集合，避免两侧漂移。
package eventcontract

// 事件名。
const (
	TranscriptPartial = "transcript.partial"
	TranscriptFinal   = "transcript.final"
	VoiceListening    = "voice.listening"
	VoiceFinal        = "voice.final"
	VoiceThinking     = "voice.thinking"
	VoiceSpeaking     = "voice.speaking"
	VoiceFirstAudio   = "voice.first_audio"
	VoiceCompleted    = "voice.completed"
	VoiceError        = "voice.error"
)

// payload 字段名（后端发布用，前端解析用同一契约）。
const (
	FieldText        = "text"
	FieldIndex       = "index"
	FieldMode        = "mode"
	FieldUtteranceID = "utterance_id"
)

// EnvelopeFields 事件信封字段（顺序与契约 JSON 一致）。
var EnvelopeFields = []string{
	"seq", "event_id", "session_id", "type", "ts_ms", "epoch", "request_id", "payload",
}

package live

import "context"

type firstPCMKey struct{}
type transcriptPersistenceKey struct{}

// WithTranscriptPersistence 控制本次 Say 是否发布/留存字幕；默认 true。
// false 只影响当前语音回合，其他口播与既有调用不变。
func WithTranscriptPersistence(ctx context.Context, persist bool) context.Context {
	return context.WithValue(ctx, transcriptPersistenceKey{}, persist)
}

func transcriptPersistence(ctx context.Context) bool {
	persist, ok := ctx.Value(transcriptPersistenceKey{}).(bool)
	return !ok || persist
}

// WithFirstPCMHook 在引擎真正发出首个 PCM 帧时回调；它不是 TTS WAV 就绪时间。
func WithFirstPCMHook(ctx context.Context, fn func()) context.Context {
	return context.WithValue(ctx, firstPCMKey{}, fn)
}

func NotifyFirstPCM(ctx context.Context) {
	if fn, ok := ctx.Value(firstPCMKey{}).(func()); ok && fn != nil {
		fn()
	}
}

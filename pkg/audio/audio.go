// Package audio 提供 24kHz / s16le / mono 的 PCM 换算与实时喂流原语。
//
// 事实来源：蓝本 tts/audio.py:16-17 定义 SAMPLE_RATE=24000、BYTES_PER_MS=48，
// engine.py:1703-1761 以 100ms 分片按绝对时基喂流。本包只做换算与节奏，
// 不做重采样（重采样由解码阶段/ffmpeg 负责）。
package audio

const (
	// SampleRate 蓝本固定 24kHz。
	SampleRate = 24000
	// Channels 单声道。
	Channels = 1
	// BytesPerSample s16le = 2 字节。
	BytesPerSample = 2
	// BytesPerMS 24000 * 1 * 2 / 1000 = 48。
	BytesPerMS = SampleRate * Channels * BytesPerSample / 1000

	// DefaultChunkMS 默认 100ms 分片（蓝本 tts.chunk_ms 默认值）。
	DefaultChunkMS = 100
	// DefaultChunkBytes 100ms = 4800 字节。
	DefaultChunkBytes = DefaultChunkMS * BytesPerMS
)

// MSForBytes 把 PCM 字节数换算为整毫秒（不足 1ms 的部分截断）。
// 蓝本播放进度用 len(piece) // BYTES_PER_MS，本函数保持同一口径。
func MSForBytes(n int) int64 {
	if n <= 0 {
		return 0
	}
	return int64(n / BytesPerMS)
}

// BytesForMS 把毫秒换算为字节数。
func BytesForMS(ms int64) int {
	if ms <= 0 {
		return 0
	}
	return int(ms * BytesPerMS)
}

// SecondsForBytes 便于日志与统计，精确到字节。
func SecondsForBytes(n int) float64 {
	return float64(n) / float64(SampleRate*Channels*BytesPerSample)
}

// SplitPCM 按 chunkBytes 切分 PCM；最后不足一片的尾巴必须保留（不丢帧）。
// chunkBytes<=0 时用 DefaultChunkBytes。空输入返回 nil。
func SplitPCM(pcm []byte, chunkBytes int) [][]byte {
	if len(pcm) == 0 {
		return nil
	}
	if chunkBytes <= 0 {
		chunkBytes = DefaultChunkBytes
	}
	chunks := make([][]byte, 0, (len(pcm)+chunkBytes-1)/chunkBytes)
	for off := 0; off < len(pcm); off += chunkBytes {
		end := off + chunkBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		chunks = append(chunks, pcm[off:end])
	}
	return chunks
}

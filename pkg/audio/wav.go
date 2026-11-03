package audio

import (
	"bytes"
	"encoding/binary"
	"math"
)

// WavBytes 把 24kHz/s16le/mono PCM 包成最小 WAV（单声道 16bit），供试听与口播下载。
// 蓝本 bridge.py tts_preview 直接返回 audio.wav_bytes(pcm)，本函数保持同样的采样口径。
func WavBytes(pcm []byte) []byte {
	const bitsPerSample = 16
	dataSize := len(pcm)
	buf := bytes.NewBuffer(make([]byte, 0, 44+dataSize))
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(buf, binary.LittleEndian, uint16(Channels))
	_ = binary.Write(buf, binary.LittleEndian, uint32(SampleRate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(SampleRate*Channels*BytesPerSample))
	_ = binary.Write(buf, binary.LittleEndian, uint16(Channels*BytesPerSample))
	_ = binary.Write(buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(dataSize))
	buf.Write(pcm)
	return buf.Bytes()
}

// SilencePCM 生成 ms 毫秒的静音 PCM，供 fake 合成器/解码器产出可播放的合法数据。
func SilencePCM(ms int64) []byte {
	n := BytesForMS(ms)
	if n <= 0 {
		return nil
	}
	return make([]byte, n)
}

// TonePCM 生成 ms 毫秒的 440Hz 正弦波（仅测试/试听占位，不用于真实直播）。
func TonePCM(ms int64) []byte {
	n := BytesForMS(ms)
	if n <= 0 {
		return nil
	}
	out := make([]byte, n)
	samples := n / BytesPerSample
	for i := 0; i < samples; i++ {
		v := int16(math.Round(6000 * math.Sin(2*math.Pi*440*float64(i)/SampleRate)))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

// PCMDurationMS 返回 PCM 的整毫秒时长。
func PCMDurationMS(pcm []byte) int64 {
	return MSForBytes(len(pcm))
}

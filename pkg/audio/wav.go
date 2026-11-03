package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
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

// ErrWavFormat 表示 wav 字节不符合 24kHz/s16le/mono PCM 口径。
var ErrWavFormat = errors.New("audio: wav 不是 24kHz/s16le/mono PCM")

// PCMFromWav 从 RIFF/WAVE 字节中提取裸 PCM。fail-closed：
// 仅接受 PCM16 单声道 24kHz（与 SampleRate/Channels/BytesPerSample 一致），
// 其他编码/采样率一律报错，绝不静默重采样。
func PCMFromWav(wav []byte) ([]byte, error) {
	if len(wav) < 44 {
		return nil, fmt.Errorf("%w: 长度不足 %d", ErrWavFormat, len(wav))
	}
	if !bytes.Equal(wav[0:4], []byte("RIFF")) || !bytes.Equal(wav[8:12], []byte("WAVE")) {
		return nil, fmt.Errorf("%w: 缺少 RIFF/WAVE 头", ErrWavFormat)
	}
	pos := 12
	var data []byte
	for pos+8 <= len(wav) {
		id := string(wav[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(wav[pos+4 : pos+8]))
		body := wav[pos+8:]
		if len(body) < size {
			size = len(body)
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("%w: fmt 块过短", ErrWavFormat)
			}
			format := binary.LittleEndian.Uint16(body[0:2])
			channels := binary.LittleEndian.Uint16(body[2:4])
			rate := binary.LittleEndian.Uint32(body[4:8])
			bits := binary.LittleEndian.Uint16(body[14:16])
			if format != 1 || channels != uint16(Channels) || int(rate) != SampleRate || bits != uint16(BytesPerSample*8) {
				return nil, fmt.Errorf("%w: format=%d channels=%d rate=%d bits=%d",
					ErrWavFormat, format, channels, rate, bits)
			}
		case "data":
			data = body[:size]
		}
		pos += 8 + size
		if size%2 == 1 { // RIFF 块按 2 字节对齐
			pos++
		}
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: 缺少 data 块", ErrWavFormat)
	}
	return data, nil
}

package voice

import (
	"encoding/binary"
	"math"
	"time"
)

// VAD 是可替换回合判定端口；首版默认 CPU 能量实现，sherpa 端点可在后续适配。
type VAD interface {
	Push(pcm []byte) VADState
	SpeechDuration() time.Duration
}

type VADState struct {
	Speech   bool
	Endpoint bool
}

// EnergyVAD 以噪声绝对下限做保守判定；生产阈值必须按麦克风实测调整。
type EnergyVAD struct {
	cfg       VADConfig
	speech    time.Duration
	silence   time.Duration
	candidate time.Duration
	active    bool
}

func NewEnergyVAD(cfg VADConfig) *EnergyVAD { return &EnergyVAD{cfg: cfg} }

func (v *EnergyVAD) Push(pcm []byte) VADState {
	if len(pcm) < 2 {
		return VADState{}
	}
	var sum float64
	samples := len(pcm) / 2
	for i := 0; i+1 < len(pcm); i += 2 {
		x := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) / 32768
		sum += x * x
	}
	rms := math.Sqrt(sum / float64(samples))
	db := -120.0
	if rms > 0 {
		db = 20 * math.Log10(rms)
	}
	d := time.Duration(samples) * time.Second / 16000
	if db >= v.cfg.AbsoluteDBFS {
		v.candidate += d
		v.silence = 0
		if v.candidate >= v.cfg.MinStartSpeech {
			v.active = true
		}
	} else {
		v.candidate = 0
		v.silence += d
	}
	if v.active {
		v.speech += d
	}
	endpoint := v.active && v.silence >= v.cfg.Silence
	if endpoint {
		v.active = false
	}
	return VADState{Speech: db >= v.cfg.AbsoluteDBFS, Endpoint: endpoint}
}
func (v *EnergyVAD) SpeechDuration() time.Duration { return v.speech }

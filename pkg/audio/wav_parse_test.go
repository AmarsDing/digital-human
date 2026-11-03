package audio

import (
	"errors"
	"testing"
)

func TestPCMFromWavRoundTrip(t *testing.T) {
	pcm := TonePCM(100)
	out, err := PCMFromWav(WavBytes(pcm))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != len(pcm) {
		t.Fatalf("pcm len=%d want %d", len(out), len(pcm))
	}
}

func TestPCMFromWavRejectsBadInput(t *testing.T) {
	if _, err := PCMFromWav([]byte("short")); !errors.Is(err, ErrWavFormat) {
		t.Fatalf("short: %v", err)
	}
	if _, err := PCMFromWav(make([]byte, 64)); !errors.Is(err, ErrWavFormat) {
		t.Fatalf("no RIFF: %v", err)
	}
	// 48kHz 立体声 wav 必须被拒（不重采样）。
	hdr := WavBytes(TonePCM(10))
	hdr[22] = 2 // channels=2
	if _, err := PCMFromWav(hdr); !errors.Is(err, ErrWavFormat) {
		t.Fatalf("stereo: %v", err)
	}
}

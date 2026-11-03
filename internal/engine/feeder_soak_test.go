package engine

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// countingSender 记录帧数与字节数，用于长跑漂移统计。
type countingSender struct {
	frames int
	bytes  int
}

func (s *countingSender) SendPCM(_ context.Context, pcm []byte) error {
	s.frames++
	s.bytes += len(pcm)
	return nil
}

// soakMinutes 读取 DH_SOAK_MINUTES；未设置或 <=0 时跳过（长跑不进入 CI 默认路径）。
func soakMinutes(t *testing.T, env string) int {
	t.Helper()
	v, _ := strconv.Atoi(os.Getenv(env))
	if v <= 0 {
		t.Skipf("设置 %s（分钟）后运行长跑", env)
	}
	return v
}

// TestSoakFeederDriftRealClock —— 真机真实时钟长跑（D 批：1 小时）。
// 以 100ms×4800B 分片连续喂流 N 分钟（默认由 DH_SOAK_MINUTES 给出，建议 60），
// 断言：不跳帧（frames*chunkBytes == bytes）、累计漂移 |Drift| < 250ms、无告警风暴。
// 运行：DH_SOAK_MINUTES=60 go test ./internal/engine/ -run TestSoakFeederDriftRealClock -count=1 -v -timeout 90m
func TestSoakFeederDriftRealClock(t *testing.T) {
	minutes := soakMinutes(t, "DH_SOAK_MINUTES")
	ctx := context.Background()
	sender := &countingSender{}

	var maxLag time.Duration
	var warns int
	feeder := NewFeeder(FeedConfig{
		ChunkMS: 100,
		OnWarn:  func(time.Duration) { warns++ },
		OnLate: func(d time.Duration) {
			if -d > maxLag {
				maxLag = -d
			}
		},
		WarnWindow: 10 * time.Second,
	})

	deadline := time.Now().Add(time.Duration(minutes) * time.Minute)
	// 每轮喂 30s 语音（300 帧），轮间静默 200ms，模拟真实“句间停顿”。
	pcm := make([]byte, 30*1000*48) // 30s × 48B/ms
	var totalDrift time.Duration
	var rounds, totalLate, totalFrames int
	worst := time.Duration(0)
	for time.Now().Before(deadline) {
		res, err := feeder.Feed(ctx, sender, pcm, nil)
		if err != nil {
			t.Fatalf("第 %d 轮喂流失败: %v", rounds, err)
		}
		rounds++
		totalLate += res.Late
		totalFrames += res.Frames
		totalDrift += res.Drift
		if res.MaxLag > worst {
			worst = res.MaxLag
		}
		time.Sleep(200 * time.Millisecond)
	}

	if sender.frames != totalFrames {
		t.Fatalf("跳帧: sender=%d feeder=%d", sender.frames, totalFrames)
	}
	avgDrift := totalDrift / time.Duration(rounds)
	t.Logf("真实时钟长跑 %d 分钟: 轮次=%d 总帧=%d 总字节=%d 迟到帧=%d 告警=%d 单轮最大滞后=%v 平均轮末漂移=%v",
		minutes, rounds, totalFrames, sender.bytes, totalLate, warns, worst, avgDrift)
	if worst > 250*time.Millisecond {
		t.Fatalf("单轮最大滞后 %v 超过 250ms 阈值", worst)
	}
}

// TestSoakFeederDriftAccelerated —— 加速时钟长跑（D 批：8h 等效 tick 数）。
// 用 1ms 分片（同一 Pacer/Feeder 代码路径，仅刻度压缩 100x），累计 288000 tick
// ≈ 8h×3600s×10tick/s 的节拍数，墙钟约 5 分钟。断言 tick 数完整、无跳帧、
// 漂移有界。与真实时钟长跑互补：它证明节拍计数与漂移校正在大基数下不发散，
// 不证明 8h 墙钟下的 OS 定时器行为（那部分由 DH_SOAK_MINUTES=28800 的真机长跑负责）。
// 运行：DH_SOAK_ACCEL=1 go test ./internal/engine/ -run TestSoakFeederDriftAccelerated -count=1 -v -timeout 20m
func TestSoakFeederDriftAccelerated(t *testing.T) {
	if os.Getenv("DH_SOAK_ACCEL") == "" {
		t.Skip("设置 DH_SOAK_ACCEL=1 后运行加速时钟长跑")
	}
	ctx := context.Background()
	sender := &countingSender{}

	const targetTicks = 288000 // 8h 等效（100ms tick × 8×3600×10）
	var maxLag time.Duration
	feeder := NewFeeder(FeedConfig{
		ChunkMS: 1, // 刻度压缩 100x：1ms 分片 = 48B
		Coarse:  0, // 保持默认混合等待
		OnLate: func(d time.Duration) {
			if -d > maxLag {
				maxLag = -d
			}
		},
		WriteTimeout: 3 * time.Second,
	})

	// 每轮 300 tick（=加速后的“30s 语音”），跑满 targetTicks。
	pcm := make([]byte, 300*48)
	ticks := 0
	rounds := 0
	started := time.Now()
	for ticks < targetTicks {
		res, err := feeder.Feed(ctx, sender, pcm, nil)
		if err != nil {
			t.Fatalf("第 %d 轮喂流失败: %v", rounds, err)
		}
		ticks += res.Frames
		rounds++
	}
	wall := time.Since(started)
	t.Logf("加速时钟长跑: tick=%d（≈8h 等效节拍）轮次=%d 墙钟=%v 单轮最大滞后=%v", ticks, rounds, wall, maxLag)
	if sender.frames != ticks {
		t.Fatalf("跳帧: sender=%d feeder=%d", sender.frames, ticks)
	}
	// 1ms 刻度下 Windows 定时器噪声相对更大，阈值放宽到 50ms/轮。
	if maxLag > 50*time.Millisecond {
		t.Fatalf("加速长跑单轮最大滞后 %v 超阈值", maxLag)
	}
}

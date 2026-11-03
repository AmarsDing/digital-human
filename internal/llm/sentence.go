package llm

import "strings"

// 分句规则复刻蓝本 src/llm.py:29-31（2026-10-29 口径）：
// 强停顿立即分句；弱停顿只在已积累 >= SoftMin 字时分；缓冲超过 HardMax 字强制放出，
// 且回退到最近的弱标点/空格，不硬按第 N 字切。换行不当句末（模型排版换行会切在句中）。
const (
	strongEnds = "。！？!?；;"
	softEnds   = "，,、"

	// DefaultSoftMin 弱停顿最小积累字数（蓝本 soft_min=10）。
	DefaultSoftMin = 10
	// DefaultHardMax 无标点长串兜底字数（蓝本 2026-10-29: 30→45，给标点留回退区间）。
	DefaultHardMax = 45
)

// SentenceSplitter 增量分句器：Feed(增量文本) → 本轮凑齐的完整句子列表；Flush 收尾。
// 字数按 rune 计（等价蓝本 Python len(str)）。
type SentenceSplitter struct {
	buf     []rune
	softMin int
	hardMax int
}

func NewSentenceSplitter() *SentenceSplitter {
	return &SentenceSplitter{softMin: DefaultSoftMin, hardMax: DefaultHardMax}
}

// Feed 喂入增量文本，返回本轮凑齐的完整句子（可能为空）。
func (s *SentenceSplitter) Feed(delta string) []string {
	s.buf = append(s.buf, []rune(delta)...)
	var out []string
	for {
		cut := -1
		for i, ch := range s.buf {
			if strings.ContainsRune(strongEnds, ch) {
				cut = i
				break
			}
			if strings.ContainsRune(softEnds, ch) && i+1 >= s.softMin {
				cut = i
				break
			}
		}
		if cut < 0 {
			if len(s.buf) > s.hardMax {
				// 兜底强制放行：回退到最近弱标点/空格（蓝本 llm.py:74-83）。
				n := s.hardMax
				cutAt := -1
				for _, c := range []rune(softEnds) {
					if p := lastIndexRune(s.buf, c, n); p > cutAt {
						cutAt = p
					}
				}
				if cutAt < 6 {
					if sp := lastIndexRune(s.buf, ' ', n); sp >= 6 {
						cutAt = sp
					} else {
						cutAt = n - 1
					}
				}
				if piece := strings.TrimSpace(string(s.buf[:cutAt+1])); piece != "" {
					out = append(out, piece)
				}
				s.buf = s.buf[cutAt+1:]
				continue
			}
			break
		}
		if piece := strings.TrimSpace(string(s.buf[:cut+1])); piece != "" {
			out = append(out, piece)
		}
		s.buf = s.buf[cut+1:]
	}
	return out
}

// Flush 收尾：返回缓冲区剩余文本（去首尾空白），并清空缓冲。
func (s *SentenceSplitter) Flush() string {
	tail := strings.TrimSpace(string(s.buf))
	s.buf = nil
	return tail
}

// lastIndexRune 在 buf[:n] 窗口内找 c 最后一次出现的位置；找不到返回 -1。
func lastIndexRune(buf []rune, c rune, n int) int {
	if n > len(buf) {
		n = len(buf)
	}
	for i := n - 1; i >= 0; i-- {
		if buf[i] == c {
			return i
		}
	}
	return -1
}

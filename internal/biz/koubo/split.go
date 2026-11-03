package koubo

import (
	"regexp"
	"strings"
)

// 与蓝本 _PROTECT 一致：URL / 邮箱 / 时间 / 版本 / 数字 / 英文单词不能被切断。
var protectRE = regexp.MustCompile(
	`https?://[^\s，。；！？、）】」』"']+` +
		`|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}` +
		`|\d{1,2}:\d{2}(?::\d{2})?` +
		`|v?\d+\.\d+(?:\.\d+)*%?` +
		`|\d+(?:,\d{3})*(?:\.\d+)?%?` +
		`|[A-Za-z][A-Za-z0-9'._+\-/]*`,
)

const clausePunct = "。！？!?；;，,、：:"

func safeSpans(s string) [][2]int {
	ms := protectRE.FindAllStringIndex(s, -1)
	out := make([][2]int, 0, len(ms))
	for _, m := range ms {
		out = append(out, [2]int{m[0], m[1]})
	}
	return out
}

func inside(spans [][2]int, pos int) bool {
	for _, sp := range spans {
		if sp[0] < pos && pos < sp[1] {
			return true
		}
	}
	return false
}

func clauses(text string) []string {
	spans := safeSpans(text)
	out := []string{}
	start := 0
	for i, ch := range text {
		if strings.ContainsRune(clausePunct, ch) && !inside(spans, i+1) {
			if piece := strings.TrimSpace(text[start : i+1]); piece != "" {
				out = append(out, piece)
			}
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(text[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

func splitLong(sent string, maxChars int) []string {
	spans := safeSpans(sent)
	out := []string{}
	for len(sent) > maxChars {
		window := sent
		if maxChars+1 < len(window) {
			window = sent[:maxChars+1]
		}
		cut := -1
		for pos, r := range window {
			if pos+len(string(r)) >= maxChars {
				break
			}
			if pos > maxChars/2 && strings.ContainsRune(clausePunct, r) && !inside(spans, pos+1) {
				cut = pos + len(string(r))
				break
			}
		}
		if cut < 0 {
			cut = SafePos(sent, maxChars)
		}
		if cut <= 0 {
			break
		}
		out = append(out, strings.TrimSpace(sent[:cut]))
		sent = strings.TrimSpace(sent[cut:])
		spans = safeSpans(sent)
	}
	if sent != "" {
		out = append(out, sent)
	}
	return out
}

// SplitText 复刻蓝本 split_text：标点优先，字数只是软目标（允许超 35%），尾段过短并入上一段。
func SplitText(text string, maxChars int) []string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil
	}
	if maxChars < 20 {
		maxChars = 20
	}
	soft := int(float64(maxChars) * 1.35)

	segs := []string{}
	buf := ""
	for _, c := range clauses(text) {
		if buf == "" {
			buf = c
			continue
		}
		if len(buf)+len(c) <= soft {
			buf += c
		} else {
			segs = append(segs, buf)
			buf = c
		}
	}
	if buf != "" {
		segs = append(segs, buf)
	}

	out := []string{}
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		limit := maxChars * 3 / 2
		if limit < maxChars {
			limit = maxChars
		}
		if len(s) <= limit {
			out = append(out, s)
		} else {
			out = append(out, splitLong(s, maxChars)...)
		}
	}
	// 尾段过短就并入上一段。
	if len(out) >= 2 && len(out[len(out)-1]) < max(10, maxChars/4) {
		out[len(out)-2] += out[len(out)-1]
		out = out[:len(out)-1]
	}
	return out
}

// SafePos 把切分点挪到 token 边界外，避免把 URL/英文/数字拦腰切断。
func SafePos(text string, pos int) int {
	if pos < 1 {
		pos = 1
	}
	if pos > len(text)-1 {
		pos = len(text) - 1
	}
	for _, sp := range safeSpans(text) {
		if sp[0] < pos && pos < sp[1] {
			if sp[1]-pos <= pos-sp[0] {
				return sp[1]
			}
			return sp[0]
		}
	}
	return pos
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

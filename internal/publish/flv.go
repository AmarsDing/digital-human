package publish

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// FLV 读取器：解析 ffmpeg -f flv pipe:1 输出，产出可经 RTMP 发布的 tag。
// 只实现 FLV v1 头 + tag 序列（type 8 音频 / 9 视频 / 18 脚本数据）。

// Tag 是一个 FLV tag。
type Tag struct {
	Type      byte   // 8=audio 9=video 18=script
	Timestamp uint32 // 毫秒
	Data      []byte
}

// ReadFLVHeader 读取并校验 FLV 头（"FLV" 0x01 flags + 4 字节 dataoffset + prevTagSize0）。
func ReadFLVHeader(r io.Reader) error {
	var hdr [13]byte // 9 字节头 + 4 字节 PreviousTagSize0
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return fmt.Errorf("flv: 读头失败: %w", err)
	}
	if hdr[0] != 'F' || hdr[1] != 'L' || hdr[2] != 'V' || hdr[3] != 0x01 {
		return fmt.Errorf("flv: 非法魔数 %q", hdr[:3])
	}
	off := binary.BigEndian.Uint32(hdr[5:9])
	if off != 9 {
		// 跳过扩展头（实践中 ffmpeg 恒为 9）
		if _, err := io.CopyN(io.Discard, r, int64(off-9)); err != nil {
			return fmt.Errorf("flv: 跳扩展头失败: %w", err)
		}
		if _, err := io.ReadFull(r, hdr[9:13]); err != nil {
			return fmt.Errorf("flv: 读 prevTagSize0 失败: %w", err)
		}
	}
	return nil
}

// ReadFLVTag 读取一个 tag（含其后的 PreviousTagSize）。EOF 表示流正常结束。
func ReadFLVTag(r *bufio.Reader) (Tag, error) {
	var h [11]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Tag{}, err
	}
	t := Tag{Type: h[0]}
	sz := uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])
	ts := uint32(h[4])<<16 | uint32(h[5])<<8 | uint32(h[6]) | uint32(h[7])<<24
	t.Timestamp = ts
	if sz > 16<<20 {
		return Tag{}, fmt.Errorf("flv: tag 过大 %d", sz)
	}
	t.Data = make([]byte, sz)
	if _, err := io.ReadFull(r, t.Data); err != nil {
		return Tag{}, fmt.Errorf("flv: 读 tag 数据失败: %w", err)
	}
	var prev [4]byte
	if _, err := io.ReadFull(r, prev[:]); err != nil {
		return Tag{}, fmt.Errorf("flv: 读 prevTagSize 失败: %w", err)
	}
	return t, nil
}

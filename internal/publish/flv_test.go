package publish

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
)

// buildFLV 构造最小合法 FLV 流（头 + 若干 tag）。
func buildFLV(tags ...Tag) []byte {
	var b bytes.Buffer
	b.Write([]byte{'F', 'L', 'V', 0x01, 0x05, 0, 0, 0, 9})
	b.Write([]byte{0, 0, 0, 0}) // PreviousTagSize0
	prev := 0
	_ = prev
	for _, t := range tags {
		var h [11]byte
		h[0] = t.Type
		sz := len(t.Data)
		h[1] = byte(sz >> 16)
		h[2] = byte(sz >> 8)
		h[3] = byte(sz)
		h[4] = byte(t.Timestamp >> 16)
		h[5] = byte(t.Timestamp >> 8)
		h[6] = byte(t.Timestamp)
		h[7] = byte(t.Timestamp >> 24)
		b.Write(h[:])
		b.Write(t.Data)
		var p [4]byte
		binary.BigEndian.PutUint32(p[:], uint32(11+sz))
		b.Write(p[:])
	}
	return b.Bytes()
}

func TestReadFLV(t *testing.T) {
	tags := []Tag{
		{Type: 18, Timestamp: 0, Data: []byte("meta")},
		{Type: 9, Timestamp: 33, Data: []byte{0x17, 0x01}},
		{Type: 8, Timestamp: 40, Data: []byte{0xaf, 0x01}},
	}
	buf := buildFLV(tags...)
	r := bufio.NewReader(bytes.NewReader(buf))
	if err := ReadFLVHeader(r); err != nil {
		t.Fatal(err)
	}
	for i, want := range tags {
		got, err := ReadFLVTag(r)
		if err != nil {
			t.Fatalf("tag %d: %v", i, err)
		}
		if got.Type != want.Type || got.Timestamp != want.Timestamp || !bytes.Equal(got.Data, want.Data) {
			t.Fatalf("tag %d: got %+v want %+v", i, got, want)
		}
	}
}

func TestReadFLVBadMagic(t *testing.T) {
	r := bufio.NewReader(bytes.NewReader([]byte("NOT_FLV_CONTENT")))
	if err := ReadFLVHeader(r); err == nil {
		t.Fatal("want error for bad magic")
	}
}

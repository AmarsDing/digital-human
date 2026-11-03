package publish

// 进程内假 RTMP 服务器：实现服务端握手 + chunk 读 + AMF0 命令应答，
// 断言 publish 命令携带的 stream key，并接收后续 metadata/av tag。

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRTMPServer struct {
	ln net.Listener
	mu sync.Mutex
	// 观测结果
	gotKey    string
	gotMeta   int
	gotVideo  int
	gotAudio  int
	denyKey   string // 非空时对该 key 回 Publish.Denied
	connCount int
}

func newFakeRTMPServer(t *testing.T) *fakeRTMPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeRTMPServer{ln: ln}
	go s.acceptLoop()
	return s
}

func (s *fakeRTMPServer) addr() string { return s.ln.Addr().String() }
func (s *fakeRTMPServer) url() string {
	return "rtmp://" + s.ln.Addr().String() + "/live"
}
func (s *fakeRTMPServer) close() { _ = s.ln.Close() }

func (s *fakeRTMPServer) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.connCount++
		s.mu.Unlock()
		go s.handle(c)
	}
}

// srvReadMsg 读客户端消息（客户端 chunk size 由 Set Chunk Size 控制，默认 128）。
func srvReadMessage(br *bufio.Reader, inChunk *int) (inMessage, error) {
	parts := map[uint32]*struct {
		buf    []byte
		typeID byte
		sid    uint32
		want   int
	}{}
	for {
		b0, err := br.ReadByte()
		if err != nil {
			return inMessage{}, err
		}
		fmtv := b0 >> 6
		csid := uint32(b0 & 0x3F)
		if csid == 0 {
			b1, _ := br.ReadByte()
			csid = uint32(b1) + 64
		}
		p := parts[csid]
		switch fmtv {
		case 0:
			var mh [11]byte
			if _, err := io.ReadFull(br, mh[:]); err != nil {
				return inMessage{}, err
			}
			p = &struct {
				buf    []byte
				typeID byte
				sid    uint32
				want   int
			}{typeID: mh[6], want: int(mh[3])<<16 | int(mh[4])<<8 | int(mh[5]),
				sid: binary.LittleEndian.Uint32(mh[7:11])}
			parts[csid] = p
		case 3:
			if p == nil {
				return inMessage{}, errors.New("srv: fmt3 无前置")
			}
		default:
			return inMessage{}, fmt.Errorf("srv: 未实现 fmt%d", fmtv)
		}
		need := p.want - len(p.buf)
		if need > *inChunk {
			need = *inChunk
		}
		chunk := make([]byte, need)
		if _, err := io.ReadFull(br, chunk); err != nil {
			return inMessage{}, err
		}
		p.buf = append(p.buf, chunk...)
		if len(p.buf) >= p.want {
			if p.typeID == 0x01 {
				*inChunk = int(binary.BigEndian.Uint32(p.buf))
				delete(parts, csid)
				continue
			}
			return inMessage{typeID: p.typeID, streamID: p.sid, payload: p.buf}, nil
		}
	}
}

func srvWriteChunk(w io.Writer, csid uint32, typeID byte, streamID uint32, payload []byte) error {
	hdr := []byte{byte(csid)}
	var mh [11]byte
	put24(mh[3:6], uint32(len(payload)))
	mh[6] = typeID
	binary.LittleEndian.PutUint32(mh[7:11], streamID)
	if _, err := w.Write(append(hdr, mh[:]...)); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func srvCommand(w io.Writer, csid uint32, streamID uint32, vals ...[]byte) error {
	var p []byte
	for _, v := range vals {
		p = append(p, v...)
	}
	return srvWriteChunk(w, csid, 20, streamID, p)
}

func (s *fakeRTMPServer) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	// 握手：读 C0C1，回 S0S1S2，读 C2
	var c0c1 [1537]byte
	if _, err := io.ReadFull(br, c0c1[:]); err != nil {
		return
	}
	s1 := make([]byte, 1536)
	var s0s1s2 []byte
	s0s1s2 = append(s0s1s2, 0x03)
	s0s1s2 = append(s0s1s2, s1...)
	s0s1s2 = append(s0s1s2, c0c1[1:]...) // S2 = C1 回显
	if _, err := c.Write(s0s1s2); err != nil {
		return
	}
	var c2 [1536]byte
	if _, err := io.ReadFull(br, c2[:]); err != nil {
		return
	}
	inChunk := 128
	for {
		msg, err := srvReadMessage(br, &inChunk)
		if err != nil {
			return
		}
		if msg.typeID != 20 {
			// metadata/audio/video
			s.mu.Lock()
			switch msg.typeID {
			case 18:
				s.gotMeta++
			case 9:
				s.gotVideo++
			case 8:
				s.gotAudio++
			}
			s.mu.Unlock()
			continue
		}
		ar := &amfReader{r: bytes.NewReader(msg.payload)}
		name, _ := ar.read()
		txn, _ := ar.read()
		txnf, _ := txn.(float64)
		switch name {
		case "connect":
			_ = srvCommand(c, 3, 0,
				amfString("_result"), amfNumber(txnf),
				amfObject(map[string]any{"fmsVer": "FMS/3,0", "capabilities": 31.0}),
				amfObject(map[string]any{"level": "status", "code": "NetConnection.Connect.Success"}))
		case "createStream":
			_, _ = ar.read() // null
			_ = srvCommand(c, 3, 0,
				amfString("_result"), amfNumber(txnf), amfNull(), amfNumber(1))
		case "publish":
			_, _ = ar.read() // null
			keyv, _ := ar.read()
			key, _ := keyv.(string)
			s.mu.Lock()
			s.gotKey = key
			deny := s.denyKey != "" && s.denyKey == key
			s.mu.Unlock()
			code := "NetStream.Publish.Start"
			if deny {
				code = "NetStream.Publish.Denied"
			}
			_ = srvCommand(c, 3, msg.streamID,
				amfString("onStatus"), amfNumber(0), amfNull(),
				amfObject(map[string]any{"level": "status", "code": code}))
		}
	}
}

func TestDialRTMPPublishFlow(t *testing.T) {
	s := newFakeRTMPServer(t)
	defer s.close()
	c, err := DialRTMP(s.url(), "sk_test_key_123", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SendMeta(720, 1280, 30); err != nil {
		t.Fatal(err)
	}
	if err := c.SendTag(Tag{Type: 9, Timestamp: 33, Data: []byte{0x17, 0x00}}); err != nil {
		t.Fatal(err)
	}
	if err := c.SendTag(Tag{Type: 8, Timestamp: 40, Data: []byte{0xaf, 0x00}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gotKey != "sk_test_key_123" {
		t.Fatalf("server got key %q", s.gotKey)
	}
	if s.gotMeta != 1 || s.gotVideo != 1 || s.gotAudio != 1 {
		t.Fatalf("meta=%d video=%d audio=%d", s.gotMeta, s.gotVideo, s.gotAudio)
	}
}

func TestDialRTMPRejected(t *testing.T) {
	s := newFakeRTMPServer(t)
	defer s.close()
	s.denyKey = "bad_key"
	_, err := DialRTMP(s.url(), "bad_key", 3*time.Second)
	if !errors.Is(err, ErrPublishRejected) {
		t.Fatalf("want ErrPublishRejected, got %v", err)
	}
}

func TestDialRTMPBadURL(t *testing.T) {
	if _, err := DialRTMP("http://x/live", "k", time.Second); err == nil {
		t.Fatal("want scheme error")
	}
	if _, err := DialRTMP("rtmp://x", "k", time.Second); err == nil ||
		!strings.Contains(err.Error(), "app") {
		t.Fatal("want missing app error")
	}
}

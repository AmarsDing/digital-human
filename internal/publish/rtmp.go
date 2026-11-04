package publish

// 最小 RTMP 发布客户端：握手 → connect(app) → createStream → publish(key) →
// 转发 FLV tag（metadata/audio/video）。只实现发布所需子集，不含播放/RTMPS。

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"strings"
	"time"
)

// ErrPublishRejected 表示服务器拒绝发布（NetConnection.Connect.Rejected 等），
// 对应状态机 rejected 终态（常见原因：stream key 失效）。
var ErrPublishRejected = errors.New("rtmp: 发布被服务器拒绝")

// RTMPClient 是一个最小发布端。
type RTMPClient struct {
	conn      net.Conn
	br        *bufio.Reader
	chunkSize int // 出站 chunk 尺寸
	inChunk   int // 入站 chunk 尺寸（服务器 Set Chunk Size 控制）
	streamID  uint32
}

// rtmpTarget 拆解推流地址。
type rtmpTarget struct {
	host, app, key, tcURL string
}

func parseRTMP(rawURL, key string) (rtmpTarget, error) {
	normalized, err := normalizeTarget(Target{RTMPURL: rawURL, Key: key})
	if err != nil {
		return rtmpTarget{}, err
	}
	u, _ := url.Parse(normalized.RTMPURL)
	port := u.Port()
	if port == "" {
		port = "1935"
	}
	app := strings.TrimPrefix(u.Path, "/")
	return rtmpTarget{
		host:  net.JoinHostPort(u.Hostname(), port),
		app:   app,
		key:   normalized.Key,
		tcURL: normalized.RTMPURL,
	}, nil
}

// DialRTMP 建立连接并完成握手与 publish。全程死线由 timeout 约束。
func DialRTMP(rawURL, key string, timeout time.Duration) (*RTMPClient, error) {
	t, err := parseRTMP(rawURL, key)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", t.host, timeout)
	if err != nil {
		return nil, classifyNetworkError(err)
	}
	c := &RTMPClient{conn: conn, br: bufio.NewReader(conn), chunkSize: 4096,
		inChunk: 128}
	c.conn.SetDeadline(time.Now().Add(timeout))
	if err := c.handshake(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.writeSetChunkSize(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.connect(t); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.createStreamAndPublish(t); err != nil {
		conn.Close()
		return nil, err
	}
	c.conn.SetDeadline(time.Time{})
	return c, nil
}

func (c *RTMPClient) handshake() error {
	var c1 [1536]byte
	binary.BigEndian.PutUint32(c1[0:4], uint32(time.Now().Unix()))
	if _, err := rand.Read(c1[8:]); err != nil {
		return err
	}
	if _, err := c.conn.Write(append([]byte{0x03}, c1[:]...)); err != nil {
		return fmt.Errorf("rtmp: 发 C0C1 失败: %w", err)
	}
	var s0s1s2 [3073]byte
	if _, err := io.ReadFull(c.br, s0s1s2[:]); err != nil {
		return fmt.Errorf("rtmp: 读 S0S1S2 失败: %w", err)
	}
	if s0s1s2[0] != 0x03 {
		return fmt.Errorf("rtmp: 服务器版本异常 %d", s0s1s2[0])
	}
	c2 := s0s1s2[1:1537] // C2 = S1 回显
	if _, err := c.conn.Write(c2); err != nil {
		return fmt.Errorf("rtmp: 发 C2 失败: %w", err)
	}
	return nil
}

// ---- AMF0 最小编解码 ----

func amfString(s string) []byte {
	b := make([]byte, 0, len(s)+3)
	b = append(b, 0x02, byte(len(s)>>8), byte(len(s)))
	return append(b, s...)
}

func amfNumber(f float64) []byte {
	b := []byte{0x00, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint64(b[1:], math.Float64bits(f))
	return b
}

func amfNull() []byte { return []byte{0x05} }

func amfBool(v bool) []byte {
	if v {
		return []byte{0x01, 1}
	}
	return []byte{0x01, 0}
}

func amfObject(kv map[string]any) []byte {
	b := []byte{0x03}
	for k, v := range kv {
		b = append(b, byte(len(k)>>8), byte(len(k)))
		b = append(b, k...)
		b = append(b, amfAny(v)...)
	}
	return append(b, 0, 0, 0x09)
}

func amfECMA(kv map[string]any) []byte {
	b := []byte{0x08, 0, 0, 0, byte(len(kv))}
	for k, v := range kv {
		b = append(b, byte(len(k)>>8), byte(len(k)))
		b = append(b, k...)
		b = append(b, amfAny(v)...)
	}
	return append(b, 0, 0, 0x09)
}

func amfAny(v any) []byte {
	switch t := v.(type) {
	case string:
		return amfString(t)
	case float64:
		return amfNumber(t)
	case int:
		return amfNumber(float64(t))
	case bool:
		return amfBool(t)
	case nil:
		return amfNull()
	case map[string]any:
		return amfObject(t)
	default:
		return amfNull()
	}
}

type amfReader struct {
	r   *bytes.Reader
	off int
}

func (a *amfReader) read() (any, error) {
	t, err := a.r.ReadByte()
	if err != nil {
		return nil, err
	}
	switch t {
	case 0x00:
		var b [8]byte
		if _, err := io.ReadFull(a.r, b[:]); err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b[:])), nil
	case 0x01:
		v, err := a.r.ReadByte()
		return v == 1, err
	case 0x02:
		var b [2]byte
		if _, err := io.ReadFull(a.r, b[:]); err != nil {
			return nil, err
		}
		n := int(b[0])<<8 | int(b[1])
		s := make([]byte, n)
		if _, err := io.ReadFull(a.r, s); err != nil {
			return nil, err
		}
		return string(s), nil
	case 0x05:
		return nil, nil
	case 0x03, 0x08: // object / ecma array
		if t == 0x08 {
			var b [4]byte
			if _, err := io.ReadFull(a.r, b[:]); err != nil {
				return nil, err
			}
		}
		obj := map[string]any{}
		for {
			var b [2]byte
			if _, err := io.ReadFull(a.r, b[:]); err != nil {
				return nil, err
			}
			n := int(b[0])<<8 | int(b[1])
			if n == 0 { // 可能是结束标记 00 00 09
				m, err := a.r.ReadByte()
				if err != nil {
					return nil, err
				}
				if m == 0x09 {
					return obj, nil
				}
				return nil, fmt.Errorf("amf0: 非法对象结束 %x", m)
			}
			k := make([]byte, n)
			if _, err := io.ReadFull(a.r, k); err != nil {
				return nil, err
			}
			v, err := a.read()
			if err != nil {
				return nil, err
			}
			obj[string(k)] = v
		}
	default:
		return nil, fmt.Errorf("amf0: 未支持类型 %x", t)
	}
}

// ---- chunk 写出 ----

// writeChunk 写单个消息（自动按 chunkSize 切片，fmt0 首片 + fmt3 续片）。
func (c *RTMPClient) writeChunk(csid uint32, typeID byte, streamID uint32, ts uint32, payload []byte) error {
	var basic []byte
	switch {
	case csid < 64:
		basic = []byte{byte(0<<6) | byte(csid)}
	case csid < 320:
		basic = []byte{byte(0 << 6), byte(csid - 64)}
	default:
		return fmt.Errorf("rtmp: csid 过大 %d", csid)
	}
	var mh [11]byte
	put24(mh[0:3], ts)
	put24(mh[3:6], uint32(len(payload)))
	mh[6] = typeID
	binary.LittleEndian.PutUint32(mh[7:11], streamID)
	if _, err := c.conn.Write(append(basic, mh[:]...)); err != nil {
		return err
	}
	for off := 0; off < len(payload); {
		n := c.chunkSize
		if n > len(payload)-off {
			n = len(payload) - off
		}
		if off > 0 { // fmt3 续片
			if _, err := c.conn.Write([]byte{basic[0] | 0xc0}); err != nil {
				return err
			}
		}
		if _, err := c.conn.Write(payload[off : off+n]); err != nil {
			return err
		}
		off += n
	}
	return nil
}

func put24(b []byte, v uint32) { b[0] = byte(v >> 16); b[1] = byte(v >> 8); b[2] = byte(v) }

func (c *RTMPClient) writeSetChunkSize() error {
	var p [4]byte
	binary.BigEndian.PutUint32(p[:], uint32(c.chunkSize))
	return c.writeChunk(2, 0x01, 0, 0, p[:])
}

func (c *RTMPClient) command(name string, txn float64, args ...[]byte) error {
	p := amfString(name)
	p = append(p, amfNumber(txn)...)
	for _, a := range args {
		p = append(p, a...)
	}
	return c.writeChunk(3, 20, 0, 0, p)
}

// ---- chunk 读入与消息重组 ----

type inMessage struct {
	typeID   byte
	streamID uint32
	ts       uint32
	payload  []byte
}

// readMessage 读一条完整消息（重组 chunk）。
func (c *RTMPClient) readMessage() (inMessage, error) {
	type partial struct {
		buf      []byte
		typeID   byte
		streamID uint32
		ts       uint32
		want     int
	}
	parts := map[uint32]*partial{}
	for {
		b0, err := c.br.ReadByte()
		if err != nil {
			return inMessage{}, err
		}
		fmtv := b0 >> 6
		csid := uint32(b0 & 0x3F)
		if csid == 0 {
			b1, err := c.br.ReadByte()
			if err != nil {
				return inMessage{}, err
			}
			csid = uint32(b1) + 64
		} else if csid == 1 {
			var b2 [2]byte
			if _, err := io.ReadFull(c.br, b2[:]); err != nil {
				return inMessage{}, err
			}
			csid = uint32(b2[0]) + uint32(b2[1])<<8 + 64
		}
		p := parts[csid]
		switch fmtv {
		case 0:
			var mh [11]byte
			if _, err := io.ReadFull(c.br, mh[:]); err != nil {
				return inMessage{}, err
			}
			p = &partial{
				typeID:   mh[6],
				ts:       uint32(mh[0])<<16 | uint32(mh[1])<<8 | uint32(mh[2]),
				want:     int(mh[3])<<16 | int(mh[4])<<8 | int(mh[5]),
				streamID: binary.LittleEndian.Uint32(mh[7:11]),
			}
			parts[csid] = p
		case 1:
			var mh [7]byte
			if _, err := io.ReadFull(c.br, mh[:]); err != nil {
				return inMessage{}, err
			}
			if p == nil {
				p = &partial{}
				parts[csid] = p
			}
			p.ts += uint32(mh[0])<<16 | uint32(mh[1])<<8 | uint32(mh[2])
			p.want = int(mh[3])<<16 | int(mh[4])<<8 | int(mh[5])
			p.typeID = mh[6]
			p.buf = p.buf[:0]
		case 2:
			var mh [3]byte
			if _, err := io.ReadFull(c.br, mh[:]); err != nil {
				return inMessage{}, err
			}
			if p == nil {
				return inMessage{}, errors.New("rtmp: fmt2 无前置头")
			}
			p.ts += uint32(mh[0])<<16 | uint32(mh[1])<<8 | uint32(mh[2])
		case 3:
			if p == nil {
				return inMessage{}, errors.New("rtmp: fmt3 无前置头")
			}
		}
		if p == nil {
			return inMessage{}, errors.New("rtmp: 空 partial")
		}
		need := p.want - len(p.buf)
		if need > c.inChunk {
			need = c.inChunk
		}
		if need < 0 {
			return inMessage{}, errors.New("rtmp: chunk 长度异常")
		}
		chunk := make([]byte, need)
		if _, err := io.ReadFull(c.br, chunk); err != nil {
			return inMessage{}, err
		}
		p.buf = append(p.buf, chunk...)
		if len(p.buf) >= p.want {
			msg := inMessage{typeID: p.typeID, streamID: p.streamID, ts: p.ts, payload: p.buf}
			delete(parts, csid)
			// 协议控制消息就地处理
			switch msg.typeID {
			case 0x01: // Set Chunk Size
				c.inChunk = int(binary.BigEndian.Uint32(msg.payload))
				continue
			case 0x04: // User Control：PingRequest(6) 回 PingResponse(7)
				if len(msg.payload) >= 2 && binary.BigEndian.Uint16(msg.payload) == 6 {
					resp := []byte{0, 7}
					resp = append(resp, msg.payload[2:]...)
					if err := c.writeChunk(2, 0x04, 0, 0, resp); err != nil {
						return inMessage{}, err
					}
				}
				continue
			case 0x02, 0x03, 0x05, 0x06: // Abort/Ack/WinAck/SetPeerBandwidth：忽略
				continue
			}
			return msg, nil
		}
	}
}

// readCommand 读到一条 AMF0 命令/响应，返回全部解析值。
func (c *RTMPClient) readCommand() ([]any, error) {
	for {
		msg, err := c.readMessage()
		if err != nil {
			return nil, err
		}
		if msg.typeID != 20 && msg.typeID != 17 {
			continue
		}
		ar := &amfReader{r: bytes.NewReader(msg.payload)}
		var vals []any
		for ar.r.Len() > 0 {
			v, err := ar.read()
			if err != nil {
				return nil, err
			}
			vals = append(vals, v)
		}
		return vals, nil
	}
}

func (c *RTMPClient) connect(t rtmpTarget) error {
	if err := c.command("connect", 1, amfObject(map[string]any{
		"app": t.app, "tcUrl": t.tcURL, "type": "nonprivate",
		"flashVer": "FMLE/3.0", "fpad": false,
	})); err != nil {
		return err
	}
	name, _, err := c.readCommandName()
	if err != nil {
		return fmt.Errorf("rtmp: connect 无响应: %w", err)
	}
	if name != "_result" {
		return fmt.Errorf("%w: connect 返回 %s", ErrPublishRejected, name)
	}
	return nil
}

// readCommandName 读命令并返回命令名（connect/createStream 用）。
func (c *RTMPClient) readCommandName() (string, []any, error) {
	vals, err := c.readCommand()
	if err != nil || len(vals) == 0 {
		return "", nil, err
	}
	s, _ := vals[0].(string)
	return s, vals, nil
}

func (c *RTMPClient) createStreamAndPublish(t rtmpTarget) error {
	if err := c.command("createStream", 2, amfNull()); err != nil {
		return err
	}
	name, vals, err := c.readCommandName()
	if err != nil {
		return fmt.Errorf("rtmp: createStream 无响应: %w", err)
	}
	if name != "_result" {
		return fmt.Errorf("%w: createStream 返回 %s", ErrPublishRejected, name)
	}
	// createStream 响应: ["_result", txn, null, streamId]
	if len(vals) >= 4 {
		if id, ok := vals[3].(float64); ok && id > 0 {
			c.streamID = uint32(id)
		} else {
			c.streamID = 1
		}
	} else {
		c.streamID = 1
	}
	// publish 命令携带 stream key（仅此一处使用，随 TCP 连接发出，不落任何日志）
	p := amfString("publish")
	p = append(p, amfNumber(0)...)
	p = append(p, amfNull()...)
	p = append(p, amfString(t.key)...)
	p = append(p, amfString("live")...)
	if err := c.writeChunk(3, 20, c.streamID, 0, p); err != nil {
		return err
	}
	for {
		vals, err := c.readCommand()
		if err != nil {
			return fmt.Errorf("rtmp: publish 无响应: %w", err)
		}
		// onStatus: ["onStatus", 0, null, {level, code, description}]
		name, _ := vals[0].(string)
		if name != "onStatus" || len(vals) < 4 {
			continue
		}
		m, ok := vals[3].(map[string]any)
		if !ok {
			return errors.New("rtmp: onStatus 形状异常")
		}
		code, _ := m["code"].(string)
		if code == "NetStream.Publish.Start" {
			return nil
		}
		desc, _ := m["description"].(string)
		return fmt.Errorf("%w: %s %s", ErrPublishRejected, code, desc)
	}
}

// SendMeta 发送 @setDataFrame onMetaData。
func (c *RTMPClient) SendMeta(width, height, fps int) error {
	p := amfString("@setDataFrame")
	p = append(p, amfString("onMetaData")...)
	p = append(p, amfECMA(map[string]any{
		"width": width, "height": height, "framerate": fps,
		"videocodecid": 7.0, "audiocodecid": 10.0,
	})...)
	return c.writeChunk(4, 18, c.streamID, 0, p)
}

// SendTag 发布一个 FLV tag（type 8/9/18）。csid 视频 6 / 音频 7 / 脚本 4。
func (c *RTMPClient) SendTag(t Tag) error {
	var csid uint32 = 4
	switch t.Type {
	case 9:
		csid = 6
	case 8:
		csid = 7
	case 18:
	default:
		return nil // 忽略未知类型
	}
	return c.writeChunk(csid, t.Type, c.streamID, t.Timestamp, t.Data)
}

// Close 关闭连接。
func (c *RTMPClient) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

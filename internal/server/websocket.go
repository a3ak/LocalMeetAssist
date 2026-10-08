package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// maxWSMessage caps a single incoming frame payload. URLs lists are small, but a
// generous bound keeps a misbehaving client from asking for an unbounded buffer.
const maxWSMessage = 1 << 20 // 1 MiB

var errWsClosed = errors.New("websocket closed")

// wsConn is a minimal server-side RFC 6455 connection. Text frames only; control
// frames (ping/pong/close) are handled inline.
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex // serializes writes
}

// Hijack forwards to the underlying writer so the WebSocket upgrade can take
// over the connection from within the security middleware.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("hijacking not supported")
}

// upgradeWebSocket performs the RFC 6455 handshake and hijacks the connection.
func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerHasToken(r.Header.Get("Connection"), "upgrade") {
		return nil, errors.New("not a websocket upgrade request")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer does not support hijacking")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if err == nil {
		err = rw.Flush()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{conn: conn, br: rw.Reader}, nil
}

// ReadText returns the payload of the next text frame, transparently answering
// pings and ignoring binary frames. Close frames return errWsClosed.
func (c *wsConn) ReadText() ([]byte, error) {
	for {
		op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case 0x1: // text
			return payload, nil
		case 0x2: // binary — ignored
		case 0x8: // close
			_ = c.writeFrame(0x8, nil)
			return nil, errWsClosed
		case 0x9: // ping
			_ = c.writeFrame(0xA, payload)
		case 0xA: // pong — ignored
		}
	}
}

func (c *wsConn) readFrame() (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(c.br, header[:]); err != nil {
		return 0, nil, err
	}
	op := header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > maxWSMessage {
		return 0, nil, errors.New("websocket frame too large")
	}
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return op, payload, nil
}

// WriteText sends a single unmasked text frame.
func (c *wsConn) WriteText(payload []byte) error {
	return c.writeFrame(0x1, payload)
}

func (c *wsConn) writeFrame(op byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	header := make([]byte, 0, 10)
	header = append(header, 0x80|op) // FIN + opcode
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, byte(n))
	case n <= 0xFFFF:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		header = append(header, ext[:]...)
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}

// SetReadDeadline configures the underlying connection read deadline.
func (c *wsConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// Close sends a close frame and shuts the underlying connection down.
func (c *wsConn) Close() error {
	_ = c.writeFrame(0x8, nil)
	return c.conn.Close()
}

// WebSocket close status codes used by the integration handshake.
const (
	closeProtocolError   = 1002
	closePolicyViolation = 1008
)

// CloseWithCode sends a close frame carrying a status code and a short reason,
// then releases the connection. The payload of a control frame is limited to
// 125 bytes, two of which hold the code.
func (c *wsConn) CloseWithCode(code uint16, reason string) error {
	payload := append([]byte{byte(code >> 8), byte(code)}, reason...)
	if len(payload) > 123 {
		payload = payload[:123]
	}
	_ = c.writeFrame(0x8, payload)
	return c.conn.Close()
}

func headerHasToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// websocketHandler upgrades the request and runs the browser plugin session.
// Authentication happens in the hello message; here we only reject web pages,
// for which CORS does not apply to WebSocket connections.
func (s *Server) websocketHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && (strings.HasPrefix(origin, "http://") || strings.HasPrefix(origin, "https://")) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	conn, err := upgradeWebSocket(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "upgrade failed")
		return
	}
	s.serveBrowserWS(conn, remoteIP(r), r.UserAgent())
}

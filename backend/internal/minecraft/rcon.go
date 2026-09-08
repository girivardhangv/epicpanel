// rcon.go — the Source RCON protocol client used to drive Minecraft server
// consoles (list / TPS / save-all / stop / allowlisted commands).
//
// Stdlib-only, ~150 lines, framed over TCP: [int32 length][int32 id]
// [int32 type][payload][00 00]. The codec is separated from the connection
// so it is unit-testable without a server (the agent and the control plane
// share this package; the password never leaves the node beyond the
// encrypted-at-rest copy).
package minecraft

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// RCON packet types (Source RCON spec).
const (
	RCONTypeAuthResponse = 2
	RCONTypeCommand      = 2
	RCONTypeAuth         = 3
	RCONTypeResponse     = 0
)

// RCONMaxPacket caps the frame payload (spec limit 4096 body bytes; we read
// up to 16 KiB and reject oversized frames to bound memory).
const RCONMaxPacket = 16 * 1024

var errRCONAuth = errors.New("rcon: authentication failed")

// RCONPacket is one wire frame.
type RCONPacket struct {
	ID   int32
	Type int32
	Body string
}

// EncodeRCONPacket serializes a packet.
func EncodeRCONPacket(p RCONPacket) []byte {
	body := []byte(p.Body)
	size := 4 + 4 + len(body) + 2
	buf := make([]byte, 4+size)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(size))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(p.ID))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(p.Type))
	copy(buf[12:], body)
	buf[12+len(body)] = 0
	buf[12+len(body)+1] = 0
	return buf
}

// DecodeRCONPacket reads one frame (length-capped).
func DecodeRCONPacket(r io.Reader) (RCONPacket, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return RCONPacket{}, err
	}
	size := binary.LittleEndian.Uint32(lenBuf[:])
	if size < 10 || size > RCONMaxPacket {
		return RCONPacket{}, fmt.Errorf("rcon: invalid packet size %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return RCONPacket{}, err
	}
	p := RCONPacket{
		ID:   int32(binary.LittleEndian.Uint32(body[0:4])),
		Type: int32(binary.LittleEndian.Uint32(body[4:8])),
	}
	// Body runs to the first NUL (trailing padding included by some servers).
	end := 8
	for end < len(body) && body[end] != 0 {
		end++
	}
	p.Body = string(body[8:end])
	return p, nil
}

// RCONConn is a command connection to one server's RCON endpoint.
type RCONConn struct {
	conn net.Conn
	id   int32
}

// DialRCON opens and authenticates a connection. Bounded by timeout.
func DialRCON(addr, password string, timeout time.Duration) (*RCONConn, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c := &RCONConn{conn: conn, id: 1}
	if err := c.auth(password); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return c, nil
}

func (c *RCONConn) auth(password string) error {
	req := RCONPacket{ID: c.id, Type: RCONTypeAuth, Body: password}
	if _, err := c.conn.Write(EncodeRCONPacket(req)); err != nil {
		return err
	}
	resp, err := DecodeRCONPacket(c.conn)
	if err != nil {
		return err
	}
	if resp.ID == -1 {
		return errRCONAuth
	}
	c.id++
	return nil
}

// Command runs one server command and returns the response text. A trailing
// extra auth-response frame (some servers send one after commands) is
// skipped transparently.
func (c *RCONConn) Command(cmd string) (string, error) {
	req := RCONPacket{ID: c.id, Type: RCONTypeCommand, Body: cmd}
	if _, err := c.conn.Write(EncodeRCONPacket(req)); err != nil {
		return "", err
	}
	resp, err := DecodeRCONPacket(c.conn)
	if err != nil {
		return "", err
	}
	if resp.ID != req.ID {
		// One stale auth-response frame: read once more (bounded).
		resp, err = DecodeRCONPacket(c.conn)
		if err != nil {
			return "", err
		}
	}
	c.id++
	return resp.Body, nil
}

// Close closes the connection.
func (c *RCONConn) Close() error { return c.conn.Close() }

// ---------------------------------------------------------------------------
// Response parsing for metrics (players / TPS / MSPT) — best-effort with
// honesty flags: an unparseable response means "unknown", never a guess.
// ---------------------------------------------------------------------------

// RCONPlayers parses `list` output ("There are 2 of a max of 20 players
// online: Alice, Bob").
func RCONPlayers(listOutput string) (online, max int, names []string, ok bool) {
	s := strings.TrimSpace(listOutput)
	i := strings.Index(s, "There are ")
	if i < 0 {
		return 0, 0, nil, false
	}
	s = s[i+len("There are "):]
	if _, err := fmt.Sscanf(s, "%d of a max of %d", &online, &max); err != nil {
		// Paper style: "There are 2 of a max of 20 players online" — same
		// shape; fail honestly if neither matches.
		return 0, 0, nil, false
	}
	rest := ""
	if j := strings.Index(s, "online:"); j >= 0 {
		rest = strings.TrimSpace(s[j+len("online:"):])
	}
	if rest != "" {
		for _, n := range strings.Split(rest, ", ") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	return online, max, names, true
}

// RCONTPS parses Paper's `tps` output ("TPS from last 1m, 5m, 15m: 19.98,
// 20.0, 20.0") and returns the 1m value.
func RCONTPS(tpsOutput string) (float64, bool) {
	s := strings.TrimSpace(tpsOutput)
	i := strings.Index(s, ":")
	if i < 0 || !strings.Contains(s, "TPS") {
		return 0, false
	}
	fields := strings.Split(s[i+1:], ",")
	if len(fields) < 1 {
		return 0, false
	}
	var v float64
	if _, err := fmt.Sscanf(strings.TrimSpace(fields[0]), "%f", &v); err != nil {
		return 0, false
	}
	if v < 0 || v > 1000 {
		return 0, false
	}
	return v, true
}

// RCONMSPT parses Paper's `paper mspt` output ("Median: 3.2 ms/ tick." or
// the percentile table). Returns the median (best stability indicator).
func RCONMSPT(msptOutput string) (float64, bool) {
	for _, line := range strings.Split(msptOutput, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Median:") || strings.Contains(line, "median") {
			var v float64
			if _, err := fmt.Sscanf(line, "Median: %f", &v); err == nil && v >= 0 && v < 1000 {
				return v, true
			}
		}
	}
	return 0, false
}

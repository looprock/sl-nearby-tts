package main

// The LEAP wire protocol. Every message in both directions is
//
//	<byte count>:<LLSD notation>
//
// with no trailing newline. The viewer sends one preliminary message naming the
// pump it will read our replies from; from then on both sides exchange
// {'pump': <name>, 'data': <payload>} maps.
//
// Reference: indra/llcommon/llleap.cpp (LLLeapImpl) in secondlife/viewer.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// maxFrame bounds how much we will allocate for one message, so that a desync
// cannot be turned into an out-of-memory crash by a bogus length prefix.
const maxFrame = 16 << 20

// Conn is one LEAP session: the viewer's stdout pipe to us, and our stdout pipe
// back to it.
type Conn struct {
	r io.Reader
	w io.Writer

	// ReplyPump is the name of the viewer-side pump that forwards events to our
	// stdin. We quote it as "reply" in requests that expect an answer.
	ReplyPump string
	// CommandPump is the per-session LEAP command pump, which is where "listen"
	// and friends go.
	CommandPump string

	mu sync.Mutex // serializes writes; one frame must not interleave with another
}

func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{r: r, w: w}
}

// Frame is one decoded message.
type Frame struct {
	Pump string
	Data map[string]any
}

// Handshake reads the viewer's preliminary message and records the pump names
// from it. It must be called before anything else.
func (c *Conn) Handshake() error {
	f, err := c.ReadFrame()
	if err != nil {
		return fmt.Errorf("leap handshake: %w", err)
	}
	c.ReplyPump = f.Pump
	c.CommandPump, _ = f.Data["command"].(string)
	if c.ReplyPump == "" || c.CommandPump == "" {
		return fmt.Errorf("leap handshake: missing pump names (pump=%q command=%q)", c.ReplyPump, c.CommandPump)
	}
	return nil
}

// ReadFrame reads and decodes the next message.
func (c *Conn) ReadFrame() (Frame, error) {
	n, err := c.readLength()
	if err != nil {
		return Frame{}, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return Frame{}, fmt.Errorf("leap: short read of %d byte frame: %w", n, err)
	}
	v, err := decodeFrame(buf)
	if err != nil {
		return Frame{}, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Frame{}, fmt.Errorf("leap: frame is %T, want map", v)
	}
	f := Frame{}
	f.Pump, _ = m["pump"].(string)
	if d, ok := m["data"].(map[string]any); ok {
		f.Data = d
	} else {
		f.Data = map[string]any{}
	}
	return f, nil
}

// decodeFrame decodes one message body, whichever serialization the viewer
// chose. LLSDSerialize::serialize prepends a "<? LLSD/Binary ?>\n" header
// naming the format; a body with no header is notation, which is what this
// program sends and what the protocol was originally documented as.
func decodeFrame(b []byte) (any, error) {
	if !bytes.HasPrefix(b, []byte("<?")) {
		return Decode(b)
	}
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return nil, fmt.Errorf("leap: unterminated serialization header")
	}
	header, body := strings.ToLower(string(b[:nl])), b[nl+1:]
	switch {
	case strings.Contains(header, "binary"):
		return decodeBinary(body)
	case strings.Contains(header, "notation"):
		return Decode(body)
	default:
		return nil, fmt.Errorf("leap: unsupported serialization %q", strings.TrimSpace(string(b[:nl])))
	}
}

// readLength consumes the decimal byte count and the colon that follows it.
func (c *Conn) readLength() (int, error) {
	var digits []byte
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(c.r, one); err != nil {
			if len(digits) == 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
				return 0, io.EOF // clean shutdown: viewer closed the pipe
			}
			return 0, fmt.Errorf("leap: reading length prefix: %w", err)
		}
		switch b := one[0]; {
		case b >= '0' && b <= '9':
			digits = append(digits, b)
			if len(digits) > 12 {
				return 0, fmt.Errorf("leap: absurd length prefix")
			}
		case b == ':':
			if len(digits) == 0 {
				return 0, fmt.Errorf("leap: empty length prefix")
			}
			n, err := strconv.Atoi(string(digits))
			if err != nil {
				return 0, fmt.Errorf("leap: bad length prefix %q", digits)
			}
			if n < 0 || n > maxFrame {
				return 0, fmt.Errorf("leap: frame length %d out of range", n)
			}
			return n, nil
		default:
			// The viewer reports this as "bad protocol" in the other direction;
			// on our side it means the stream is no longer framed and there is
			// no safe way to resynchronize.
			return 0, fmt.Errorf("leap: unexpected byte %q in length prefix", string(b))
		}
	}
}

// Send writes one message addressed to a viewer-side pump.
func (c *Conn) Send(pump string, data any) error {
	body, err := Encode(map[string]any{"pump": pump, "data": data})
	if err != nil {
		return err
	}
	// Assemble the whole frame before writing so that a single Write carries it,
	// and hold the lock so two goroutines cannot interleave frames.
	frame := append([]byte(strconv.Itoa(len(body))+":"), body...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.w.Write(frame); err != nil {
		return fmt.Errorf("leap: writing frame: %w", err)
	}
	if f, ok := c.w.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
	return nil
}

// Command sends an operation to the LEAP command pump, tagging it with our
// reply pump so that any response comes back to our stdin.
func (c *Conn) Command(op string, args map[string]any) error {
	data := map[string]any{"op": op, "reply": c.ReplyPump}
	for k, v := range args {
		data[k] = v
	}
	return c.Send(c.CommandPump, data)
}

// Listen subscribes to a viewer-side event pump by name. Events then arrive as
// frames whose Pump field is the source name.
func (c *Conn) Listen(source, listener string) error {
	return c.Command("listen", map[string]any{"source": source, "listener": listener})
}

// Request calls an operation on a named LLEventAPI, for example
// Request("LLAgent", "getID", nil).
func (c *Conn) Request(api, op string, args map[string]any) error {
	data := map[string]any{"op": op, "reply": c.ReplyPump}
	for k, v := range args {
		data[k] = v
	}
	return c.Send(api, data)
}

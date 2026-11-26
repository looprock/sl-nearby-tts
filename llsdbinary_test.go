package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// benc writes binary LLSD the way the viewer's LLSDBinaryFormatter does, so the
// decoder is tested against bytes built from the format rather than from its
// own assumptions.
func benc(v any) []byte {
	u32 := func(n int) []byte {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(n))
		return b
	}
	switch t := v.(type) {
	case nil:
		return []byte{'!'}
	case bool:
		if t {
			return []byte{'1'}
		}
		return []byte{'0'}
	case int64:
		return append([]byte{'i'}, u32(int(t))...)
	case float64:
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, math.Float64bits(t))
		return append([]byte{'r'}, b...)
	case string:
		return append(append([]byte{'s'}, u32(len(t))...), t...)
	case URI:
		return append(append([]byte{'l'}, u32(len(t))...), t...)
	case []byte:
		return append(append([]byte{'b'}, u32(len(t))...), t...)
	case UUID:
		raw := make([]byte, 0, 16)
		for _, part := range strings.Split(string(t), "-") {
			for i := 0; i+1 < len(part); i += 2 {
				n, _ := strconv.ParseUint(part[i:i+2], 16, 8)
				raw = append(raw, byte(n))
			}
		}
		return append([]byte{'u'}, raw...)
	case []any:
		out := append([]byte{'['}, u32(len(t))...)
		for _, e := range t {
			out = append(out, benc(e)...)
		}
		return append(out, ']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := append([]byte{'{'}, u32(len(t))...)
		for _, k := range keys {
			out = append(out, 'k')
			out = append(out, u32(len(k))...)
			out = append(out, k...)
			out = append(out, benc(t[k])...)
		}
		return append(out, '}')
	}
	panic(fmt.Sprintf("benc: %T", v))
}

// bframe wraps a payload the way LLLeapImpl::wstdin does: byte count, colon,
// the "<? LLSD/Binary ?>" header that serialize() prepends, then the payload.
func bframe(pump string, data map[string]any) string {
	body := append([]byte("<? LLSD/Binary ?>\n"), benc(map[string]any{"pump": pump, "data": data})...)
	return strconv.Itoa(len(body)) + ":" + string(body)
}

func TestBinaryRoundTrip(t *testing.T) {
	original := map[string]any{
		"message": "hello there",
		"from":    "Alice Resident",
		"from_id": UUID("5f7b2c10-1234-4abc-9def-0123456789ab"),
		"source":  int64(1),
		"time":    1234.5,
		"flag":    true,
		"off":     false,
		"nothing": nil,
		"blob":    []byte{0, 1, 2, 255},
		"link":    URI("http://example.com/x"),
		"list":    []any{"a", int64(-7), 2.5},
		"nested":  map[string]any{"inner": "value"},
		"unicode": "café ✓",
	}
	got, err := decodeBinary(benc(original))
	if err != nil {
		t.Fatalf("decodeBinary: %v", err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("round trip changed the value:\n got %#v\nwant %#v", got, original)
	}
}

func TestBinaryNegativeInteger(t *testing.T) {
	got, err := decodeBinary(benc(int64(-42)))
	if err != nil {
		t.Fatalf("decodeBinary: %v", err)
	}
	if got != int64(-42) {
		t.Errorf("got %#v, want int64(-42)", got)
	}
}

// The bug this file exists for: the viewer's handshake arrives as binary behind
// a "<? LLSD/Binary ?>" header, and a notation-only parser dies on the '<'.
func TestReadFrameAcceptsBinaryHandshake(t *testing.T) {
	wire := bframe("reply-pump-7", map[string]any{
		"command":  "cmd-pump-7",
		"features": map[string]any{},
	})
	c := NewConn(strings.NewReader(wire), nil)
	if err := c.Handshake(); err != nil {
		t.Fatalf("Handshake on a binary frame: %v", err)
	}
	if c.ReplyPump != "reply-pump-7" || c.CommandPump != "cmd-pump-7" {
		t.Errorf("pumps = %q / %q", c.ReplyPump, c.CommandPump)
	}
}

func TestReadFrameAcceptsBinaryChat(t *testing.T) {
	wire := bframe(chatPump, map[string]any{
		"message":    "hello there",
		"from":       "Alice Resident",
		"from_id":    UUID("aaaa1111-0000-0000-0000-000000000001"),
		"source":     int64(srcAgent),
		"chat_type":  int64(chatNormal),
		"chat_style": int64(styleNormal),
		"time":       1234.5,
	})
	f, err := NewConn(strings.NewReader(wire), nil).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f.Pump != chatPump {
		t.Fatalf("pump = %q", f.Pump)
	}
	c := parseChat(f.Data)
	if c.Message != "hello there" || c.From != "Alice Resident" || c.Source != srcAgent {
		t.Errorf("parseChat = %+v", c)
	}
	line, ok := NewNarrator(DefaultOptions()).Line(c, base)
	if !ok || line != "Alice says, hello there" {
		t.Errorf("Line = %q, ok=%v", line, ok)
	}
}

// Notation with no header still works, which is what this program sends back
// and what the protocol was originally documented as.
func TestReadFrameStillAcceptsNotation(t *testing.T) {
	payload := `{'pump':'LLChat','data':{'message':'hi'}}`
	f, err := NewConn(strings.NewReader(strconv.Itoa(len(payload))+":"+payload), nil).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f.Data["message"] != "hi" {
		t.Errorf("data = %#v", f.Data)
	}
}

func TestDecodeFrameRejectsUnknownSerialization(t *testing.T) {
	// XML is a format the viewer does not use for LEAP; say so rather than
	// failing with a confusing byte-level error.
	_, err := decodeFrame([]byte("<? LLSD/XML ?>\n<llsd><map/></llsd>"))
	if err == nil || !strings.Contains(err.Error(), "unsupported serialization") {
		t.Errorf("got %v, want an unsupported-serialization error", err)
	}
}

func TestDecodeBinaryErrors(t *testing.T) {
	for name, in := range map[string][]byte{
		"empty":            {},
		"unknown tag":      {'Z'},
		"truncated int":    {'i', 0, 0},
		"truncated real":   {'r', 0},
		"truncated uuid":   {'u', 1, 2, 3},
		"string overruns":  {'s', 0, 0, 0, 9, 'a', 'b'},
		"map key untagged": {'{', 0, 0, 0, 1, 'X'},
		"array unclosed":   {'[', 0, 0, 0, 1, '1'},
		"absurd length":    {'s', 255, 255, 255, 255},
	} {
		if v, err := decodeBinary(in); err == nil {
			t.Errorf("%s: got %#v, want error", name, v)
		}
	}
}

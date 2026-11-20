package main

import (
	"reflect"
	"strings"
	"testing"
)

// A nearby-chat event exactly as LLSDNotationFormatter writes it: single-quoted
// strings and keys, i-prefixed integers, r-prefixed reals, u-prefixed UUIDs.
const sampleChat = `{'chat_style':i0,'chat_type':i1,'from':'Alice Resident','from_id':u5f7b2c10-1234-4abc-9def-0123456789ab,` +
	`'message':'hello there','sender_slurl':'secondlife:///app/agent/5f7b2c10-1234-4abc-9def-0123456789ab/about','source':i1,'time':r1234.57}`

func TestDecodeChatEvent(t *testing.T) {
	v, err := Decode([]byte(sampleChat))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("got %T, want map", v)
	}
	if got := m["message"]; got != "hello there" {
		t.Errorf("message = %#v", got)
	}
	if got := m["from"]; got != "Alice Resident" {
		t.Errorf("from = %#v", got)
	}
	if got := m["from_id"]; got != UUID("5f7b2c10-1234-4abc-9def-0123456789ab") {
		t.Errorf("from_id = %#v", got)
	}
	if got := m["chat_type"]; got != int64(1) {
		t.Errorf("chat_type = %#v, want int64(1)", got)
	}
	if got := m["time"]; got != 1234.57 {
		t.Errorf("time = %#v", got)
	}

	// And it should survive the trip through our own struct conversion.
	c := parseChat(m)
	if c.From != "Alice Resident" || c.ChatType != chatNormal || c.Source != srcAgent {
		t.Errorf("parseChat = %+v", c)
	}
}

func TestDecodeHandshake(t *testing.T) {
	// Shape of the preliminary message from LLLeapImpl's constructor.
	in := `{'command':'LLLeapListener-0','features':{'versions':[i1,i2]}}`
	v, err := Decode([]byte(in))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	m := v.(map[string]any)
	if m["command"] != "LLLeapListener-0" {
		t.Errorf("command = %#v", m["command"])
	}
	feats, ok := m["features"].(map[string]any)
	if !ok {
		t.Fatalf("features = %T", m["features"])
	}
	if got := feats["versions"]; !reflect.DeepEqual(got, []any{int64(1), int64(2)}) {
		t.Errorf("versions = %#v", got)
	}
}

func TestDecodeScalars(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want any
	}{
		{`!`, nil},
		{`1`, true},
		{`0`, false},
		{`true`, true},
		{`false`, false},
		{`T`, true},
		{`f`, false},
		{`i0`, int64(0)},
		{`i-42`, int64(-42)},
		{`r-1.5`, -1.5},
		{`r1e+10`, 1e10},
		{`'plain'`, "plain"},
		{`"double quoted"`, "double quoted"},
		{`s(5)"hello"`, "hello"},
		{`s(0)""`, ""},
		{`''`, ""},
		{`u00000000-0000-0000-0000-000000000000`, UUID(nullUUID)},
		{`l"http://example.com/x"`, URI("http://example.com/x")},
		{`d"2009-01-01T00:00:00Z"`, "2009-01-01T00:00:00Z"},
		{`{}`, map[string]any{}},
		{`[]`, []any{}},
		{"  i7  ", int64(7)},
	} {
		got, err := Decode([]byte(tc.in))
		if err != nil {
			t.Errorf("Decode(%q): %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Decode(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestDecodeStringEscapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`'it\'s'`, `it's`},
		{`'back\\slash'`, `back\slash`},
		{`'two\nlines'`, "two\nlines"},
		{`'tab\there'`, "tab\there"},
		{`'bell\a'`, "bell\a"},
		{`'quote"inside'`, `quote"inside`},
		// The formatter escapes every byte above 0x7e individually, so UTF-8
		// arrives as hex and must be reassembled bytewise, not runewise.
		{`'caf\xc3\xa9'`, "café"},
		{`'\xe2\x9c\x93 done'`, "✓ done"},
		{`'\q'`, "q"},
	} {
		got, err := Decode([]byte(tc.in))
		if err != nil {
			t.Errorf("Decode(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Decode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDecodeBinary(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{`b16"48656c6c6f"`, "Hello"},
		{`b64"SGVsbG8="`, "Hello"},
		{`b(5)"Hello"`, "Hello"},
	} {
		got, err := Decode([]byte(tc.in))
		if err != nil {
			t.Errorf("Decode(%q): %v", tc.in, err)
			continue
		}
		b, ok := got.([]byte)
		if !ok {
			t.Errorf("Decode(%q) = %T, want []byte", tc.in, got)
			continue
		}
		if string(b) != tc.want {
			t.Errorf("Decode(%q) = %q, want %q", tc.in, b, tc.want)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{
		``,
		`'unterminated`,
		`{'k':i1`,
		`{'k' i1}`,
		`{i1:i2}`,
		`[i1 i2]`,
		`i1 i2`,
		`u0000`,
		`s(9)"short"`,
		`&`,
		`b(3)"ab"`,
		`b99"zz"`,
	} {
		if v, err := Decode([]byte(in)); err == nil {
			t.Errorf("Decode(%q) = %#v, want error", in, v)
		}
	}
}

func TestDecodeRejectsDeepNesting(t *testing.T) {
	deep := strings.Repeat("[", 200) + strings.Repeat("]", 200)
	if _, err := Decode([]byte(deep)); err == nil {
		t.Error("want error for deeply nested input")
	}
}

func TestEncode(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{nil, `!`},
		{true, `true`},
		{false, `false`},
		{int(7), `i7`},
		{int64(-7), `i-7`},
		{1.5, `r1.5`},
		{"plain", `'plain'`},
		{UUID("5f7b2c10-1234-4abc-9def-0123456789ab"), `u5f7b2c10-1234-4abc-9def-0123456789ab`},
		{URI("http://x/y"), `l"http://x/y"`},
		{[]any{int64(1), "two"}, `[i1,'two']`},
		// Keys are sorted, which keeps the wire format reproducible.
		{map[string]any{"op": "listen", "source": "LLChat"}, `{'op':'listen','source':'LLChat'}`},
		{map[string]any{}, `{}`},
		// Escaping: quote, backslash, newline, and anything non-ASCII.
		{"it's", `'it\'s'`},
		{`a\b`, `'a\\b'`},
		{"two\nlines", `'two\nlines'`},
		{"café", `'caf\xc3\xa9'`},
	} {
		got, err := Encode(tc.in)
		if err != nil {
			t.Errorf("Encode(%#v): %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("Encode(%#v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestEncodeRejectsUnsupported(t *testing.T) {
	if _, err := Encode(struct{}{}); err == nil {
		t.Error("want error encoding an unsupported type")
	}
}

// Round-tripping proves the escape table in writeEscaped and the unescaping in
// quoted agree with each other, including for text the viewer would hex-escape.
func TestRoundTrip(t *testing.T) {
	original := map[string]any{
		"op":      "sendChat",
		"message": "Olá! \"quoted\", it's 100% — ✓\nnewline\ttab\\slash",
		"n":       int64(42),
		"f":       2.5,
		"b":       true,
		"nothing": nil,
		"id":      UUID("5f7b2c10-1234-4abc-9def-0123456789ab"),
		"list":    []any{"a", int64(1), false},
		"nested":  map[string]any{"inner": "value"},
	}
	wire, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := Decode(wire)
	if err != nil {
		t.Fatalf("Decode(%s): %v", wire, err)
	}
	if !reflect.DeepEqual(back, original) {
		t.Errorf("round trip changed the value:\n got %#v\nwant %#v", back, original)
	}
}

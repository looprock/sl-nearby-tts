package main

// Minimal codec for LLSD "notation" serialization, which is the only format the
// Second Life viewer accepts from a LEAP plugin.
//
// Reference: indra/llcommon/llsdserialize.cpp in secondlife/viewer --
// LLSDNotationParser::doParse (what we must produce) and
// LLSDNotationFormatter::format_impl (what we must consume).
//
// Notation in one paragraph: "!" is undef; a bare "1"/"0" or "true"/"false" is a
// boolean; "i42" an integer; "r1.5" a real; "u<uuid>" a UUID; "'text'" a string
// (also s(5)"text" with an explicit byte count); d"..." a date; l"..." a URI;
// b16"..", b64".." or b(n)".." binary; "[a,b]" an array; "{'k':v}" a map.
// The formatter escapes every byte outside printable ASCII as \xNN, so decoded
// strings must be assembled bytewise and only then treated as UTF-8.

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// UUID and URI are distinct from plain strings so that a decode/encode round
// trip preserves the type tag.
type UUID string

type URI string

// Decode parses a single notation value. Trailing whitespace is allowed;
// anything else after the value is an error, since every frame the viewer
// sends holds exactly one value.
func Decode(b []byte) (any, error) {
	d := &decoder{b: b}
	v, err := d.value(0)
	if err != nil {
		return nil, err
	}
	d.space()
	if d.i < len(d.b) {
		return nil, fmt.Errorf("llsd: %d trailing byte(s) at offset %d", len(d.b)-d.i, d.i)
	}
	return v, nil
}

const maxDepth = 64

type decoder struct {
	b []byte
	i int
}

func (d *decoder) space() {
	for d.i < len(d.b) {
		switch d.b[d.i] {
		case ' ', '\t', '\r', '\n', '\v', '\f':
			d.i++
		default:
			return
		}
	}
}

func (d *decoder) errAt(format string, args ...any) error {
	return fmt.Errorf("llsd: "+format+" at offset %d", append(args, d.i)...)
}

func (d *decoder) value(depth int) (any, error) {
	if depth > maxDepth {
		return nil, d.errAt("nested deeper than %d", maxDepth)
	}
	d.space()
	if d.i >= len(d.b) {
		return nil, d.errAt("unexpected end of input")
	}
	switch c := d.b[d.i]; c {
	case '{':
		return d.mapValue(depth)
	case '[':
		return d.arrayValue(depth)
	case '!':
		d.i++
		return nil, nil
	case '0':
		d.i++
		return false, nil
	case '1':
		d.i++
		return true, nil
	case 't', 'T', 'f', 'F':
		return d.boolWord()
	case 'i':
		d.i++
		return d.integer()
	case 'r':
		d.i++
		return d.real()
	case 'u':
		d.i++
		return d.uuid()
	case '\'', '"':
		s, err := d.quoted(c)
		return s, err
	case 's':
		return d.sizedString()
	case 'd':
		d.i++
		return d.delimited("date")
	case 'l':
		d.i++
		s, err := d.delimited("uri")
		if err != nil {
			return nil, err
		}
		return URI(s), nil
	case 'b':
		return d.binary()
	default:
		return nil, d.errAt("unexpected byte %q", string(c))
	}
}

// mapValue decodes {'key':value,...}. Keys are always strings; whitespace is
// tolerated everywhere so that pretty-printed notation parses too.
func (d *decoder) mapValue(depth int) (map[string]any, error) {
	d.i++ // '{'
	m := map[string]any{}
	d.space()
	if d.i < len(d.b) && d.b[d.i] == '}' {
		d.i++
		return m, nil
	}
	for {
		d.space()
		if d.i >= len(d.b) {
			return nil, d.errAt("unterminated map")
		}
		var (
			key string
			err error
		)
		switch c := d.b[d.i]; c {
		case '\'', '"':
			key, err = d.quoted(c)
		case 's':
			key, err = d.sizedString()
		default:
			return nil, d.errAt("map key is not a string")
		}
		if err != nil {
			return nil, err
		}
		d.space()
		if d.i >= len(d.b) || d.b[d.i] != ':' {
			return nil, d.errAt("expected : after map key %q", key)
		}
		d.i++
		v, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		m[key] = v
		d.space()
		if d.i >= len(d.b) {
			return nil, d.errAt("unterminated map")
		}
		switch d.b[d.i] {
		case ',':
			d.i++
		case '}':
			d.i++
			return m, nil
		default:
			return nil, d.errAt("expected , or } in map")
		}
	}
}

// arrayValue decodes [a,b,...].
func (d *decoder) arrayValue(depth int) ([]any, error) {
	d.i++ // '['
	out := []any{}
	d.space()
	if d.i < len(d.b) && d.b[d.i] == ']' {
		d.i++
		return out, nil
	}
	for {
		v, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		d.space()
		if d.i >= len(d.b) {
			return nil, d.errAt("unterminated array")
		}
		switch d.b[d.i] {
		case ',':
			d.i++
		case ']':
			d.i++
			return out, nil
		default:
			return nil, d.errAt("expected , or ] in array")
		}
	}
}

// boolWord handles the spelled-out "true"/"false" forms. The parser in the
// viewer accepts a lone "t" or "f" too, so a bare letter is not an error.
func (d *decoder) boolWord() (any, error) {
	want := true
	if c := d.b[d.i]; c == 'f' || c == 'F' {
		want = false
	}
	d.i++
	word := "true"
	if !want {
		word = "false"
	}
	if rest := len(word) - 1; d.i+rest <= len(d.b) &&
		strings.EqualFold(string(d.b[d.i:d.i+rest]), word[1:]) {
		d.i += rest
	}
	return want, nil
}

// numeric scans the characters that can make up an integer or real literal.
func (d *decoder) numeric() string {
	start := d.i
	for d.i < len(d.b) {
		switch c := d.b[d.i]; {
		case c >= '0' && c <= '9',
			c == '+', c == '-', c == '.',
			c == 'e', c == 'E',
			c == 'n', c == 'a', c == 'i', c == 'f', c == 'N', c == 'A', c == 'I', c == 'F':
			// Letters are included so that "nan", "inf" and "-inf" scan as one
			// token instead of silently truncating to 0.
			d.i++
		default:
			return string(d.b[start:d.i])
		}
	}
	return string(d.b[start:d.i])
}

func (d *decoder) integer() (int64, error) {
	tok := d.numeric()
	if tok == "" {
		return 0, d.errAt("integer with no digits")
	}
	n, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		return 0, d.errAt("bad integer %q", tok)
	}
	return n, nil
}

func (d *decoder) real() (float64, error) {
	tok := d.numeric()
	if tok == "" {
		return 0, d.errAt("real with no digits")
	}
	f, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return 0, d.errAt("bad real %q", tok)
	}
	return f, nil
}

func (d *decoder) uuid() (UUID, error) {
	const n = 36 // 8-4-4-4-12
	if d.i+n > len(d.b) {
		return "", d.errAt("truncated uuid")
	}
	s := string(d.b[d.i : d.i+n])
	d.i += n
	return UUID(s), nil
}

// delimited reads a quoted string whose opening delimiter has not been consumed
// yet, used for the d"..." and l"..." forms.
func (d *decoder) delimited(what string) (string, error) {
	if d.i >= len(d.b) {
		return "", d.errAt("truncated %s", what)
	}
	q := d.b[d.i]
	if q != '"' && q != '\'' {
		return "", d.errAt("%s not quoted", what)
	}
	return d.quoted(q)
}

// quoted decodes a string delimited by q, which is still unconsumed, applying
// the escape rules of deserialize_string_delim.
func (d *decoder) quoted(q byte) (string, error) {
	d.i++ // opening delimiter
	var out []byte
	for d.i < len(d.b) {
		c := d.b[d.i]
		d.i++
		switch c {
		case q:
			return string(out), nil
		case '\\':
			if d.i >= len(d.b) {
				return "", d.errAt("truncated escape")
			}
			e := d.b[d.i]
			d.i++
			switch e {
			case 'a':
				out = append(out, '\a')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'v':
				out = append(out, '\v')
			case 'x':
				if d.i+2 > len(d.b) {
					return "", d.errAt("truncated hex escape")
				}
				n, err := strconv.ParseUint(string(d.b[d.i:d.i+2]), 16, 8)
				if err != nil {
					return "", d.errAt("bad hex escape %q", string(d.b[d.i:d.i+2]))
				}
				d.i += 2
				out = append(out, byte(n))
			default:
				// Any other escaped byte stands for itself, which is how \\ and
				// \' arrive.
				out = append(out, e)
			}
		default:
			out = append(out, c)
		}
	}
	return "", d.errAt("unterminated string")
}

// sizedString decodes s(5)"hello", where the count is in bytes and the body is
// copied verbatim with no escape processing.
func (d *decoder) sizedString() (string, error) {
	d.i++ // 's'
	n, err := d.parenCount()
	if err != nil {
		return "", err
	}
	if d.i >= len(d.b) {
		return "", d.errAt("truncated sized string")
	}
	q := d.b[d.i]
	if q != '"' && q != '\'' {
		return "", d.errAt("sized string not quoted")
	}
	d.i++
	if d.i+n > len(d.b) {
		return "", d.errAt("sized string shorter than its %d byte count", n)
	}
	s := string(d.b[d.i : d.i+n])
	d.i += n
	if d.i >= len(d.b) || d.b[d.i] != q {
		return "", d.errAt("sized string not closed")
	}
	d.i++
	return s, nil
}

func (d *decoder) parenCount() (int, error) {
	if d.i >= len(d.b) || d.b[d.i] != '(' {
		return 0, d.errAt("expected (count)")
	}
	d.i++
	start := d.i
	for d.i < len(d.b) && d.b[d.i] >= '0' && d.b[d.i] <= '9' {
		d.i++
	}
	tok := string(d.b[start:d.i])
	if d.i >= len(d.b) || d.b[d.i] != ')' {
		return 0, d.errAt("unterminated (count)")
	}
	d.i++
	n, err := strconv.Atoi(tok)
	if err != nil || n < 0 {
		return 0, d.errAt("bad count %q", tok)
	}
	return n, nil
}

func (d *decoder) binary() (any, error) {
	d.i++ // 'b'
	switch {
	case d.i < len(d.b) && d.b[d.i] == '(':
		n, err := d.parenCount()
		if err != nil {
			return nil, err
		}
		if d.i >= len(d.b) {
			return nil, d.errAt("truncated binary")
		}
		q := d.b[d.i]
		d.i++
		if d.i+n > len(d.b) {
			return nil, d.errAt("binary shorter than its %d byte count", n)
		}
		out := make([]byte, n)
		copy(out, d.b[d.i:d.i+n])
		d.i += n
		if d.i >= len(d.b) || d.b[d.i] != q {
			return nil, d.errAt("binary not closed")
		}
		d.i++
		return out, nil
	case strings.HasPrefix(string(d.b[d.i:]), "16"):
		d.i += 2
		s, err := d.delimited("binary")
		if err != nil {
			return nil, err
		}
		out := make([]byte, 0, len(s)/2)
		for j := 0; j+1 < len(s); j += 2 {
			n, err := strconv.ParseUint(s[j:j+2], 16, 8)
			if err != nil {
				return nil, d.errAt("bad base16 binary")
			}
			out = append(out, byte(n))
		}
		return out, nil
	case strings.HasPrefix(string(d.b[d.i:]), "64"):
		d.i += 2
		s, err := d.delimited("binary")
		if err != nil {
			return nil, err
		}
		out, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, d.errAt("bad base64 binary")
		}
		return out, nil
	default:
		return nil, d.errAt("unknown binary form")
	}
}

// Encode serializes a value as notation. Map keys are emitted in sorted order
// so that output is deterministic.
func Encode(v any) ([]byte, error) {
	var sb strings.Builder
	if err := encodeTo(&sb, v, 0); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

func encodeTo(sb *strings.Builder, v any, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("llsd: encode nested deeper than %d", maxDepth)
	}
	switch t := v.(type) {
	case nil:
		sb.WriteByte('!')
	case bool:
		if t {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case int:
		fmt.Fprintf(sb, "i%d", t)
	case int64:
		fmt.Fprintf(sb, "i%d", t)
	case float64:
		fmt.Fprintf(sb, "r%s", strconv.FormatFloat(t, 'g', -1, 64))
	case UUID:
		sb.WriteByte('u')
		sb.WriteString(string(t))
	case URI:
		sb.WriteString("l\"")
		writeEscaped(sb, string(t), '"')
		sb.WriteByte('"')
	case string:
		sb.WriteByte('\'')
		writeEscaped(sb, t, '\'')
		sb.WriteByte('\'')
	case []byte:
		fmt.Fprintf(sb, "b(%d)\"", len(t))
		sb.Write(t)
		sb.WriteByte('"')
	case []any:
		sb.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				sb.WriteByte(',')
			}
			if err := encodeTo(sb, e, depth+1); err != nil {
				return err
			}
		}
		sb.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('\'')
			writeEscaped(sb, k, '\'')
			sb.WriteString("':")
			if err := encodeTo(sb, t[k], depth+1); err != nil {
				return err
			}
		}
		sb.WriteByte('}')
	default:
		return fmt.Errorf("llsd: cannot encode %T", v)
	}
	return nil
}

// writeEscaped mirrors serialize_string: printable ASCII passes through, the
// delimiter and backslash are escaped, and everything else becomes \xNN. Bytes
// above 0x7f are escaped individually, which keeps UTF-8 intact once the
// receiving parser reassembles them.
func writeEscaped(sb *strings.Builder, s string, delim byte) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == delim:
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c == '\\':
			sb.WriteString(`\\`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(sb, `\x%02x`, c)
		}
	}
}

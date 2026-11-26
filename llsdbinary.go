package main

// Binary LLSD: what the viewer actually sends a LEAP plugin.
//
// The LEAP wiki page says messages are notation, and that was once true. Both
// the official viewer and Firestorm now call
//
//	LLSDSerialize::serialize(packet, buffer, LLSDSerialize::LLSD_BINARY, ...)
//
// in LLLeapImpl::wstdin, citing SL-18330: binary parses much faster than
// notation for large payloads. serialize() prepends a "<? LLSD/Binary ?>\n"
// header, which is the leading '<' a notation parser trips over.
//
// The direction back to the viewer is still notation, because its read side
// hard-codes LLSDNotationParser, so only this half needed adding.
//
// Reference: LLSDBinaryParser in indra/llcommon/llsdserialize.cpp. Counts and
// lengths are 4-byte big-endian; reals are big-endian IEEE 754.

import (
	"encoding/binary"
	"fmt"
	"math"
)

func decodeBinary(b []byte) (any, error) {
	d := &bdec{b: b}
	v, err := d.value(0)
	if err != nil {
		return nil, err
	}
	return v, nil
}

type bdec struct {
	b []byte
	i int
}

func (d *bdec) errAt(format string, args ...any) error {
	return fmt.Errorf("llsd binary: "+format+" at offset %d", append(args, d.i)...)
}

func (d *bdec) take(n int) ([]byte, error) {
	if n < 0 || d.i+n > len(d.b) {
		return nil, d.errAt("want %d byte(s), have %d", n, len(d.b)-d.i)
	}
	out := d.b[d.i : d.i+n]
	d.i += n
	return out, nil
}

func (d *bdec) u32() (int, error) {
	b, err := d.take(4)
	if err != nil {
		return 0, err
	}
	n := binary.BigEndian.Uint32(b)
	// Lengths and counts are read back as signed in the viewer; a value that
	// large is a desync, not a real payload.
	if n > maxFrame {
		return 0, d.errAt("implausible count or length %d", n)
	}
	return int(n), nil
}

func (d *bdec) value(depth int) (any, error) {
	if depth > maxDepth {
		return nil, d.errAt("nested deeper than %d", maxDepth)
	}
	tag, err := d.take(1)
	if err != nil {
		return nil, err
	}
	switch c := tag[0]; c {
	case '!':
		return nil, nil
	case '0':
		return false, nil
	case '1':
		return true, nil

	case 'i':
		b, err := d.take(4)
		if err != nil {
			return nil, err
		}
		return int64(int32(binary.BigEndian.Uint32(b))), nil

	case 'r':
		b, err := d.take(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil

	case 'd':
		// Dates are the one case the viewer reads without byte-swapping, so
		// they arrive in host order. Both ends are the same machine here, and
		// every platform this builds for is little-endian.
		b, err := d.take(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(b)), nil

	case 'u':
		b, err := d.take(16)
		if err != nil {
			return nil, err
		}
		return UUID(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])), nil

	case 's':
		s, err := d.str()
		return s, err

	case 'l':
		s, err := d.str()
		if err != nil {
			return nil, err
		}
		return URI(s), nil

	case 'b':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		b, err := d.take(n)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), b...), nil

	case '\'', '"':
		// A notation-style delimited string can appear inside binary. Reuse the
		// notation reader rather than duplicating its escape handling.
		return d.quotedViaNotation(c)

	case '[':
		return d.array(depth)
	case '{':
		return d.mapValue(depth)

	default:
		return nil, d.errAt("unknown type byte %q", string(c))
	}
}

// str reads a 4-byte length followed by that many raw bytes.
func (d *bdec) str() (string, error) {
	n, err := d.u32()
	if err != nil {
		return "", err
	}
	b, err := d.take(n)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// quotedViaNotation delegates to the notation decoder's string reader, whose
// escape table is already tested. The delimiter has been consumed, so rewind
// one byte for it.
func (d *bdec) quotedViaNotation(delim byte) (string, error) {
	nd := &decoder{b: d.b, i: d.i - 1}
	s, err := nd.quoted(delim)
	if err != nil {
		return "", err
	}
	d.i = nd.i
	return s, nil
}

func (d *bdec) array(depth int) ([]any, error) {
	size, err := d.u32()
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, min(size, 64))
	for len(out) < size {
		if d.i < len(d.b) && d.b[d.i] == ']' {
			break
		}
		v, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	closer, err := d.take(1)
	if err != nil {
		return nil, err
	}
	if closer[0] != ']' {
		return nil, d.errAt("array not closed, saw %q", string(closer[0]))
	}
	return out, nil
}

func (d *bdec) mapValue(depth int) (map[string]any, error) {
	size, err := d.u32()
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, size)
	for len(out) < size {
		tag, err := d.take(1)
		if err != nil {
			return nil, err
		}
		var key string
		switch tag[0] {
		case '}':
			return out, nil
		case 'k':
			if key, err = d.str(); err != nil {
				return nil, err
			}
		case '\'', '"':
			if key, err = d.quotedViaNotation(tag[0]); err != nil {
				return nil, err
			}
		default:
			return nil, d.errAt("map key tagged %q, want 'k'", string(tag[0]))
		}
		v, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
	closer, err := d.take(1)
	if err != nil {
		return nil, err
	}
	if closer[0] != '}' {
		return nil, d.errAt("map not closed, saw %q", string(closer[0]))
	}
	return out, nil
}

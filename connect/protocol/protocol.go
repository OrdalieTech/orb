// Package protocol implements the bounded, strict JSON stream used by Orb Bridge.
package protocol

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/internal/jsonwire"
)

const (
	Version    = "orb-bridge/1"
	Service    = "orb.instance/1"
	MaxFrame   = 1 << 20
	MaxRecord  = 16 << 10
	MaxDepth   = 64
	MaxPending = 64
	MaxPage    = 128
	MaxOutput  = 4 << 20
)

func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func ValidID(s string) bool {
	b, e := base64.RawURLEncoding.Strict().DecodeString(s)
	return e == nil && len(b) == 16 && len(s) == 22
}
func Counter(s string) (uint64, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, errors.New("invalid counter")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid counter")
		}
	}
	return strconv.ParseUint(s, 10, 64)
}

func Read(r io.Reader) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > MaxFrame {
		return nil, errors.New("invalid frame length")
	}
	b := make([]byte, int(n))
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	if _, err := Canonical(b); err != nil {
		return nil, err
	}
	return b, nil
}
func Write(w io.Writer, b []byte) error {
	if len(b) == 0 || len(b) > MaxFrame {
		return errors.New("invalid frame length")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	for _, part := range [][]byte{h[:], b} {
		for len(part) > 0 {
			n, err := w.Write(part)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}

// Decode rejects unknown arguments as well as JSON ambiguities before binding a schema.
func Decode(b []byte, v any) error {
	if _, err := Canonical(b); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// Canonical implements RFC 8785 for object payloads. Counters remain strings;
// numeric values use IEEE 754 binary64, as required by JCS.
func Canonical(b []byte) ([]byte, error) {
	if len(b) > MaxFrame || !utf8.Valid(b) || !validSurrogates(b) {
		return nil, errors.New("invalid JSON encoding")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	v, err := readValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, errors.New("JSON object required")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	var out bytes.Buffer
	if err = canonicalValue(&out, v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > MaxDepth {
		return nil, errors.New("JSON nesting limit")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				key, ok := k.(string)
				if !ok {
					return nil, errors.New("invalid object key")
				}
				if _, ok = m[key]; ok {
					return nil, errors.New("duplicate object key")
				}
				v, e := readValue(d, depth+1)
				if e != nil {
					return nil, e
				}
				m[key] = v
			}
			_, err = d.Token()
			return m, err
		case '[':
			a := []any{}
			for d.More() {
				v, e := readValue(d, depth+1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			_, err = d.Token()
			return a, err
		default:
			return nil, errors.New("invalid delimiter")
		}
	}
	return t, nil
}

func canonicalValue(out *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b))) })
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			s, _ := jsonwire.Marshal(k)
			out.Write(s)
			out.WriteByte(':')
			if err := canonicalValue(out, v[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, x := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := canonicalValue(out, x); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	default:
		b, err := jsonwire.Marshal(v)
		if err != nil {
			return err
		}
		out.Write(b)
	}
	return nil
}

func validSurrogates(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if b[i] != '"' {
			continue
		}
		i++
		for ; i < len(b) && b[i] != '"'; i++ {
			if b[i] != '\\' {
				continue
			}
			i++
			if i >= len(b) {
				return false
			}
			if b[i] != 'u' {
				continue
			}
			if i+4 >= len(b) {
				return false
			}
			u, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
			if e != nil {
				return false
			}
			i += 4
			if u >= 0xdc00 && u <= 0xdfff {
				return false
			}
			if u >= 0xd800 && u <= 0xdbff {
				if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
					return false
				}
				low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
				if e != nil || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	return true
}

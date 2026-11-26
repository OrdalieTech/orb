// Package protocol implements the bounded, strict JSON stream used by Orb Mitosis.
package protocol

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/internal/jsonwire"
)

const (
	Version    = "orb-mitosis/1"
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
	if err := Validate(b); err != nil {
		return nil, err
	}
	return b, nil
}

var writeBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 16<<10)
		return &b
	},
}

func Write(w io.Writer, b []byte) error {
	if len(b) == 0 || len(b) > MaxFrame {
		return errors.New("invalid frame length")
	}
	total := 4 + len(b)
	// Coalesce length prefix and payload into one Write so TLS/WebSocket emit a single record/frame.
	pooled := writeBufPool.Get().(*[]byte)
	buf := *pooled
	if cap(buf) < total {
		buf = make([]byte, total)
	} else {
		buf = buf[:total]
	}
	defer func() {
		// Cap retained pooled buffers so a rare 1 MiB frame does not pin memory forever.
		if cap(buf) <= 64<<10 {
			*pooled = buf[:0]
		} else {
			*pooled = make([]byte, 0, 16<<10)
		}
		writeBufPool.Put(pooled)
	}()
	binary.BigEndian.PutUint32(buf[:4], uint32(len(b)))
	copy(buf[4:], b)
	frame := buf
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

// Decode rejects unknown arguments as well as JSON ambiguities before binding a schema.
func Decode(b []byte, v any) error {
	if err := Validate(b); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// Canonical implements RFC 8785 for object payloads. Counters remain strings;
// numeric values use IEEE 754 binary64, as required by JCS.
func Canonical(b []byte) ([]byte, error) {
	v, err := parse(b)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err = canonicalValue(&out, v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Validate accepts exactly the payloads Canonical does without materializing an AST.
func Validate(b []byte) error {
	if len(b) > MaxFrame || !utf8.Valid(b) {
		return errors.New("invalid JSON encoding")
	}
	v := validator{b: b}
	v.skipWS()
	if v.i >= len(v.b) || v.b[v.i] != '{' {
		return errors.New("JSON object required")
	}
	if err := v.object(0); err != nil {
		return err
	}
	v.skipWS()
	if v.i != len(v.b) {
		return errors.New("trailing JSON")
	}
	return nil
}

type validator struct {
	b []byte
	i int
}

func (v *validator) skipWS() {
	for v.i < len(v.b) {
		switch v.b[v.i] {
		case ' ', '\t', '\r', '\n':
			v.i++
		default:
			return
		}
	}
}

func (v *validator) value(depth int) error {
	if depth > MaxDepth {
		return errors.New("JSON nesting limit")
	}
	v.skipWS()
	if v.i >= len(v.b) {
		return io.ErrUnexpectedEOF
	}
	switch v.b[v.i] {
	case '{':
		return v.object(depth)
	case '[':
		return v.array(depth)
	case '"':
		_, _, err := v.stringToken()
		return err
	case 't':
		if v.i+4 <= len(v.b) && string(v.b[v.i:v.i+4]) == "true" {
			v.i += 4
			return nil
		}
		return errors.New("invalid JSON literal")
	case 'f':
		if v.i+5 <= len(v.b) && string(v.b[v.i:v.i+5]) == "false" {
			v.i += 5
			return nil
		}
		return errors.New("invalid JSON literal")
	case 'n':
		if v.i+4 <= len(v.b) && string(v.b[v.i:v.i+4]) == "null" {
			v.i += 4
			return nil
		}
		return errors.New("invalid JSON literal")
	default:
		return v.number()
	}
}

func (v *validator) object(depth int) error {
	if depth > MaxDepth {
		return errors.New("JSON nesting limit")
	}
	v.i++ // '{'
	v.skipWS()
	if v.i >= len(v.b) {
		return io.ErrUnexpectedEOF
	}
	if v.b[v.i] == '}' {
		v.i++
		return nil
	}
	// Stack-allocated slice avoids a map allocation for typical small RPC objects.
	var smallKeys [8][]byte
	nSmall := 0
	var largeKeys map[string]struct{}
	for {
		v.skipWS()
		if v.i >= len(v.b) || v.b[v.i] != '"' {
			return errors.New("invalid object key")
		}
		rawKey, escaped, err := v.stringToken()
		if err != nil {
			return err
		}
		keyBytes := rawKey
		if escaped {
			var decoded string
			if err := json.Unmarshal(v.b[v.i-len(rawKey)-2:v.i], &decoded); err != nil {
				return err
			}
			keyBytes = []byte(decoded)
		}
		if largeKeys != nil {
			k := string(keyBytes)
			if _, dup := largeKeys[k]; dup {
				return errors.New("duplicate object key")
			}
			largeKeys[k] = struct{}{}
		} else {
			for j := range nSmall {
				if bytes.Equal(smallKeys[j], keyBytes) {
					return errors.New("duplicate object key")
				}
			}
			if nSmall < len(smallKeys) {
				smallKeys[nSmall] = keyBytes
				nSmall++
			} else {
				largeKeys = make(map[string]struct{}, 16)
				for j := range nSmall {
					largeKeys[string(smallKeys[j])] = struct{}{}
				}
				largeKeys[string(keyBytes)] = struct{}{}
			}
		}
		v.skipWS()
		if v.i >= len(v.b) || v.b[v.i] != ':' {
			return errors.New("expected ':'")
		}
		v.i++
		if err := v.value(depth + 1); err != nil {
			return err
		}
		v.skipWS()
		if v.i >= len(v.b) {
			return io.ErrUnexpectedEOF
		}
		if v.b[v.i] == '}' {
			v.i++
			return nil
		}
		if v.b[v.i] != ',' {
			return errors.New("expected ',' or '}'")
		}
		v.i++
	}
}

func (v *validator) array(depth int) error {
	if depth > MaxDepth {
		return errors.New("JSON nesting limit")
	}
	v.i++ // '['
	v.skipWS()
	if v.i >= len(v.b) {
		return io.ErrUnexpectedEOF
	}
	if v.b[v.i] == ']' {
		v.i++
		return nil
	}
	for {
		if err := v.value(depth + 1); err != nil {
			return err
		}
		v.skipWS()
		if v.i >= len(v.b) {
			return io.ErrUnexpectedEOF
		}
		if v.b[v.i] == ']' {
			v.i++
			return nil
		}
		if v.b[v.i] != ',' {
			return errors.New("expected ',' or ']'")
		}
		v.i++
	}
}

func (v *validator) stringToken() ([]byte, bool, error) {
	v.i++ // '"'
	start := v.i
	escaped := false
	for v.i < len(v.b) {
		c := v.b[v.i]
		if c == '"' {
			raw := v.b[start:v.i]
			v.i++
			return raw, escaped, nil
		}
		if c < 0x20 {
			return nil, false, errors.New("control character in string")
		}
		if c != '\\' {
			v.i++
			continue
		}
		escaped = true
		v.i++
		if v.i >= len(v.b) {
			return nil, false, io.ErrUnexpectedEOF
		}
		switch v.b[v.i] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			v.i++
		case 'u':
			u, ok := parseHex4(v.b[v.i+1:])
			if !ok {
				return nil, false, errors.New("invalid unicode escape")
			}
			v.i += 5
			if u >= 0xdc00 && u <= 0xdfff {
				return nil, false, errors.New("lone low surrogate")
			}
			if u >= 0xd800 && u <= 0xdbff {
				if v.i+6 > len(v.b) || v.b[v.i] != '\\' || v.b[v.i+1] != 'u' {
					return nil, false, errors.New("lone high surrogate")
				}
				low, ok := parseHex4(v.b[v.i+2:])
				if !ok || low < 0xdc00 || low > 0xdfff {
					return nil, false, errors.New("invalid surrogate pair")
				}
				v.i += 6
			}
		default:
			return nil, false, errors.New("invalid escape character")
		}
	}
	return nil, false, io.ErrUnexpectedEOF
}

func parseHex4(b []byte) (uint16, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var out uint16
	for i := range 4 {
		c := b[i]
		var d uint16
		switch {
		case c >= '0' && c <= '9':
			d = uint16(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint16(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint16(c-'A') + 10
		default:
			return 0, false
		}
		out = out<<4 | d
	}
	return out, true
}

func (v *validator) number() error {
	start := v.i
	if v.b[v.i] == '-' {
		v.i++
		if v.i >= len(v.b) {
			return errors.New("invalid number")
		}
	}
	if v.b[v.i] == '0' {
		v.i++
	} else if v.b[v.i] >= '1' && v.b[v.i] <= '9' {
		for v.i < len(v.b) && v.b[v.i] >= '0' && v.b[v.i] <= '9' {
			v.i++
		}
	} else {
		return errors.New("invalid number")
	}
	hasExp := false
	if v.i < len(v.b) && v.b[v.i] == '.' {
		v.i++
		if v.i >= len(v.b) || v.b[v.i] < '0' || v.b[v.i] > '9' {
			return errors.New("invalid number")
		}
		for v.i < len(v.b) && v.b[v.i] >= '0' && v.b[v.i] <= '9' {
			v.i++
		}
	}
	if v.i < len(v.b) && (v.b[v.i] == 'e' || v.b[v.i] == 'E') {
		hasExp = true
		v.i++
		if v.i < len(v.b) && (v.b[v.i] == '+' || v.b[v.i] == '-') {
			v.i++
		}
		if v.i >= len(v.b) || v.b[v.i] < '0' || v.b[v.i] > '9' {
			return errors.New("invalid number")
		}
		for v.i < len(v.b) && v.b[v.i] >= '0' && v.b[v.i] <= '9' {
			v.i++
		}
	}
	// Reject float64 overflow (e.g. 1e999) to match json.Decoder's IEEE 754 number parsing.
	if hasExp || v.i-start > 300 {
		if _, err := strconv.ParseFloat(string(v.b[start:v.i]), 64); err != nil {
			return err
		}
	}
	return nil
}

func parse(b []byte) (any, error) {
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
	return v, nil
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
		slices.SortFunc(keys, compareUTF16)
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

// compareUTF16 orders valid UTF-8 strings by their UTF-16 code units, as JCS
// sorts keys: U+E000 to U+FFFF sort after the supplementary planes' surrogates.
func compareUTF16(a, b string) int {
	unit := func(r rune) rune {
		if r >= 0xe000 && r <= 0xffff {
			return r + utf8.MaxRune
		}
		return r
	}
	for a != "" && b != "" {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb {
			return cmp.Compare(unit(ra), unit(rb))
		}
		a, b = a[na:], b[nb:]
	}
	return cmp.Compare(len(a), len(b))
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

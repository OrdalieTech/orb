package protocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestStrictJSON(t *testing.T) {
	for _, s := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `[1]`, "{\"a\":\"\xff\"}", `{"a":"\ud800"}`, `{"a":1e999}`, `{} {}`, `{"a":` + strings.Repeat(`[`, 65) + `0` + strings.Repeat(`]`, 65) + `}`} {
		if _, err := Canonical([]byte(s)); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	for _, s := range []string{`{}`, `{"a":"😀"}`, `{"a":"\ud83d\ude00"}`, `{"a":[1,true,null]}`} {
		if _, err := Canonical([]byte(s)); err != nil {
			t.Errorf("rejected %q: %v", s, err)
		}
	}
}

func TestCanonicalVectors(t *testing.T) {
	vectors := [][2]string{
		{`{"z":-0,"b":1e+30,"a":4.50}`, `{"a":4.5,"b":1e+30,"z":0}`},
		{`{"\ue000":1,"😀":2,"\r":3}`, `{"\r":3,"😀":2,"":1}`},
		{`{"a":"<>&\u2028"}`, "{\"a\":\"<>&\u2028\"}"},
	}
	for _, v := range vectors {
		got, err := Canonical([]byte(v[0]))
		if err != nil || string(got) != v[1] {
			t.Fatalf("got %s %v, want %s", got, err, v[1])
		}
	}
}

func TestFrameLimits(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, []byte(`{"jsonrpc":"2.0","id":"a","method":"bridge.ping"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(&b); err != nil {
		t.Fatal(err)
	}
	for _, n := range []uint32{0, MaxFrame + 1, ^uint32(0)} {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], n)
		if _, err := Read(bytes.NewReader(h[:])); err == nil {
			t.Fatal("accepted", n)
		}
	}
	if err := Write(&b, make([]byte, MaxFrame+1)); err == nil {
		t.Fatal("oversize write")
	}
}

func TestIdentifiersAndCounters(t *testing.T) {
	id := NewID()
	if !ValidID(id) || len(id) != 22 {
		t.Fatal(id)
	}
	for _, s := range []string{"", "01", "-1", "+1", "18446744073709551616"} {
		if _, err := Counter(s); err == nil {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"0", "18446744073709551615"} {
		if _, err := Counter(s); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzCanonical(f *testing.F) {
	f.Add([]byte(`{"a":[1,"x"]}`))
	f.Add([]byte(`{"a":"\ud800"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > MaxFrame {
			return
		}
		v, err := Canonical(b)
		if err != nil {
			return
		}
		w, err := Canonical(v)
		if err != nil || !bytes.Equal(v, w) {
			t.Fatalf("not idempotent %s", v)
		}
	})
}

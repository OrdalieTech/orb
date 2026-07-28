package jsonwire

import (
	"encoding/json"
	"math"
	"testing"
)

func TestMarshalMatchesJSONStringifyStringEscaping(t *testing.T) {
	value := struct {
		Text string `json:"text"`
	}{Text: "<>&\u2028\u2029\\u2028"}
	encoded, err := Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"text\":\"<>&\u2028\u2029\\\\u2028\"}"
	if string(encoded) != want {
		t.Fatalf("encoded = %q, want %q", encoded, want)
	}
}

func TestMarshalNegativeZeroMatchesJSONStringify(t *testing.T) {
	// node -e 'console.log(JSON.stringify(-0), JSON.stringify({a:-0,b:[-0,-0.5],c:"-0"}))'
	// → 0 {"a":0,"b":[0,-0.5],"c":"-0"}
	negativeZero := math.Copysign(0, -1)
	encoded, err := Marshal(negativeZero)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "0" {
		t.Fatalf("Marshal(-0) = %q, want %q", encoded, "0")
	}
	value := struct {
		A float64   `json:"a"`
		B []float64 `json:"b"`
		C string    `json:"c"`
	}{A: negativeZero, B: []float64{negativeZero, -0.5}, C: "-0"}
	encoded, err = Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"a":0,"b":[0,-0.5],"c":"-0"}`; got != want {
		t.Fatalf("encoded = %q, want %q", got, want)
	}
}

func TestMarshalKeepsNonZeroNumbersWithMinusZeroPrefix(t *testing.T) {
	encoded, err := Marshal(json.RawMessage(`{"a":-0.25,"b":"x-0","c":1e-07}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"a":-0.25,"b":"x-0","c":1e-07}`; got != want {
		t.Fatalf("encoded = %q, want %q", got, want)
	}
}

func TestMarshalIndentMatchesJSONStringify(t *testing.T) {
	// node -e 'console.log(JSON.stringify({u:"héllo ✓ <>&"},null,2))'
	value := struct {
		U string `json:"u"`
	}{U: "héllo ✓ <>&"}
	encoded, err := MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), "{\n  \"u\": \"héllo ✓ <>&\"\n}"; got != want {
		t.Fatalf("encoded = %q, want %q", got, want)
	}
}

func TestMarshalStringPreservesWTF8Surrogate(t *testing.T) {
	value := "before" + string([]byte{0xed, 0xa0, 0xbd}) + "after"
	encoded, err := MarshalString(value)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `"before\ud83dafter"`; got != want {
		t.Fatalf("encoded = %q, want %q", got, want)
	}
}

func TestMarshalStringRecombinesWTF8SurrogatePair(t *testing.T) {
	value := string([]byte{0xed, 0xa0, 0xbd, 0xed, 0xb8, 0x80})
	encoded, err := MarshalString(value)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `"😀"`; got != want {
		t.Fatalf("encoded = %q, want %q", got, want)
	}
}

func TestUnmarshalStringPreservesSurrogates(t *testing.T) {
	value, err := UnmarshalString([]byte(`"before\ud800|\udc00|\ud83d\ude00after"`))
	if err != nil {
		t.Fatal(err)
	}
	want := "before" + string([]byte{0xed, 0xa0, 0x80}) + "|" + string([]byte{0xed, 0xb0, 0x80}) + "|😀after"
	if value != want {
		t.Fatalf("decoded = %q, want %q", value, want)
	}
	encoded, err := MarshalString(value)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `"before\ud800|\udc00|😀after"`; got != want {
		t.Fatalf("re-encoded = %s, want %s", got, want)
	}
}

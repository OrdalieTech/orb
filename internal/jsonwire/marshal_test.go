package jsonwire

import "testing"

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

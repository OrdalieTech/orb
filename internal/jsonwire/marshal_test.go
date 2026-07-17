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

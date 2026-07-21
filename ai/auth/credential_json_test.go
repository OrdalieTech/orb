package auth

import (
	"encoding/json"
	"testing"
)

func TestCredentialJSONPreservesUnknownFieldsAndOrder(t *testing.T) {
	input := []byte(`{"type":"oauth","access":"access","refresh":"refresh","expires":42,"enterpriseUrl":"https://example.test","availableModelIds":["a"]}`)
	var credential Credential
	if err := json.Unmarshal(input, &credential); err != nil {
		t.Fatal(err)
	}
	credential.Access = "updated"
	output, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"oauth","access":"updated","refresh":"refresh","expires":42,"enterpriseUrl":"https://example.test","availableModelIds":["a"]}`
	if string(output) != want {
		t.Fatalf("credential JSON = %s, want %s", output, want)
	}
}

func TestCredentialConstructorsUseUpstreamFieldOrder(t *testing.T) {
	tests := []struct {
		credential *Credential
		want       string
	}{
		{APIKeyCredential("key"), `{"type":"api_key","key":"key"}`},
		{OAuthCredential("refresh", "access", 42), `{"type":"oauth","refresh":"refresh","access":"access","expires":42}`},
	}
	for _, test := range tests {
		encoded, err := json.Marshal(test.credential)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != test.want {
			t.Fatalf("credential JSON = %s, want %s", encoded, test.want)
		}
	}
}

func TestOAuthCredentialAccessFirstAndExtraPreserveProviderOrder(t *testing.T) {
	credential := OAuthCredentialAccessFirst("access", "refresh", 123)
	credential.SetExtra("accountId", json.RawMessage(`"account"`))
	encoded, err := credential.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"oauth","access":"access","refresh":"refresh","expires":123,"accountId":"account"}`
	if string(encoded) != want {
		t.Fatalf("credential = %s, want %s", encoded, want)
	}

	credential.SetExtra("accountId", json.RawMessage(`"replaced"`))
	encoded, err = credential.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want = `{"type":"oauth","access":"access","refresh":"refresh","expires":123,"accountId":"replaced"}`
	if string(encoded) != want {
		t.Fatalf("credential after replacement = %s, want %s", encoded, want)
	}
}

func TestCredentialRejectsInvalidTrailer(t *testing.T) {
	var credential Credential
	if err := json.Unmarshal([]byte(`{"type":"api_key"}x`), &credential); err == nil {
		t.Fatal("credential with invalid trailer was accepted")
	}
}

// LOG-m8: pi (TS) stores expires as a JavaScript number, so fractional values
// must survive an auth.json decode/encode cycle as JSON numbers.
func TestLOGm8FractionalExpiresRoundTrips(t *testing.T) {
	input := []byte(`{"type":"oauth","access":"a","refresh":"r","expires":1721556021935.75}`)
	var credential Credential
	if err := json.Unmarshal(input, &credential); err != nil {
		t.Fatalf("float expires rejected: %v", err)
	}
	assertInt64 := func(int64) {}
	assertInt64(credential.Expires)
	if credential.Expires != 1721556021935 {
		t.Fatalf("public expires = %v, want integer-millisecond compatibility", credential.Expires)
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(input) {
		t.Fatalf("fractional expires round trip = %s, want %s", encoded, input)
	}

	clone := credential.Clone()
	encoded, err = json.Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(input) {
		t.Fatalf("cloned fractional expires = %s, want %s", encoded, input)
	}

	credential.Expires++
	encoded, err = json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	wantChanged := `{"type":"oauth","access":"a","refresh":"r","expires":1721556021936}`
	if string(encoded) != wantChanged {
		t.Fatalf("changed expires = %s, want %s", encoded, wantChanged)
	}

	var boundary Credential
	if err := json.Unmarshal([]byte(`{"type":"oauth","expires":2000.75}`), &boundary); err != nil {
		t.Fatal(err)
	}
	if boundary.expiredAt(2000) {
		t.Fatal("fractional expiry was treated as expired before the JavaScript-number boundary")
	}
	if !boundary.expiredAt(2001) {
		t.Fatal("fractional expiry remained valid after the JavaScript-number boundary")
	}
	if err := json.Unmarshal([]byte(`{"type":"oauth","expires":"soon"}`), &credential); err == nil {
		t.Fatal("non-numeric expires was accepted")
	}
}

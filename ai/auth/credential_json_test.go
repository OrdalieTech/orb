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

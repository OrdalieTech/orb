package extensions

import (
	"testing"

	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

func TestOAuthCredentialsMarshalOrderAndEscaping(t *testing.T) {
	credentials := OAuthCredentials{
		Refresh: "r<1>&",
		Access:  "a>2",
		Expires: 42,
		Extra: map[string]any{
			"scope":   "read <all>",
			"account": map[string]any{"id": "x&y"},
			// Declared members win over identically named extras.
			"refresh": "shadowed",
		},
	}
	// node -e 'console.log(JSON.stringify({refresh:"r<1>&",access:"a>2",expires:42,
	//   account:{id:"x&y"},scope:"read <all>"}))'
	want := `{"refresh":"r<1>&","access":"a>2","expires":42,"account":{"id":"x&y"},"scope":"read <all>"}`
	encoded, err := credentials.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}
	viaWire, err := jsonwire.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if string(viaWire) != want {
		t.Fatalf("jsonwire encoded = %s, want %s", viaWire, want)
	}

	var decoded OAuthCredentials
	if err := decoded.UnmarshalJSON(encoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Refresh != credentials.Refresh || decoded.Access != credentials.Access || decoded.Expires != credentials.Expires {
		t.Fatalf("round trip lost declared members: %+v", decoded)
	}
	if decoded.Extra["scope"] != "read <all>" {
		t.Fatalf("round trip lost extra members: %+v", decoded.Extra)
	}
}

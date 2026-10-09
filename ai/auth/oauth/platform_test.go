package oauth

import "testing"

func TestCallbackHostUsesOrbEnvironmentOnly(t *testing.T) {
	t.Setenv("PI_OAUTH_CALLBACK_HOST", "pi.invalid")
	t.Setenv("ORB_OAUTH_CALLBACK_HOST", "")
	if got := callbackHost(); got != "127.0.0.1" {
		t.Fatalf("Pi environment changed Orb callback host: %q", got)
	}
	t.Setenv("ORB_OAUTH_CALLBACK_HOST", "localhost")
	if got := callbackHost(); got != "localhost" {
		t.Fatalf("Orb callback host = %q", got)
	}
}

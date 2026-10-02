package oauth

import (
	"net"
	"net/http"
	"os"
)

// The package's platform defaults, named once; every flow takes overrides
// through its options, which hosts and tests supply.
var (
	defaultHTTPClient = http.DefaultClient
	defaultListen     = net.Listen
)

// callbackHost is PI_OAUTH_CALLBACK_HOST, or 127.0.0.1.
func callbackHost() string {
	if host := os.Getenv("PI_OAUTH_CALLBACK_HOST"); host != "" {
		return host
	}
	return "127.0.0.1"
}

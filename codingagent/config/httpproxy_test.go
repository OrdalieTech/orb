package config

import (
	"os"
	"testing"
)

func TestApplyHTTPProxySettings(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	_ = os.Unsetenv("HTTP_PROXY")
	_ = os.Unsetenv("HTTPS_PROXY")
	ApplyHTTPProxySettings("  ")
	if os.Getenv("HTTP_PROXY") != "" {
		t.Fatal("blank proxy must not set env")
	}
	ApplyHTTPProxySettings(" http://proxy:8080 ")
	if os.Getenv("HTTP_PROXY") != "http://proxy:8080" || os.Getenv("HTTPS_PROXY") != "http://proxy:8080" {
		t.Fatalf("proxy env = %q / %q", os.Getenv("HTTP_PROXY"), os.Getenv("HTTPS_PROXY"))
	}
	t.Setenv("HTTP_PROXY", "http://existing:1")
	ApplyHTTPProxySettings("http://other:2")
	if os.Getenv("HTTP_PROXY") != "http://existing:1" {
		t.Fatal("environment proxy must win over the setting")
	}
}

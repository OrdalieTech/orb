package main

import (
	"crypto/x509"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSystemRootsFallBackWithoutCABundle(t *testing.T) {
	if os.Getenv("ORB_TEST_FALLBACK_ROOTS_CHILD") == "1" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			t.Fatalf("SystemCertPool: %v", err)
		}
		if pool.Equal(x509.NewCertPool()) {
			t.Fatal("system pool is empty without a CA bundle")
		}
		return
	}
	// Roots load once per process, so the empty bundle must be in place before the child starts.
	dir := t.TempDir()
	bundle := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(bundle, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSystemRootsFallBackWithoutCABundle$")
	cmd.Env = append(os.Environ(), "ORB_TEST_FALLBACK_ROOTS_CHILD=1", "SSL_CERT_FILE="+bundle, "SSL_CERT_DIR="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, output)
	}
}

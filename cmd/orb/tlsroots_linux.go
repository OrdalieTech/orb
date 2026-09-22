package main

// Minimal Linux containers often ship without a CA bundle. pi verifies TLS there with Node's
// compiled-in Mozilla roots, so the CLI carries the same roots as a fallback; a system bundle,
// when present, still takes precedence.
import _ "golang.org/x/crypto/x509roots/fallback"

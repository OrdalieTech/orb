package bridge

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func TestPinnedTLSAndWrongIdentity(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		a, b := newBridge(t), newBridge(t)
		serverConfig, err := b.TLSConfig("", true)
		if err != nil {
			t.Fatal(err)
		}
		pin := b.PeerID()
		if wrong {
			pin = a.PeerID()
		}
		clientConfig, err := a.TLSConfig(pin, false)
		if err != nil {
			t.Fatal(err)
		}
		x, y := net.Pipe()
		client, server := tls.Client(x, clientConfig), tls.Server(y, serverConfig)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan error, 1)
		go func() { done <- server.HandshakeContext(ctx) }()
		err = client.HandshakeContext(ctx)
		if wrong && err == nil {
			t.Fatal("wrong pin accepted")
		}
		if !wrong && err != nil {
			t.Fatal(err)
		}
		_ = client.Close()
		_ = server.Close()
		cancel()
		<-done
	}
}

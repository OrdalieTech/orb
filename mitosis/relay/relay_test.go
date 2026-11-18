package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/mitosis"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublic(t *testing.T) {
	for addr, public := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "::1": false, "10.1.2.3": false, "192.168.1.1": false, "172.16.0.1": false,
		"100.101.102.103": false, "169.254.169.254": false, "fe80::1": false, "fd00::1": false,
		"::ffff:127.0.0.1": false, "0.0.0.0": false, "224.0.0.1": false,
	} {
		if got := Public(netip.MustParseAddr(addr)); got != public {
			t.Errorf("Public(%s) = %v", addr, got)
		}
	}
}

// A streamed response reaches the peer whole, through the device that grants the call, while a
// device that refuses it passes the request on.
func TestTransportStreamsThroughAGrantingDevice(t *testing.T) {
	stream, send := io.Pipe()
	server := &Server{Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.example/v1?q=1" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("relayed %s %s %v", r.Method, r.URL, r.Header)
		}
		if body, _ := io.ReadAll(r.Body); string(body) != "prompt" {
			t.Errorf("relayed body %q", body)
		}
		return &http.Response{StatusCode: 201, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: stream}, nil
	})}}
	transport := &Transport{
		Devices: func(context.Context) []string { return []string{"stranger", "laptop"} },
		Call: func(ctx context.Context, device, method string, params, result any) error {
			if device == "stranger" {
				return mitosis.Fail("unauthorized")
			}
			raw, err := server.Handle(ctx, method, mitosis.JSON(params))
			if err == nil && result != nil {
				err = json.Unmarshal(raw, result)
			}
			return err
		},
	}
	go func() {
		for _, event := range []string{"data: one\n\n", strings.Repeat("x", 600<<10), "data: two\n\n"} {
			_, _ = send.Write([]byte(event))
		}
		_ = send.Close()
	}()
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.example/v1?q=1", strings.NewReader("prompt"))
	request.Header.Set("Authorization", "Bearer k")
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 201 || response.Header.Get("Content-Type") != "text/event-stream" || string(body) != "data: one\n\n"+strings.Repeat("x", 600<<10)+"data: two\n\n" {
		t.Fatalf("relayed %d %v, %d bytes, %v", response.StatusCode, response.Header, len(body), err)
	}
	if _, err := server.open(http.MethodGet, "http://api.example/", nil, nil); mitosis.Code(err) != "invalid_params" {
		t.Fatalf("plain http relayed: %v", err)
	}
}

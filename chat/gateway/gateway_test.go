package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/chat"
)

type stubAdapter struct{}

func (stubAdapter) Platform() string { return "stub" }
func (stubAdapter) Account() string  { return "" }
func (stubAdapter) NewDelivery(chat.ConversationKey, string, string) chat.Delivery {
	return nil
}
func (stubAdapter) Download(context.Context, chat.AttachmentRef) (io.ReadCloser, string, error) {
	return nil, "", nil
}

func TestWebhooksRoutesMultiplePlatformsAndValidatesCustomPath(t *testing.T) {
	handler := func(platform string) func(func(chat.Message) error) http.Handler {
		return func(publish func(chat.Message) error) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = publish(chat.Message{Platform: platform, SenderID: "u1"})
				w.WriteHeader(http.StatusAccepted)
			})
		}
	}
	webhooks := map[string]func(func(chat.Message) error) http.Handler{
		"slack": handler("slack"),
		"teams": handler("teams"),
	}
	if _, err := Webhooks("127.0.0.1:0", "/custom", webhooks); err == nil {
		t.Fatal("expected ORB_CHAT_PATH with multiple webhook platforms to be rejected")
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	ingress, err := Webhooks(addr, "", webhooks)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var seen []string
	done := make(chan error, 1)
	go func() {
		done <- ingress(ctx, func(msg chat.Message) error {
			mu.Lock()
			seen = append(seen, msg.Platform)
			mu.Unlock()
			return nil
		})
	}()

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, reqErr := client.Post("http://"+addr+"/slack", "application/json", strings.NewReader("{}"))
		if reqErr == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook server did not start: %v", reqErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := client.Post("http://"+addr+"/teams", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Fatalf("ingress exit = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"slack", "teams"}) {
		t.Fatalf("published platforms = %v, want [slack teams]", seen)
	}
}

func TestRunRefusesSecondGatewayOnSameDataDir(t *testing.T) {
	dataDir := t.TempDir()
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- Run(ctx, Options{
			DataDir:   dataDir,
			Adapters:  []chat.Adapter{stubAdapter{}},
			Authorize: func(chat.Message) error { return nil },
			Fronts: []func(context.Context) error{
				func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					return ctx.Err()
				},
			},
		})
	}()
	select {
	case <-started:
	case err := <-firstDone:
		t.Fatalf("first gateway failed to start: %v", err)
	}

	err := Run(context.Background(), Options{
		DataDir:   dataDir,
		Adapters:  []chat.Adapter{stubAdapter{}},
		Authorize: func(chat.Message) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second gateway error = %v, want already in use", err)
	}
	cancel()
	if err := <-firstDone; err != nil {
		t.Fatalf("first gateway exit = %v", err)
	}
}

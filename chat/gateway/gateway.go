// Package gateway runs an agent's chat platforms as one process: the
// adapters and their ingress (polling or a webhook server) feed one
// at-least-once processor over local sessions, beside the fronts that drive
// the agent themselves. Its caller reads the environment and supplies the
// persistence (DECISIONS.md D27, "Team agents").
package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/chat"
	"github.com/gofrs/flock"
)

// Authorizer admits only the platform user IDs in allowed, a comma-separated
// list (ORB_CHAT_ALLOWED_SENDERS).
func Authorizer(allowed string) (func(chat.Message) error, error) {
	ids := map[string]struct{}{}
	for _, id := range strings.Split(allowed, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("ORB_CHAT_ALLOWED_SENDERS is required")
	}
	return func(message chat.Message) error {
		if _, ok := ids[message.SenderID]; ok {
			return nil
		}
		return fmt.Errorf("sender %s is not in ORB_CHAT_ALLOWED_SENDERS", message.SenderID)
	}, nil
}

// Webhook serves a platform's webhook on listen (default 127.0.0.1:8080) at
// path (default /<platform>, ORB_CHAT_PATH).
func Webhook(platform, listen, path string, webhook func(func(chat.Message) error) http.Handler) func(context.Context, func(chat.Message) error) error {
	return func(ctx context.Context, publish func(chat.Message) error) error {
		if listen == "" {
			listen = "127.0.0.1:8080"
		}
		if path == "" {
			path = "/" + platform
		}
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "{} \t\r\n") {
			return errors.New("ORB_CHAT_PATH must be a literal path starting with /")
		}
		mux := http.NewServeMux()
		mux.Handle(path, webhook(publish))
		// ponytail: one stdlib webhook server per process; terminate TLS and
		// multiplex public routes in the deployment's reverse proxy.
		server := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		result := make(chan error, 1)
		go func() { result <- server.ListenAndServe() }()
		select {
		case err := <-result:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownContext); err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}

type Options struct {
	// DataDir holds the gateway's sessions and, without Spool, its spool file.
	DataDir   string
	Adapters  []chat.Adapter
	Ingresses []func(context.Context, func(chat.Message) error) error
	// Fronts run beside the gateway (see RunFronts).
	Fronts    []func(context.Context) error
	Authorize func(chat.Message) error
	Provider  []chat.LocalProviderOption
	// Spool, when set, holds the queue instead of DataDir/spool.jsonl, and
	// the gateway then locks DataDir for this process.
	Spool chat.Spool
	Log   io.Writer
}

// Run serves the platforms and fronts until one ends or ctx does.
func Run(ctx context.Context, options Options) error {
	provider, err := chat.NewLocalProvider(filepath.Join(options.DataDir, "sessions"), options.Provider...)
	if err != nil {
		return err
	}
	processor, err := chat.New(chat.Options{Sessions: provider, Adapters: options.Adapters, Authorize: options.Authorize})
	if err != nil {
		return err
	}
	var local *chat.Local
	if options.Spool != nil {
		lock := flock.New(filepath.Join(options.DataDir, "gateway.lock"))
		held, lockErr := lock.TryLock()
		if lockErr != nil || !held {
			_ = lock.Close()
			return errors.New("chat gateway data is already in use")
		}
		defer func() { _ = lock.Close() }()
		local, err = chat.NewLocalWithSpool(processor, options.Spool)
	} else {
		local, err = chat.NewLocal(processor, filepath.Join(options.DataDir, "spool.jsonl"))
	}
	if err != nil {
		return err
	}
	fronts := options.Fronts
	for _, ingress := range options.Ingresses {
		fronts = append(fronts, func(ctx context.Context) error { return ingress(ctx, local.Publish) })
	}
	return RunFronts(ctx, fronts, options.Log, func(ctx context.Context) error {
		return errors.Join(local.Close(ctx), processor.Close(ctx))
	})
}

// RunFronts runs an agent's fronts until one ends or ctx does, stops the
// others, then closes; the ending front's error is the result, and an ended
// ctx is a clean stop.
func RunFronts(ctx context.Context, fronts []func(context.Context) error, log io.Writer, close func(context.Context) error) error {
	running, cancel := context.WithCancel(ctx)
	results := make(chan error, len(fronts))
	for _, front := range fronts {
		go func() { results <- front(running) }()
	}
	_, _ = fmt.Fprintln(log, "agent running; press Ctrl-C to stop")
	err := <-results
	if ctx.Err() != nil {
		err = nil
	}
	cancel()
	for range len(fronts) - 1 {
		<-results
	}
	if close != nil {
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelShutdown()
		err = errors.Join(err, close(shutdown))
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

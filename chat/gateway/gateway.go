// Package gateway runs an agent's chat platforms as one process: the
// adapters and their ingress (polling or a webhook server) feed one
// at-least-once processor over local sessions, beside the fronts that drive
// the agent themselves. Its caller reads the environment and supplies the
// persistence (DECISIONS.md D27, "Team agents").
package gateway

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
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

// Webhooks serves every webhook platform from one server on listen (default
// 127.0.0.1:8080), each at /<platform>; path (ORB_CHAT_PATH) replaces that
// only when there is a single platform.
func Webhooks(listen, path string, webhooks map[string]func(func(chat.Message) error) http.Handler) (func(context.Context, func(chat.Message) error) error, error) {
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	if path != "" && len(webhooks) > 1 {
		return nil, errors.New("ORB_CHAT_PATH applies to a single webhook platform; unset it to serve each at /<platform>")
	}
	if path != "" && (!strings.HasPrefix(path, "/") || strings.ContainsAny(path, "{} \t\r\n")) {
		return nil, errors.New("ORB_CHAT_PATH must be a literal path starting with /")
	}
	return func(ctx context.Context, publish func(chat.Message) error) error {
		mux := http.NewServeMux()
		for platform, webhook := range webhooks {
			mux.Handle(cmp.Or(path, "/"+platform), webhook(publish))
		}
		// ponytail: one stdlib webhook server per process; terminate TLS in the
		// deployment's reverse proxy.
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
	}, nil
}

type Options struct {
	// DataDir holds the gateway's sessions, its lock and, without Spool, its
	// spool file.
	DataDir   string
	Adapters  []chat.Adapter
	Ingresses []func(context.Context, func(chat.Message) error) error
	// Fronts run beside the gateway (see RunFronts).
	Fronts    []func(context.Context) error
	Authorize func(chat.Message) error
	Provider  []chat.LocalProviderOption
	// Spool, when set, holds the queue instead of DataDir/spool.jsonl.
	Spool chat.Spool
	Log   io.Writer
	// Logger receives the processor's diagnostics; give the adapters the same.
	Logger *slog.Logger
}

// Run serves the platforms and fronts until one ends or ctx does.
func Run(ctx context.Context, options Options) error {
	provider, err := chat.NewLocalProvider(filepath.Join(options.DataDir, "sessions"), options.Provider...)
	if err != nil {
		return err
	}
	processor, err := chat.New(chat.Options{Sessions: provider, Adapters: options.Adapters, Authorize: options.Authorize, Logger: options.Logger})
	if err != nil {
		return err
	}
	lock := flock.New(filepath.Join(options.DataDir, "gateway.lock"))
	held, err := lock.TryLock()
	if err != nil || !held {
		_ = lock.Close()
		return errors.New("chat gateway data is already in use")
	}
	defer func() { _ = lock.Close() }()
	var local *chat.Local
	if options.Spool != nil {
		local, err = chat.NewLocalWithSpool(processor, options.Spool)
	} else {
		local, err = chat.NewLocal(processor, filepath.Join(options.DataDir, "spool.jsonl"))
	}
	if err != nil {
		return err
	}
	fronts := slices.Clip(options.Fronts)
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
	log = cmp.Or[io.Writer](log, io.Discard)
	if len(fronts) == 0 {
		if close != nil {
			shutdown, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelShutdown()
			return close(shutdown)
		}
		return nil
	}
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

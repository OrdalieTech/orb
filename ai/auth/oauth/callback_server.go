package oauth

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
)

// callbackOptions configure the loopback OAuth redirect handler that the
// browser sign-in flows share.
type callbackOptions[T any] struct {
	// provider names the provider on the browser page, for example "OpenAI".
	provider string
	listen   func(network, address string) (net.Listener, error)
	host     string
	// port 0 picks a free port.
	port int
	path string
	// redirectHost is the host in redirectURI when it differs from host.
	redirectHost string
	// state is the expected state parameter; empty when the provider sends none.
	state string
	// ipv6Loopback also holds [::1] on the port when the redirect names
	// localhost, which a browser may resolve to either loopback address.
	ipv6Loopback bool
	// complete finishes sign-in with the callback's query (it carries a code)
	// before the browser page is sent, so the page can show exchange failures.
	complete func(query url.Values) (T, error)
	timeout  time.Duration
}

type callbackOutcome[T any] struct {
	value T
	err   error
}

type callbackServer[T any] struct {
	redirectURI string
	outcome     chan callbackOutcome[T]
	server      *http.Server
	listeners   []net.Listener
	served      chan struct{}

	mu       sync.Mutex
	claimed  bool
	settled  bool
	timer    *time.Timer
	provider string
}

// startCallbackServer listens for the provider's redirect. It fails when the
// port is taken, and callers then fall back to a pasted code or redirect URL.
func startCallbackServer[T any](options callbackOptions[T]) (*callbackServer[T], error) {
	listen := options.listen
	if listen == nil {
		listen = defaultListen
	}
	first, err := listen("tcp", net.JoinHostPort(options.host, strconv.Itoa(options.port)))
	if err != nil {
		return nil, err
	}
	server := &callbackServer[T]{outcome: make(chan callbackOutcome[T], 1), served: make(chan struct{}), provider: options.provider, listeners: []net.Listener{first}}
	port := first.Addr().(*net.TCPAddr).Port
	if options.ipv6Loopback && options.host == "127.0.0.1" && options.redirectHost == "localhost" {
		// The state may carry the PKCE verifier, so a program on [::1] instead
		// would receive all it needs to sign in as the owner. A machine without
		// IPv6 has no [::1] to hold.
		address := net.JoinHostPort("::1", strconv.Itoa(port))
		if other, listenErr := listen("tcp", address); listenErr == nil {
			if other != first { // a test's single listener
				server.listeners = append(server.listeners, other)
			}
		} else if free, freeErr := listen("tcp", "[::1]:0"); freeErr == nil {
			_ = free.Close()
			_ = first.Close()
			return nil, fmt.Errorf("%w: another program is listening on %s, where the sign-in may return; close it and try again", errLoopbackTaken, address)
		}
	}
	host := options.redirectHost
	if host == "" {
		host = options.host
	}
	server.redirectURI = "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + options.path
	server.server = &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		server.handle(writer, request, options)
	}), ReadHeaderTimeout: 10 * time.Second}
	var serving sync.WaitGroup
	for _, listener := range server.listeners {
		serving.Go(func() { _ = server.server.Serve(listener) })
	}
	go func() { serving.Wait(); close(server.served) }()
	if options.timeout > 0 {
		server.timer = time.AfterFunc(options.timeout, func() {
			server.finish(callbackOutcome[T]{err: fmt.Errorf("%s sign-in timed out", options.provider)})
		})
	}
	return server, nil
}

func (server *callbackServer[T]) handle(writer http.ResponseWriter, request *http.Request, options callbackOptions[T]) {
	page := func(status int, html string) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, html)
	}
	query := request.URL.Query()
	if request.Method != http.MethodGet || request.URL.Path != options.path {
		page(http.StatusNotFound, errorPage("Callback route not found."))
		return
	}
	if options.state != "" && query.Get("state") != options.state {
		page(http.StatusBadRequest, errorPage("State mismatch."))
		return
	}
	server.mu.Lock()
	done := server.claimed || server.settled
	server.mu.Unlock()
	if done {
		page(http.StatusConflict, errorPage("This sign-in has already been handled."))
		return
	}
	if oauthError := query.Get("error"); oauthError != "" {
		description := cmp.Or(query.Get("error_description"), oauthError)
		page(http.StatusBadRequest, errorPageWithDetails(options.provider+" authorization failed.", description))
		server.finish(callbackOutcome[T]{err: fmt.Errorf("%s authorization failed: %s", options.provider, description)})
		return
	}
	code := query.Get("code")
	if code == "" {
		page(http.StatusBadRequest, errorPage("Missing authorization code."))
		return
	}
	server.mu.Lock()
	if server.claimed || server.settled {
		server.mu.Unlock()
		page(http.StatusConflict, errorPage("This sign-in has already been handled."))
		return
	}
	server.claimed = true
	server.mu.Unlock()
	value, err := options.complete(query)
	if err != nil {
		page(http.StatusBadGateway, errorPageWithDetails(options.provider+" sign-in failed.", err.Error()))
		server.finish(callbackOutcome[T]{err: err})
		return
	}
	page(http.StatusOK, successPage("Signed in to "+options.provider+"."))
	server.finish(callbackOutcome[T]{value: value})
}

func (server *callbackServer[T]) finish(outcome callbackOutcome[T]) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.settled {
		return
	}
	server.settled = true
	if server.timer != nil {
		server.timer.Stop()
	}
	server.outcome <- outcome
}

// cancel stops waiting for the browser unless a callback is already being
// completed, and reports whether it did.
func (server *callbackServer[T]) cancel() bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.claimed || server.settled {
		return !server.claimed
	}
	server.settled = true
	if server.timer != nil {
		server.timer.Stop()
	}
	return true
}

func (server *callbackServer[T]) close() {
	server.cancel()
	// Let the browser receive the callback page before closing its connection.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.server.Shutdown(ctx)
	_ = server.server.Close()
	<-server.served
}

// waitForCallbackOrManualInput waits for the browser callback, or for a pasted
// code or redirect URL when the browser cannot reach the loopback server (over
// SSH, say). With a nil server only the prompt is used. It returns the
// callback's value, or the pasted input with fromCallback false.
func waitForCallbackOrManualInput[T any](ctx context.Context, interaction auth.AuthInteraction, server *callbackServer[T], prompt auth.AuthPrompt) (value T, input string, fromCallback bool, err error) {
	manualCtx, cancelManual := context.WithCancel(ctx)
	defer cancelManual()
	manual := make(chan manualResult, 1)
	go func() {
		prompt.Type = auth.PromptManualCode
		answer, promptErr := interaction.Prompt(manualCtx, prompt)
		manual <- manualResult{input: answer, err: promptErr}
	}()
	var outcomes <-chan callbackOutcome[T]
	if server != nil {
		outcomes = server.outcome
	}
	select {
	case <-ctx.Done():
		return value, "", false, ctx.Err()
	case outcome := <-outcomes:
		return outcome.value, "", outcome.err == nil, outcome.err
	case result := <-manual:
		// A callback already being completed wins over the paste.
		if server != nil && !server.cancel() {
			outcome := <-outcomes
			return outcome.value, "", outcome.err == nil, outcome.err
		}
		return value, strings.TrimSpace(result.input), false, result.err
	}
}

type manualResult struct {
	input string
	err   error
}

// errLoopbackTaken refuses a sign-in instead of falling back to a paste.
var errLoopbackTaken = errors.New("loopback callback address taken")

var errMissingAuthorizationCode = errors.New("Missing authorization code") //nolint:staticcheck // Upstream capitalization is observable.

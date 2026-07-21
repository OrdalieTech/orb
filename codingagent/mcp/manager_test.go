package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagerRegistersExecutesAndStreamsExampleTool(t *testing.T) {
	server := exampleServer()
	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "example", Command: "in-memory"}})
	var serverSessionsMu sync.Mutex
	var serverSessions []*mcpsdk.ServerSession
	manager.connect = func(ctx, _ context.Context, _ ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
		serverSession, err := server.Connect(ctx, serverTransport, nil)
		if err != nil {
			return nil, err
		}
		serverSessionsMu.Lock()
		serverSessions = append(serverSessions, serverSession)
		serverSessionsMu.Unlock()
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "pigo-test", Version: "0"}, options)
		return client.Connect(ctx, tracker.wrapTransport(clientTransport), nil)
	}
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)

	tools := runner.AllRegisteredTools()
	if len(tools) != 1 || !strings.HasPrefix(tools[0].Definition.Name, "mcp__example__echo_") {
		t.Fatalf("registered tools = %#v", tools)
	}
	active.set([]string{tools[0].Definition.Name})
	updates := make(chan agent.AgentToolResult, 1)
	result, err := extensions.WrapRegisteredTool(tools[0], runner).Execute(
		context.Background(), "call-1", map[string]any{"text": "hello"}, func(update agent.AgentToolResult) { updates <- update },
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentText(result.Content); got != "echo: hello" {
		t.Fatalf("result text = %q", got)
	}
	image, ok := result.Content[1].(*ai.ImageContent)
	if !ok || image.Data != "cGl4ZWxz" || image.MimeType != "image/png" {
		t.Fatalf("image result = %#v", result.Content[1])
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["server"] != "example" || details["tool"] != "echo" || !reflect.DeepEqual(details["structuredContent"], map[string]any{"echoed": "hello"}) {
		t.Fatalf("details = %#v", result.Details)
	}
	select {
	case update := <-updates:
		if contentText(update.Content) != "halfway" {
			t.Fatalf("progress = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("missing MCP progress update")
	}

	serverSessionsMu.Lock()
	sessions := append([]*mcpsdk.ServerSession(nil), serverSessions...)
	serverSessionsMu.Unlock()
	if len(sessions) != 1 {
		t.Fatalf("server sessions = %d", len(sessions))
	}
}

func TestManagerDeliversWirePriorProgressBeforeAgentSettlesTool(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "progress", Version: "1"}, nil)
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: "work"}, func(
		ctx context.Context, request *mcpsdk.CallToolRequest, _ map[string]any,
	) (*mcpsdk.CallToolResult, any, error) {
		token := request.Params.GetProgressToken()
		for index, message := range []string{"first", "second"} {
			if err := request.Session.NotifyProgress(ctx, &mcpsdk.ProgressNotificationParams{
				ProgressToken: token, Progress: float64(index + 1), Total: 3, Message: message,
			}); err != nil {
				return nil, nil, err
			}
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "done"}}}, nil, nil
	})

	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "progress", Command: "in-memory"}})
	progressStarted := make(chan struct{})
	releaseProgress := make(chan struct{})
	manager.connect = func(ctx, _ context.Context, _ ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		original := options.ProgressNotificationHandler
		copied := *options
		var once sync.Once
		copied.ProgressNotificationHandler = func(ctx context.Context, request *mcpsdk.ProgressNotificationClientRequest) {
			once.Do(func() {
				close(progressStarted)
				<-releaseProgress
			})
			original(ctx, request)
		}
		serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
		if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
			return nil, err
		}
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "pigo-test", Version: "0"}, &copied)
		return client.Connect(ctx, tracker.wrapTransport(clientTransport), nil)
	}
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	tools := runner.AllRegisteredTools()
	active.set([]string{tools[0].Definition.Name})
	tool := extensions.WrapRegisteredTool(tools[0], runner)
	responses := []*ai.AssistantMessage{
		{
			Content: ai.AssistantContent{&ai.ToolCall{ID: "call-delayed", Name: tool.Spec().Name, Arguments: map[string]any{}}},
			API:     "test", Provider: "test", Model: "test-model", Usage: ai.Usage{Cost: ai.Cost{}}, StopReason: ai.StopReasonToolUse,
		},
		{API: "test", Provider: "test", Model: "test-model", Usage: ai.Usage{Cost: ai.Cost{}}, StopReason: ai.StopReasonStop},
	}
	stream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		message := responses[0]
		responses = responses[1:]
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			yield(ai.DoneEvent{Reason: message.StopReason, Message: message}, nil)
		}, nil
	}
	var eventsMu sync.Mutex
	var updates []string
	toolEnded := false
	done := make(chan error, 1)
	go func() {
		_, err := agent.RunLoop(
			context.Background(),
			agent.AgentMessages{&ai.UserMessage{Content: ai.NewUserText("work")}},
			agent.AgentContext{Tools: []agent.AgentTool{tool}},
			agent.AgentLoopConfig{Model: &ai.Model{ID: "test-model", API: "test", Provider: "test"}},
			func(_ context.Context, event agent.AgentEvent) error {
				eventsMu.Lock()
				defer eventsMu.Unlock()
				switch event := event.(type) {
				case agent.ToolExecutionUpdateEvent:
					updates = append(updates, contentText(event.PartialResult.Content))
				case agent.ToolExecutionEndEvent:
					toolEnded = true
				}
				return nil
			},
			stream,
		)
		done <- err
	}()

	select {
	case <-progressStarted:
	case <-time.After(time.Second):
		t.Fatal("progress handler did not start")
	}
	select {
	case err := <-done:
		close(releaseProgress)
		if err != nil {
			t.Fatal(err)
		}
		t.Fatal("agent settled the MCP tool before wire-prior progress was delivered")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseProgress)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not settle after progress was delivered")
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if !toolEnded {
		t.Fatal("missing tool execution end event")
	}
	counts := make(map[string]int, len(updates))
	for _, update := range updates {
		counts[update]++
	}
	if want := map[string]int{"first": 1, "second": 1}; !reflect.DeepEqual(counts, want) {
		t.Fatalf("progress updates = %v, want %v", updates, want)
	}
}

func TestManagerDoesNotCrossWireOverlappingReusedToolCallIDs(t *testing.T) {
	type input struct {
		Label string `json:"label"`
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "overlap", Version: "1"}, nil)
	started := make(chan string, 2)
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	var tokensMu sync.Mutex
	tokens := make(map[string]string)
	mcpsdk.AddTool[input, any](server, &mcpsdk.Tool{Name: "work"}, func(
		ctx context.Context, request *mcpsdk.CallToolRequest, value input,
	) (*mcpsdk.CallToolResult, any, error) {
		token := fmt.Sprint(request.Params.GetProgressToken())
		tokensMu.Lock()
		tokens[value.Label] = token
		tokensMu.Unlock()
		started <- value.Label
		if value.Label == "first" {
			<-firstRelease
		} else {
			<-secondRelease
		}
		if err := request.Session.NotifyProgress(ctx, &mcpsdk.ProgressNotificationParams{
			ProgressToken: token, Progress: 1, Total: 2, Message: value.Label,
		}); err != nil {
			return nil, nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: value.Label}}}, nil, nil
	})

	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "overlap", Command: "in-memory"}})
	manager.connect = inMemoryConnector(server)
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	registered := runner.AllRegisteredTools()[0]
	active.set([]string{registered.Definition.Name})
	tool := extensions.WrapRegisteredTool(registered, runner)

	type execution struct {
		result agent.AgentToolResult
		err    error
	}
	firstUpdates := make(chan string, 2)
	secondUpdates := make(chan string, 2)
	firstDone := make(chan execution, 1)
	secondDone := make(chan execution, 1)
	var firstReleaseOnce sync.Once
	var secondReleaseOnce sync.Once
	releaseFirst := func() { firstReleaseOnce.Do(func() { close(firstRelease) }) }
	releaseSecond := func() { secondReleaseOnce.Do(func() { close(secondRelease) }) }
	defer releaseFirst()
	defer releaseSecond()
	go func() {
		result, err := tool.Execute(context.Background(), "reused", map[string]any{"label": "first"}, func(update agent.AgentToolResult) {
			firstUpdates <- contentText(update.Content)
		})
		firstDone <- execution{result: result, err: err}
	}()
	if got := <-started; got != "first" {
		t.Fatalf("first started call = %q", got)
	}
	go func() {
		result, err := tool.Execute(context.Background(), "reused", map[string]any{"label": "second"}, func(update agent.AgentToolResult) {
			secondUpdates <- contentText(update.Content)
		})
		secondDone <- execution{result: result, err: err}
	}()
	if got := <-started; got != "second" {
		t.Fatalf("second started call = %q", got)
	}

	releaseFirst()
	first := <-firstDone
	if first.err != nil || contentText(first.result.Content) != "first" {
		t.Fatalf("first result = %#v, error = %v", first.result, first.err)
	}
	select {
	case got := <-firstUpdates:
		if got != "first" {
			t.Fatalf("first progress = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first execution did not receive its progress")
	}
	releaseSecond()
	second := <-secondDone
	if second.err != nil || contentText(second.result.Content) != "second" {
		t.Fatalf("second result = %#v, error = %v", second.result, second.err)
	}
	select {
	case got := <-secondUpdates:
		if got != "second" {
			t.Fatalf("second progress = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second execution did not receive its progress")
	}
	tokensMu.Lock()
	defer tokensMu.Unlock()
	if tokens["first"] == tokens["second"] {
		t.Fatalf("overlapping executions reused progress token %q", tokens["first"])
	}
}

func TestManagerDoesNotCrossWireLateProgressAfterSequentialIDReuse(t *testing.T) {
	type input struct {
		Label string `json:"label"`
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "sequential", Version: "1"}, nil)
	releaseOldProgress := make(chan struct{})
	releaseSecond := make(chan struct{})
	var releaseOldOnce sync.Once
	var releaseSecondOnce sync.Once
	releaseOld := func() { releaseOldOnce.Do(func() { close(releaseOldProgress) }) }
	finishSecond := func() { releaseSecondOnce.Do(func() { close(releaseSecond) }) }
	defer releaseOld()
	defer finishSecond()
	secondStarted := make(chan struct{})
	oldProgressSent := make(chan error, 1)
	var tokensMu sync.Mutex
	tokens := make(map[string]string)
	mcpsdk.AddTool[input, any](server, &mcpsdk.Tool{Name: "work"}, func(
		ctx context.Context, request *mcpsdk.CallToolRequest, value input,
	) (*mcpsdk.CallToolResult, any, error) {
		token := fmt.Sprint(request.Params.GetProgressToken())
		tokensMu.Lock()
		tokens[value.Label] = token
		tokensMu.Unlock()
		if value.Label == "first" {
			session := request.Session
			go func() {
				<-releaseOldProgress
				oldProgressSent <- session.NotifyProgress(context.Background(), &mcpsdk.ProgressNotificationParams{
					ProgressToken: token, Progress: 1, Total: 2, Message: "late-first",
				})
			}()
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "first"}}}, nil, nil
		}
		close(secondStarted)
		<-releaseSecond
		if err := request.Session.NotifyProgress(ctx, &mcpsdk.ProgressNotificationParams{
			ProgressToken: token, Progress: 1, Total: 1, Message: "second",
		}); err != nil {
			return nil, nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "second"}}}, nil, nil
	})

	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "sequential", Command: "in-memory"}})
	progressHandled := make(chan string, 2)
	manager.connect = func(ctx, _ context.Context, _ ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		copied := *options
		original := options.ProgressNotificationHandler
		copied.ProgressNotificationHandler = func(ctx context.Context, request *mcpsdk.ProgressNotificationClientRequest) {
			original(ctx, request)
			progressHandled <- request.Params.Message
		}
		serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
		if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
			return nil, err
		}
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "pigo-test", Version: "0"}, &copied)
		return client.Connect(ctx, tracker.wrapTransport(clientTransport), nil)
	}
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	registered := runner.AllRegisteredTools()[0]
	active.set([]string{registered.Definition.Name})
	tool := extensions.WrapRegisteredTool(registered, runner)
	firstUpdates := make(chan string, 1)
	first, err := tool.Execute(context.Background(), "reused", map[string]any{"label": "first"}, func(update agent.AgentToolResult) {
		firstUpdates <- contentText(update.Content)
	})
	if err != nil || contentText(first.Content) != "first" {
		t.Fatalf("first result = %#v, error = %v", first, err)
	}
	select {
	case update := <-firstUpdates:
		t.Fatalf("first execution received unexpected progress %q", update)
	default:
	}

	type execution struct {
		result agent.AgentToolResult
		err    error
	}
	secondUpdates := make(chan string, 2)
	secondDone := make(chan execution, 1)
	go func() {
		result, err := tool.Execute(context.Background(), "reused", map[string]any{"label": "second"}, func(update agent.AgentToolResult) {
			secondUpdates <- contentText(update.Content)
		})
		secondDone <- execution{result: result, err: err}
	}()
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second execution did not start")
	}
	releaseOld()
	if err := <-oldProgressSent; err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-progressHandled:
		if message != "late-first" {
			t.Fatalf("handled progress = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("late first progress was not handled")
	}
	select {
	case update := <-secondUpdates:
		finishSecond()
		t.Fatalf("late first progress crossed into second execution: %q", update)
	default:
	}
	finishSecond()
	select {
	case second := <-secondDone:
		if second.err != nil || contentText(second.result.Content) != "second" {
			t.Fatalf("second result = %#v, error = %v", second.result, second.err)
		}
	case <-time.After(time.Second):
		t.Fatal("second execution did not finish")
	}
	select {
	case update := <-secondUpdates:
		if update != "second" {
			t.Fatalf("second progress = %q", update)
		}
	case <-time.After(time.Second):
		t.Fatal("second execution did not receive progress")
	}
	manager.mu.Lock()
	progressRegistrations := len(manager.progress)
	manager.mu.Unlock()
	if progressRegistrations != 0 {
		t.Fatalf("progress registrations after terminal update = %d", progressRegistrations)
	}
	tokensMu.Lock()
	defer tokensMu.Unlock()
	if tokens["first"] == tokens["second"] {
		t.Fatalf("sequential executions reused progress token %q", tokens["first"])
	}
}

func TestManagerRefreshesDynamicToolList(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "dynamic", Version: "1"}, nil)
	addTextTool(server, "first")
	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "dynamic", Command: "in-memory"}})
	manager.connect = inMemoryConnector(server)
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)

	initial := runner.AllRegisteredTools()
	if len(initial) != 1 {
		t.Fatalf("initial tools = %d", len(initial))
	}
	active.set([]string{initial[0].Definition.Name})
	addTextTool(server, "second")
	eventually(t, func() bool {
		return runner.ToolDefinition(registeredToolName("dynamic", "second")) != nil && len(active.get()) == 2
	})
	server.RemoveTools("first")
	eventually(t, func() bool {
		return reflect.DeepEqual(active.get(), []string{registeredToolName("dynamic", "second")})
	})
	_, err := initial[0].Definition.Execute(context.Background(), "old", map[string]any{}, nil, runner.CreateContext())
	if err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("removed tool error = %v", err)
	}
}

func TestManagerUsesStreamableHTTPAndHeaders(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "http", Version: "1"}, nil)
	addTextTool(server, "ping")
	var headerSeen atomic.Bool
	var negotiatedHeaderSeen atomic.Bool
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "Bearer secret" {
			headerSeen.Store(true)
		}
		if request.Header.Get("Mcp-Session-Id") != "" && request.Header.Get("MCP-Protocol-Version") != "" {
			negotiatedHeaderSeen.Store(true)
		}
		mcpHandler.ServeHTTP(response, request)
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	manager := NewManager(t.TempDir(), []ServerConfig{{
		Name: "http", URL: httpServer.URL, Headers: map[string]string{"Authorization": "Bearer secret"},
	}})
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	tools := runner.AllRegisteredTools()
	if len(tools) != 1 || !headerSeen.Load() {
		t.Fatalf("tools = %d, header seen = %v, status = %#v", len(tools), headerSeen.Load(), manager.Status())
	}
	active.set([]string{tools[0].Definition.Name})
	result, err := extensions.WrapRegisteredTool(tools[0], runner).Execute(context.Background(), "http-call", map[string]any{}, nil)
	if err != nil || contentText(result.Content) != "ping" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if !negotiatedHeaderSeen.Load() {
		t.Fatal("post-initialization JSON requests omitted the negotiated protocol header")
	}
}

func TestManagerDrainsStreamableHTTPProgressBeforeExecuteReturns(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "http-progress", Version: "1"}, nil)
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: "work"}, func(
		ctx context.Context, request *mcpsdk.CallToolRequest, _ map[string]any,
	) (*mcpsdk.CallToolResult, any, error) {
		if err := request.Session.NotifyProgress(ctx, &mcpsdk.ProgressNotificationParams{
			ProgressToken: request.Params.GetProgressToken(), Progress: 1, Total: 2, Message: "working",
		}); err != nil {
			return nil, nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "done"}}}, nil, nil
	})
	var standaloneSSESeen atomic.Bool
	var negotiatedHeaderSeen atomic.Bool
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Mcp-Session-Id") != "" {
			if request.Method == http.MethodGet {
				standaloneSSESeen.Store(true)
			}
			if request.Header.Get("MCP-Protocol-Version") != "" {
				negotiatedHeaderSeen.Store(true)
			}
		}
		mcpHandler.ServeHTTP(response, request)
	}))
	defer httpServer.Close()

	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "http-progress", URL: httpServer.URL}})
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	registered := runner.AllRegisteredTools()[0]
	active.set([]string{registered.Definition.Name})
	tool := extensions.WrapRegisteredTool(registered, runner)
	progressStarted := make(chan struct{})
	releaseProgress := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProgress) }) }
	defer release()
	updates := make(chan string, 1)
	type execution struct {
		result agent.AgentToolResult
		err    error
	}
	done := make(chan execution, 1)
	go func() {
		result, err := tool.Execute(context.Background(), "http-progress", map[string]any{}, func(update agent.AgentToolResult) {
			updates <- contentText(update.Content)
			close(progressStarted)
			<-releaseProgress
		})
		done <- execution{result: result, err: err}
	}()
	select {
	case <-progressStarted:
	case <-time.After(time.Second):
		t.Fatal("HTTP progress handler did not start")
	}
	select {
	case execution := <-done:
		release()
		if execution.err != nil {
			t.Fatal(execution.err)
		}
		t.Fatal("HTTP tool returned before wire-prior progress was delivered")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case execution := <-done:
		if execution.err != nil || contentText(execution.result.Content) != "done" {
			t.Fatalf("result = %#v, error = %v", execution.result, execution.err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP tool did not finish after progress was delivered")
	}
	if update := <-updates; update != "working" {
		t.Fatalf("HTTP progress = %q", update)
	}
	eventually(t, func() bool { return standaloneSSESeen.Load() && negotiatedHeaderSeen.Load() })
}

func TestManagerDrainsJSONResponseStandaloneProgressBeforeExecuteReturns(t *testing.T) {
	progressStarted := make(chan struct{})
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "http-json-progress", Version: "1"}, nil)
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: "work"}, func(
		ctx context.Context, request *mcpsdk.CallToolRequest, _ map[string]any,
	) (*mcpsdk.CallToolResult, any, error) {
		if err := request.Session.NotifyProgress(ctx, &mcpsdk.ProgressNotificationParams{
			ProgressToken: request.Params.GetProgressToken(), Progress: 1, Total: 2, Message: "working",
		}); err != nil {
			return nil, nil, err
		}
		select {
		case <-progressStarted:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "done"}}}, nil, nil
	})
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return server },
		&mcpsdk.StreamableHTTPOptions{JSONResponse: true},
	)
	httpServer := httptest.NewServer(mcpHandler)
	defer httpServer.Close()

	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "http-json-progress", URL: httpServer.URL}})
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	registered := runner.AllRegisteredTools()[0]
	active.set([]string{registered.Definition.Name})
	tool := extensions.WrapRegisteredTool(registered, runner)
	releaseProgress := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProgress) }) }
	defer release()
	updates := make(chan string, 1)
	type execution struct {
		result agent.AgentToolResult
		err    error
	}
	done := make(chan execution, 1)
	go func() {
		result, err := tool.Execute(context.Background(), "http-json-progress", map[string]any{}, func(update agent.AgentToolResult) {
			updates <- contentText(update.Content)
			close(progressStarted)
			<-releaseProgress
		})
		done <- execution{result: result, err: err}
	}()
	select {
	case <-progressStarted:
	case execution := <-done:
		release()
		if execution.err != nil {
			t.Fatal(execution.err)
		}
		t.Fatal("JSON-response HTTP tool returned before standalone progress started")
	case <-time.After(time.Second):
		t.Fatal("standalone progress handler did not start")
	}
	select {
	case execution := <-done:
		release()
		if execution.err != nil {
			t.Fatal(execution.err)
		}
		t.Fatal("JSON-response HTTP tool returned before standalone progress was delivered")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case execution := <-done:
		if execution.err != nil || contentText(execution.result.Content) != "done" {
			t.Fatalf("result = %#v, error = %v", execution.result, execution.err)
		}
	case <-time.After(time.Second):
		t.Fatal("JSON-response HTTP tool did not finish after progress was delivered")
	}
	if update := <-updates; update != "working" {
		t.Fatalf("HTTP progress = %q", update)
	}
}

func TestManagerNoProgressDoesNotEnterAnArrivalWait(t *testing.T) {
	manager := NewManager(t.TempDir(), nil)
	defer closeManager(t, manager)
	registration := &progressRegistration{token: "none", update: func(agent.AgentToolResult) {}}
	manager.progress[registration.token] = registration
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := manager.waitForProgress(ctx, registration); err != nil {
		t.Fatalf("no-progress settlement consulted its cancelled context: %v", err)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.progress[registration.token]; exists {
		t.Fatal("no-progress registration remained open after settlement")
	}
}

func TestManagerSealsRegistrationWhileObservedProgressDrains(t *testing.T) {
	manager := NewManager(t.TempDir(), nil)
	defer closeManager(t, manager)
	updates := make(chan string, 2)
	registration := &progressRegistration{
		token: "sealed",
		update: func(update agent.AgentToolResult) {
			updates <- contentText(update.Content)
		},
	}
	manager.progress[registration.token] = registration
	manager.progressRead(registration.token)

	waitDone := make(chan error, 1)
	go func() { waitDone <- manager.waitForProgress(context.Background(), registration) }()
	eventually(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return registration.sealed
	})

	manager.progressRead(registration.token)
	manager.handleProgress(&mcpsdk.ProgressNotificationParams{ProgressToken: registration.token, Message: "observed"})
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("settlement waited for progress observed after the registration was sealed")
	}
	manager.handleProgress(&mcpsdk.ProgressNotificationParams{ProgressToken: registration.token, Message: "late"})
	close(updates)
	if got := <-updates; got != "observed" {
		t.Fatalf("accepted update = %q, want observed", got)
	}
	if got, exists := <-updates; exists {
		t.Fatalf("accepted post-settlement update %q", got)
	}
}

func TestManagerIgnoresStandaloneProgressObservedAfterCallSettles(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "http-late-progress", Version: "1"}, nil)
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: "work"}, func(
		ctx context.Context, request *mcpsdk.CallToolRequest, _ map[string]any,
	) (*mcpsdk.CallToolResult, any, error) {
		if err := request.Session.NotifyProgress(ctx, &mcpsdk.ProgressNotificationParams{
			ProgressToken: request.Params.GetProgressToken(), Progress: 1, Total: 2, Message: "late",
		}); err != nil {
			return nil, nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "done"}}}, nil, nil
	})
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return server },
		&mcpsdk.StreamableHTTPOptions{JSONResponse: true},
	)
	httpServer := httptest.NewServer(mcpHandler)
	defer httpServer.Close()

	releaseSSE := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSSE) }) }
	defer release()
	progressHandled := make(chan struct{})
	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "http-late-progress", URL: httpServer.URL}})
	manager.connect = gatedStandaloneConnector(httpServer.URL, releaseSSE, progressHandled)
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	registered := runner.AllRegisteredTools()[0]
	active.set([]string{registered.Definition.Name})
	updates := make(chan string, 1)

	result, err := extensions.WrapRegisteredTool(registered, runner).Execute(
		context.Background(), "late-progress", map[string]any{}, func(update agent.AgentToolResult) {
			updates <- contentText(update.Content)
		},
	)
	if err != nil || contentText(result.Content) != "done" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	// Keep the independent SSE body unread past the old grace period. The tool
	// has already settled, so this notification is late by the upstream tool contract.
	time.Sleep(30 * time.Millisecond)
	release()
	select {
	case <-progressHandled:
	case <-time.After(time.Second):
		t.Fatal("late standalone progress was not handled by the SDK")
	}
	select {
	case update := <-updates:
		t.Fatalf("late standalone progress reached the settled tool: %q", update)
	default:
	}
}

func TestManagerMatchesSDKWhitespaceParsingBeforeExecuteReturns(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "http-whitespace-progress", Version: "1"}, nil)
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: "work"}, func(
		ctx context.Context, request *mcpsdk.CallToolRequest, _ map[string]any,
	) (*mcpsdk.CallToolResult, any, error) {
		if err := request.Session.NotifyProgress(ctx, &mcpsdk.ProgressNotificationParams{
			ProgressToken: request.Params.GetProgressToken(), Progress: 1, Total: 2, Message: "working",
		}); err != nil {
			return nil, nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "done"}}}, nil, nil
	})
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mcpHandler.ServeHTTP(&rewriteResponseWriter{
			ResponseWriter: response,
			rewrite: func(data []byte) []byte {
				return bytes.ReplaceAll(data, []byte("event: message\n"), []byte("event:  message\n"))
			},
		}, request)
	}))
	defer httpServer.Close()

	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "http-whitespace-progress", URL: httpServer.URL}})
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	registered := runner.AllRegisteredTools()[0]
	active.set([]string{registered.Definition.Name})
	tool := extensions.WrapRegisteredTool(registered, runner)
	progressStarted := make(chan struct{})
	releaseProgress := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProgress) }) }
	defer release()
	type execution struct {
		result agent.AgentToolResult
		err    error
	}
	done := make(chan execution, 1)
	go func() {
		result, err := tool.Execute(context.Background(), "http-whitespace-progress", map[string]any{}, func(agent.AgentToolResult) {
			close(progressStarted)
			<-releaseProgress
		})
		done <- execution{result: result, err: err}
	}()
	select {
	case <-progressStarted:
	case <-time.After(time.Second):
		t.Fatal("SDK-compatible whitespace progress handler did not start")
	}
	select {
	case execution := <-done:
		release()
		if execution.err != nil {
			t.Fatal(execution.err)
		}
		t.Fatal("HTTP tool returned before SDK-compatible whitespace progress was delivered")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case execution := <-done:
		if execution.err != nil || contentText(execution.result.Content) != "done" {
			t.Fatalf("result = %#v, error = %v", execution.result, execution.err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP tool did not finish after whitespace progress was delivered")
	}
}

func TestManagerMalformedSSEDoesNotLeavePendingProgress(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "http-malformed-progress", Version: "1"}, nil)
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: "work"}, func(
		ctx context.Context, request *mcpsdk.CallToolRequest, _ map[string]any,
	) (*mcpsdk.CallToolResult, any, error) {
		if err := request.Session.NotifyProgress(ctx, &mcpsdk.ProgressNotificationParams{
			ProgressToken: request.Params.GetProgressToken(), Progress: 1, Total: 2, Message: "working",
		}); err != nil {
			return nil, nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "done"}}}, nil, nil
	})
	malformedWritten := make(chan struct{})
	var malformedOnce sync.Once
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mcpHandler.ServeHTTP(&rewriteResponseWriter{
			ResponseWriter: response,
			rewrite: func(data []byte) []byte {
				if !bytes.Contains(data, []byte(`"method":"notifications/progress"`)) {
					return data
				}
				malformedOnce.Do(func() { close(malformedWritten) })
				return bytes.Replace(data, []byte("\n\n"), []byte("\nmalformed\n\n"), 1)
			},
		}, request)
	}))
	defer httpServer.Close()

	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "http-malformed-progress", URL: httpServer.URL}})
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	registered := runner.AllRegisteredTools()[0]
	active.set([]string{registered.Definition.Name})
	tool := extensions.WrapRegisteredTool(registered, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type execution struct {
		result agent.AgentToolResult
		err    error
	}
	done := make(chan execution, 1)
	go func() {
		result, err := tool.Execute(ctx, "http-malformed-progress", map[string]any{}, func(agent.AgentToolResult) {})
		done <- execution{result: result, err: err}
	}()
	select {
	case <-malformedWritten:
	case <-time.After(time.Second):
		t.Fatal("malformed progress event was not written")
	}
	select {
	case execution := <-done:
		if execution.err == nil {
			t.Fatalf("malformed progress result = %#v, want transport error", execution.result)
		}
	case <-time.After(200 * time.Millisecond):
		cancel()
		execution := <-done
		t.Fatalf("malformed progress left Execute blocked until cancellation: %v", execution.err)
	}
}

func TestManagerUsesStdioTransport(t *testing.T) {
	if os.Getenv("PIGO_MCP_HELPER") == "1" {
		return
	}
	manager := NewManager(t.TempDir(), []ServerConfig{{
		Name: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: map[string]string{"PIGO_MCP_HELPER": "1"},
	}})
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	tools := runner.AllRegisteredTools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, status = %#v", len(tools), manager.Status())
	}
	active.set([]string{tools[0].Definition.Name})
	result, err := extensions.WrapRegisteredTool(tools[0], runner).Execute(context.Background(), "stdio-call", map[string]any{}, nil)
	if err != nil || contentText(result.Content) != "ping" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("PIGO_MCP_HELPER") != "1" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stdio-helper", Version: "1"}, nil)
	addTextTool(server, "ping")
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestEmptyManagerRegistersNothingAndConnectsZeroTimes(t *testing.T) {
	manager := NewManager(t.TempDir(), nil)
	var calls atomic.Int64
	manager.connect = func(context.Context, context.Context, ServerConfig, *mcpsdk.ClientOptions, progressTracker) (*mcpsdk.ClientSession, error) {
		calls.Add(1)
		return nil, errors.New("unexpected connection")
	}
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<mcp>", manager.Extension(), extensions.WithHidden(true)); err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{})
	if calls.Load() != 0 || len(runner.AllRegisteredTools()) != 0 || len(runner.RegisteredCommands()) != 0 {
		t.Fatalf("connects = %d, tools = %d, commands = %d", calls.Load(), len(runner.AllRegisteredTools()), len(runner.RegisteredCommands()))
	}
}

func TestManagerIsolatesServerFailuresAndReconnects(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "healthy", Version: "1"}, nil)
	addTextTool(server, "ping")
	manager := NewManager(t.TempDir(), []ServerConfig{
		{Name: "broken", Command: "broken", TimeoutMS: 25},
		{Name: "healthy", Command: "healthy"},
	})
	var healthyConnects atomic.Int64
	connectHealthy := inMemoryConnector(server)
	manager.connect = func(connectCtx, lifecycleCtx context.Context, config ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		if config.Name == "broken" {
			<-connectCtx.Done()
			return nil, connectCtx.Err()
		}
		healthyConnects.Add(1)
		return connectHealthy(connectCtx, lifecycleCtx, config, options, tracker)
	}
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	status := manager.Status()
	if len(status) != 2 || status[0].Name != "broken" || status[0].State != ServerError || status[1].State != ServerConnected {
		t.Fatalf("status = %#v", status)
	}
	tools := runner.AllRegisteredTools()
	if len(tools) != 1 {
		t.Fatalf("healthy tools = %d", len(tools))
	}
	active.set([]string{tools[0].Definition.Name})
	if err := manager.Reconnect(context.Background(), "healthy"); err != nil {
		t.Fatal(err)
	}
	if healthyConnects.Load() != 2 || !reflect.DeepEqual(active.get(), []string{tools[0].Definition.Name}) {
		t.Fatalf("connects = %d, active = %v", healthyConnects.Load(), active.get())
	}
	if err := manager.Reconnect(context.Background(), "missing"); err == nil || !strings.Contains(err.Error(), "unknown server") {
		t.Fatalf("missing reconnect error = %v", err)
	}
}

func TestStableRegisteredToolNamesAreSafeAndDistinct(t *testing.T) {
	first := registeredToolName("same server", "tool/name")
	second := registeredToolName("same-server", "tool name")
	if first == second {
		t.Fatalf("colliding names: %q", first)
	}
	for _, name := range []string{first, second, registeredToolName("服务", "工具")} {
		if len(name) > 64 {
			t.Fatalf("name exceeds provider-safe length: %q", name)
		}
		for _, character := range name {
			safe := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || character == '_' || character == '-'
			if !safe {
				t.Fatalf("unsafe character %q in %q", character, name)
			}
		}
	}
	if first != registeredToolName("same server", "tool/name") {
		t.Fatal("registered tool name is not stable")
	}
}

func exampleServer() *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "example", Version: "1"}, nil)
	type input struct {
		Text string `json:"text"`
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo", Description: "Echo text and an image"}, func(
		ctx context.Context, request *mcpsdk.CallToolRequest, value input,
	) (*mcpsdk.CallToolResult, any, error) {
		if token := request.Params.GetProgressToken(); token != nil {
			_ = request.Session.NotifyProgress(ctx, &mcpsdk.ProgressNotificationParams{
				ProgressToken: token, Progress: 1, Total: 2, Message: "halfway",
			})
		}
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: "echo: " + value.Text},
				&mcpsdk.ImageContent{Data: []byte("pixels"), MIMEType: "image/png"},
			},
			StructuredContent: map[string]any{"echoed": value.Text},
		}, nil, nil
	})
	return server
}

func addTextTool(server *mcpsdk.Server, name string) {
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: name}, func(context.Context, *mcpsdk.CallToolRequest, map[string]any) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: name}}}, nil, nil
	})
}

func inMemoryConnector(server *mcpsdk.Server) connectFunc {
	return func(ctx, _ context.Context, _ ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
		if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
			return nil, err
		}
		return mcpsdk.NewClient(&mcpsdk.Implementation{Name: "pigo-test", Version: "0"}, options).Connect(ctx, tracker.wrapTransport(clientTransport), nil)
	}
}

func gatedStandaloneConnector(endpoint string, release <-chan struct{}, handled chan<- struct{}) connectFunc {
	return func(ctx, _ context.Context, _ ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		copied := *options
		original := copied.ProgressNotificationHandler
		var handledOnce sync.Once
		copied.ProgressNotificationHandler = func(ctx context.Context, request *mcpsdk.ProgressNotificationClientRequest) {
			original(ctx, request)
			handledOnce.Do(func() { close(handled) })
		}
		base := standaloneReadGate{base: http.DefaultTransport, release: release}
		httpClient := &http.Client{Transport: progressRoundTripper{base: base, manager: tracker.manager}}
		transport := &mcpsdk.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "pigo-test", Version: "0"}, &copied)
		return client.Connect(ctx, transport, nil)
	}
}

type standaloneReadGate struct {
	base    http.RoundTripper
	release <-chan struct{}
}

func (gate standaloneReadGate) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := gate.base.RoundTrip(request)
	if err == nil && response.Body != nil && request.Method == http.MethodGet {
		response.Body = &gatedReadCloser{ReadCloser: response.Body, release: gate.release}
	}
	return response, err
}

type gatedReadCloser struct {
	io.ReadCloser
	release <-chan struct{}
	once    sync.Once
}

func (reader *gatedReadCloser) Read(buffer []byte) (int, error) {
	reader.once.Do(func() { <-reader.release })
	return reader.ReadCloser.Read(buffer)
}

type activeTools struct {
	mu    sync.Mutex
	names []string
}

func (active *activeTools) get() []string {
	active.mu.Lock()
	defer active.mu.Unlock()
	return append([]string(nil), active.names...)
}

func (active *activeTools) set(names []string) {
	active.mu.Lock()
	active.names = append([]string(nil), names...)
	active.mu.Unlock()
}

func registerManager(t *testing.T, manager *Manager) (*extensions.Runner, *activeTools) {
	t.Helper()
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<mcp>", manager.Extension(), extensions.WithHidden(true)); err != nil {
		t.Fatal(err)
	}
	active := &activeTools{}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Actions: extensions.Actions{
		GetActiveTools: func() ([]string, error) { return active.get(), nil },
		SetActiveTools: func(names []string) error {
			active.set(names)
			return nil
		},
	}})
	return runner, active
}

func closeManager(t *testing.T, manager *Manager) {
	t.Helper()
	if err := manager.Close(); err != nil {
		t.Errorf("close manager: %v", err)
	}
}

func contentText(content ai.ToolResultContent) string {
	var texts []string
	for _, block := range content {
		if text, ok := block.(*ai.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}

type rewriteResponseWriter struct {
	http.ResponseWriter
	rewrite func([]byte) []byte
}

func (writer *rewriteResponseWriter) Write(data []byte) (int, error) {
	rewritten := writer.rewrite(data)
	if _, err := writer.ResponseWriter.Write(rewritten); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (writer *rewriteResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

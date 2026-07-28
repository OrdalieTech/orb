package api

// Regression tests for the OA-M1 timeout family: TimeoutMS deadlines only the
// header phase (the pinned JS SDKs clear their fetch timeout once response
// headers arrive), Bedrock applies no adapter timeout at all, and aborts
// surface upstream's exact error text instead of raw Go error strings.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestAnthropicTimeoutMSDoesNotKillStreamAfterHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		flusher := response.(http.Flusher)
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		write := func(event, data string) {
			_, _ = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event, data)
			flusher.Flush()
		}
		write("message_start", `{"type":"message_start","message":{"id":"msg_slow","usage":{"input_tokens":1,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`)
		write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		for range 4 {
			time.Sleep(80 * time.Millisecond)
			write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"chunk"}}`)
		}
		write("content_block_stop", `{"type":"content_block_stop","index":0}`)
		write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`)
		write("message_stop", `{"type":"message_stop"}`)
	}))
	defer server.Close()

	model := anthropicTestModel()
	model.BaseURL = server.URL
	key := "fixture-key"
	timeout := int64(150)
	start := time.Now()
	stream, err := StreamAnthropicMessagesWithOptions(context.Background(), model, ai.Context{}, &AnthropicMessagesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &key, TimeoutMS: &timeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonStop {
		errorMessage := ""
		if message.ErrorMessage != nil {
			errorMessage = *message.ErrorMessage
		}
		t.Fatalf("stop reason = %q (%q), want stop past TimeoutMS", message.StopReason, errorMessage)
	}
	text, ok := message.Content[0].(*ai.TextContent)
	if !ok || text.Text != "chunkchunkchunkchunk" {
		t.Fatalf("content = %#v, want the trickled text", message.Content)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("stream finished in %s; the fixture did not outlive TimeoutMS", elapsed)
	}
}

func TestAnthropicTimeoutMSStillBoundsHeaderPhase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	model := anthropicTestModel()
	model.BaseURL = server.URL
	key := "fixture-key"
	timeout := int64(50)
	stream, err := StreamAnthropicMessagesWithOptions(context.Background(), model, ai.Context{}, &AnthropicMessagesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &key, TimeoutMS: &timeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonError {
		t.Fatalf("stop reason = %q, want error before headers", message.StopReason)
	}
	if message.ErrorMessage == nil || *message.ErrorMessage != "Request timed out." {
		t.Fatalf("errorMessage = %v, want the pinned SDK timeout text", message.ErrorMessage)
	}
}

func TestAnthropicInjectedClientTimeoutMSDoesNotRaceBody(t *testing.T) {
	client := anthropic.NewClient(
		option.WithAPIKey("client-owned-key"),
		option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       &contextGatedBody{ctx: request.Context(), delay: 200 * time.Millisecond, reader: strings.NewReader(minimalF2SSE(ai.APIAnthropicMessages, "claude-test"))},
				Request:    request,
			}, nil
		})}),
	)
	timeout := int64(50)
	stream, err := StreamAnthropicMessagesWithOptions(context.Background(), anthropicTestModel(), ai.Context{}, &AnthropicMessagesOptions{
		StreamOptions: ai.StreamOptions{TimeoutMS: &timeout},
		Client:        &client,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonStop || message.ErrorMessage != nil {
		t.Fatalf("message = %#v, want a clean stop past TimeoutMS", message)
	}
}

func TestAnthropicInjectedClientTimeoutMSStillBoundsHeaderPhase(t *testing.T) {
	client := anthropic.NewClient(
		option.WithAPIKey("client-owned-key"),
		option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			select {
			case <-request.Context().Done():
				return nil, request.Context().Err()
			case <-time.After(5 * time.Second):
				return nil, errors.New("header-phase timeout never fired")
			}
		})}),
	)
	timeout := int64(50)
	stream, err := StreamAnthropicMessagesWithOptions(context.Background(), anthropicTestModel(), ai.Context{}, &AnthropicMessagesOptions{
		StreamOptions: ai.StreamOptions{TimeoutMS: &timeout},
		Client:        &client,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonError {
		t.Fatalf("stop reason = %q, want error before headers", message.StopReason)
	}
	if message.ErrorMessage == nil || *message.ErrorMessage != "Request timed out." {
		t.Fatalf("errorMessage = %v, want the pinned SDK timeout text", message.ErrorMessage)
	}
}

// closeRecordingBody records whether Close ran; the header-timeout doer's body
// wrapper releases its context cancel only on Close, so an unclosed error-path
// body leaks that cancel until the caller context ends.
type closeRecordingBody struct {
	io.Reader
	closed atomic.Bool
}

func (body *closeRecordingBody) Close() error {
	body.closed.Store(true)
	return nil
}

func TestNormalizeRequestErrorsCloseTheReplacedBody(t *testing.T) {
	openAIBody := &closeRecordingBody{Reader: strings.NewReader(`{"error":{"message":"boom"}}`)}
	if err := normalizeOpenAIRequestError(&http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       openAIBody,
	}, errors.New("500 boom")); err == nil {
		t.Fatal("want a normalized error")
	}
	if !openAIBody.closed.Load() {
		t.Fatal("normalizeOpenAIRequestError left the replaced body open")
	}

	anthropicBody := &closeRecordingBody{Reader: strings.NewReader(`{"type":"error"}`)}
	if err := normalizeAnthropicRequestError(&http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       anthropicBody,
	}, errors.New("500 boom")); err == nil {
		t.Fatal("want a normalized error")
	}
	if !anthropicBody.closed.Load() {
		t.Fatal("normalizeAnthropicRequestError left the replaced body open")
	}
}

func TestAnthropicRequestErrorClosesResponseBody(t *testing.T) {
	previousClient := anthropicHTTPClient
	body := &closeRecordingBody{Reader: strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)}
	anthropicHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
			Request:    request,
		}, nil
	})}
	defer func() { anthropicHTTPClient = previousClient }()

	key := "fixture-key"
	stream, err := StreamAnthropicMessagesWithOptions(context.Background(), anthropicTestModel(), ai.Context{}, &AnthropicMessagesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &key},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonError {
		t.Fatalf("stop reason = %q, want error", message.StopReason)
	}
	if !body.closed.Load() {
		t.Fatal("request-error response body was never closed")
	}
}

type slowBedrockResponse struct {
	items       []bedrockStreamItem
	index       int
	delay       time.Duration
	sawDeadline *atomic.Bool
}

func (response *slowBedrockResponse) Status() int       { return http.StatusOK }
func (response *slowBedrockResponse) RequestID() string { return "" }
func (response *slowBedrockResponse) Close() error      { return nil }
func (response *slowBedrockResponse) Err() error        { return nil }

func (response *slowBedrockResponse) Next(ctx context.Context) (bedrockStreamItem, bool) {
	if _, ok := ctx.Deadline(); ok {
		response.sawDeadline.Store(true)
	}
	if response.index >= len(response.items) {
		return bedrockStreamItem{}, false
	}
	time.Sleep(response.delay)
	item := response.items[response.index]
	response.index++
	return item, true
}

func TestBedrockTimeoutMSNeverDeadlinesTheStream(t *testing.T) {
	previousTransport := newBedrockTransport
	defer func() { newBedrockTransport = previousTransport }()
	text := "slow"
	var sawDeadline atomic.Bool
	newBedrockTransport = func(ctx context.Context, _ *ai.Model, _ *BedrockConverseStreamOptions) (bedrockTransport, error) {
		if _, ok := ctx.Deadline(); ok {
			sawDeadline.Store(true)
		}
		return bedrockTransportFunc(func(sendCtx context.Context, _ *BedrockConverseStreamPayload) (bedrockResponse, error) {
			if _, ok := sendCtx.Deadline(); ok {
				sawDeadline.Store(true)
			}
			return &slowBedrockResponse{
				items: []bedrockStreamItem{
					{Kind: bedrockItemMessageStart, Role: "assistant"},
					{Kind: bedrockItemContentDelta, ContentBlockIndex: 0, Text: &text},
					{Kind: bedrockItemContentDelta, ContentBlockIndex: 0, Text: &text},
					{Kind: bedrockItemMessageStop, StopReason: "end_turn"},
				},
				delay:       30 * time.Millisecond,
				sawDeadline: &sawDeadline,
			}, nil
		}), nil
	}

	timeout := int64(10)
	stream, err := StreamBedrockConverseWithOptions(context.Background(), bedrockTestModel("anthropic.claude-sonnet-4-5", "Claude"), ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}},
	}, &BedrockConverseStreamOptions{StreamOptions: ai.StreamOptions{TimeoutMS: &timeout}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if sawDeadline.Load() {
		t.Fatal("TimeoutMS installed a deadline; upstream bedrock-converse-stream.ts never applies timeoutMs")
	}
	if message.StopReason != ai.StopReasonStop || message.ErrorMessage != nil {
		t.Fatalf("message = %#v, want a clean stop past TimeoutMS", message)
	}
}

func TestOpenAIResponsesAbortMidStreamPersistsUpstreamAbortText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		flusher := response.(http.Flusher)
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(response, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")
		_, _ = io.WriteString(response, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\"}}\n\n")
		_, _ = io.WriteString(response, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"hel\"}\n\n")
		flusher.Flush()
		<-request.Context().Done()
	}))
	defer server.Close()

	model := responsesTestModel()
	model.BaseURL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := "fixture-key"
	stream, err := StreamOpenAIResponsesWithOptions(ctx, model, ai.Context{}, &OpenAIResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &key},
	})
	if err != nil {
		t.Fatal(err)
	}
	var final ai.ErrorEvent
	sawError := false
	for event, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
		switch typed := event.(type) {
		case ai.TextDeltaEvent:
			cancel()
		case ai.ErrorEvent:
			final = typed
			sawError = true
		}
	}
	if !sawError || final.Reason != ai.StopReasonAborted {
		t.Fatalf("final = %#v (seen=%v), want an aborted error event", final, sawError)
	}
	if final.Error.ErrorMessage == nil || *final.Error.ErrorMessage != "Request was aborted" {
		t.Fatalf("errorMessage = %v, want upstream's abort text", final.Error.ErrorMessage)
	}
}

// abortMidReadBody serves its buffered payload, then cancels the caller's
// context and fails like a torn-down connection, mimicking a user abort while
// the response body is still streaming.
type abortMidReadBody struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (body *abortMidReadBody) Read(buffer []byte) (int, error) {
	n, err := body.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		body.cancel()
		return n, errors.New("read tcp 127.0.0.1:443: use of closed network connection")
	}
	return n, err
}

func (body *abortMidReadBody) Close() error { return nil }

func TestGoogleAbortMidStreamPersistsUpstreamAbortText(t *testing.T) {
	previousClient := googleHTTPClient
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &abortMidReadBody{
				reader: strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hel\"}]}}]}\n\n"),
				cancel: cancel,
			},
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { googleHTTPClient = previousClient })

	apiKey := "key"
	stream, err := StreamGoogleGenerativeAIWithOptions(ctx, googleTestModel("gemini-2.5-flash"), ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}},
	}, &GoogleOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonAborted {
		errorMessage := ""
		if message.ErrorMessage != nil {
			errorMessage = *message.ErrorMessage
		}
		t.Fatalf("stop reason = %q (error %q), want aborted", message.StopReason, errorMessage)
	}
	if message.ErrorMessage == nil || *message.ErrorMessage != "Request was aborted" {
		t.Fatalf("errorMessage = %v, want upstream's abort text", message.ErrorMessage)
	}
}

func TestMistralAbortMidStreamPersistsUpstreamAbortText(t *testing.T) {
	previousClient := mistralHTTPClient
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mistralHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &abortMidReadBody{
				reader: strings.NewReader("data: {\"id\":\"mistral-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"},\"finish_reason\":null}]}\n\n"),
				cancel: cancel,
			},
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { mistralHTTPClient = previousClient })

	apiKey := "test-key"
	stream, err := StreamMistralConversationsWithOptions(ctx, mistralTestModel(), ai.Context{}, &MistralConversationsOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonAborted {
		t.Fatalf("stop reason = %q, want aborted", message.StopReason)
	}
	if message.ErrorMessage == nil || *message.ErrorMessage != "Request was aborted" {
		t.Fatalf("errorMessage = %v, want upstream's abort text", message.ErrorMessage)
	}
}

func TestOpenRouterImagesAbortDuringBodyReadPersistsUpstreamAbortText(t *testing.T) {
	previousClient := openAIHTTPClient
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       &abortMidReadBody{reader: strings.NewReader(`{"id":"img-1","choices":[`), cancel: cancel},
			Request:    request,
		}, nil
	})}
	defer func() { openAIHTTPClient = previousClient }()

	key := "test"
	output, err := GenerateOpenRouterImages(ctx, openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, &ai.ImagesOptions{APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.ImagesStopReasonAborted {
		t.Fatalf("stop reason = %q, want aborted", output.StopReason)
	}
	if output.ErrorMessage == nil || *output.ErrorMessage != "Request aborted" {
		t.Fatalf("errorMessage = %v, want the upstream createAbortError text", output.ErrorMessage)
	}
}

func TestOpenRouterImagesTimeoutMSEmitsStainlessTimeoutHeader(t *testing.T) {
	subsecond := int64(500)
	seconds := int64(5000)
	tests := []struct {
		name      string
		timeoutMS *int64
		want      string
	}{
		{name: "subsecond timeout truncates to 0", timeoutMS: &subsecond, want: "0"},
		{name: "five seconds emits 5", timeoutMS: &seconds, want: "5"},
		{name: "unset timeout stays absent", timeoutMS: nil, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests []*http.Request
			restore := openRouterImagesTransport(t, http.StatusOK, openRouterImagesSuccessBody, &requests, nil)
			defer restore()

			key := "test"
			output, err := GenerateOpenRouterImages(context.Background(), openRouterImagesTestModel(), ai.ImagesContext{
				Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
			}, &ai.ImagesOptions{APIKey: &key, TimeoutMS: test.timeoutMS})
			if err != nil {
				t.Fatal(err)
			}
			if output.StopReason != ai.ImagesStopReasonStop {
				t.Fatalf("stop reason = %q (error %v)", output.StopReason, output.ErrorMessage)
			}
			if len(requests) != 1 {
				t.Fatalf("request count = %d", len(requests))
			}
			if got := requests[0].Header.Get("X-Stainless-Timeout"); got != test.want {
				t.Fatalf("X-Stainless-Timeout = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOpenRouterImagesTimeoutMSDoesNotRaceBodyRead(t *testing.T) {
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       &contextGatedBody{ctx: request.Context(), delay: 200 * time.Millisecond, reader: strings.NewReader(openRouterImagesSuccessBody)},
			Request:    request,
		}, nil
	})}
	defer func() { openAIHTTPClient = previousClient }()

	key := "test"
	timeout := int64(50)
	output, err := GenerateOpenRouterImages(context.Background(), openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "Generate a dog"}},
	}, &ai.ImagesOptions{APIKey: &key, TimeoutMS: &timeout})
	if err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.ImagesStopReasonStop || output.ErrorMessage != nil {
		t.Fatalf("output = %#v, want a clean stop past TimeoutMS", output)
	}
	if len(output.Output) != 2 {
		t.Fatalf("output blocks = %#v, want text plus image", output.Output)
	}
}

func TestRetryProviderRequestAbortReplacesRequestError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := retryProviderRequest(ctx, nil, func() (string, error) {
		cancel()
		return "", errors.New("read tcp 127.0.0.1: use of closed network connection")
	})
	if err == nil || err.Error() != "Request aborted" {
		t.Fatalf("err = %v, want the upstream createAbortError text", err)
	}
}
